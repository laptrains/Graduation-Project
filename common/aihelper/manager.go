package aihelper

import (
	"GopherAI/pkg/memory"
	"context"
	"sync"
)

var ctx = context.Background()

// AIHelperManager AI助手管理器，管理用户-会话-AIHelper的映射关系
type AIHelperManager struct {
	helpers map[string]map[string]*AIHelper // map[用户账号（唯一）]map[会话ID]*AIHelper
	// loadHelper 创建新 helper 的完整流程（加载记忆 + 创建模型）。
	// 抽成字段是为了可测试性：测试注入 fake 实现，不触碰 DB 与全局 factory
	loadHelper func(ctx context.Context, userName, sessionID, modelType string, config map[string]interface{}) (*AIHelper, error)
	mu         sync.RWMutex
}

// NewAIHelperManager 创建新的管理器实例
func NewAIHelperManager() *AIHelperManager {
	return &AIHelperManager{
		helpers:    make(map[string]map[string]*AIHelper),
		loadHelper: defaultLoadHelper,
	}
}

// defaultLoadHelper 默认创建流程：用户记忆 → 懒加载会话记忆 → 工厂创建模型
func defaultLoadHelper(ctx context.Context, userName, sessionID, modelType string, config map[string]interface{}) (*AIHelper, error) {
	user, err := getUserMemoryHub().Get(ctx, userName)
	if err != nil {
		return nil, err
	}
	mem, err := memory.Load(ctx, sessionID, getMemoryConfig(), memory.NewDBStore(), user)
	if err != nil {
		return nil, err
	}
	return GetGlobalFactory().CreateAIHelper(ctx, modelType, sessionID, userName, config, mem)
}

// GetOrCreateAIHelper 获取或创建AIHelper；首次访问会话时从 DB 懒加载有界记忆（摘要 + 近期窗口）。
// 同一会话请求了不同 modelType（如从普通对话切到 MCP 设备控制）时，旧 helper 会被替换并释放资源
func (m *AIHelperManager) GetOrCreateAIHelper(userName string, sessionID string, modelType string, config map[string]interface{}) (*AIHelper, error) {
	m.mu.RLock()
	existing, ok := m.helpers[userName][sessionID]
	m.mu.RUnlock()
	if ok && existing.ModelType() == modelType {
		return existing, nil
	}

	// 加载记忆涉及 DB IO，放在全局锁之外，避免阻塞其他会话
	helper, err := m.loadHelper(ctx, userName, sessionID, modelType, config)
	if err != nil {
		return nil, err
	}

	var old *AIHelper
	m.mu.Lock()
	userHelpers, exists := m.helpers[userName]
	if !exists {
		userHelpers = make(map[string]*AIHelper)
		m.helpers[userName] = userHelpers
	}
	if existing, ok := userHelpers[sessionID]; ok {
		if existing.ModelType() == modelType {
			// 并发创建时以先写入者为准
			m.mu.Unlock()
			return existing, nil
		}
		// modelType 已切换：替换旧 helper，锁外释放其持有的资源（如 MCP 连接）
		old = existing
	}
	userHelpers[sessionID] = helper
	m.mu.Unlock()

	if old != nil {
		old.Close()
	}
	return helper, nil
}

// 获取指定用户的指定会话的AIHelper
func (m *AIHelperManager) GetAIHelper(userName string, sessionID string) (*AIHelper, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	userHelpers, exists := m.helpers[userName]
	if !exists {
		return nil, false
	}

	helper, exists := userHelpers[sessionID]
	return helper, exists
}

// 移除指定用户的指定会话的AIHelper，并释放其持有的资源（如 MCP 连接）
func (m *AIHelperManager) RemoveAIHelper(userName string, sessionID string) {
	m.mu.Lock()
	userHelpers, exists := m.helpers[userName]
	if !exists {
		m.mu.Unlock()
		return
	}
	helper, ok := userHelpers[sessionID]
	if ok {
		delete(userHelpers, sessionID)
	}
	// 如果用户没有会话了，清理用户映射
	if len(userHelpers) == 0 {
		delete(m.helpers, userName)
	}
	m.mu.Unlock()

	if ok {
		helper.Close()
	}
}

// 获取指定用户的所有会话ID
func (m *AIHelperManager) GetUserSessions(userName string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	userHelpers, exists := m.helpers[userName]
	if !exists {
		return []string{}
	}

	sessionIDs := make([]string, 0, len(userHelpers))
	//取出所有的key
	for sessionID := range userHelpers {
		sessionIDs = append(sessionIDs, sessionID)
	}

	return sessionIDs
}

// 全局管理器实例
var globalManager *AIHelperManager
var once sync.Once

// GetGlobalManager 获取全局管理器实例
func GetGlobalManager() *AIHelperManager {
	once.Do(func() {
		globalManager = NewAIHelperManager()
	})
	return globalManager
}
