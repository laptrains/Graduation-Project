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
	mu      sync.RWMutex
}

// NewAIHelperManager 创建新的管理器实例
func NewAIHelperManager() *AIHelperManager {
	return &AIHelperManager{
		helpers: make(map[string]map[string]*AIHelper),
	}
}

// GetOrCreateAIHelper 获取或创建AIHelper；首次访问会话时从 DB 懒加载有界记忆（摘要 + 近期窗口）
func (m *AIHelperManager) GetOrCreateAIHelper(userName string, sessionID string, modelType string, config map[string]interface{}) (*AIHelper, error) {
	if helper, ok := m.GetAIHelper(userName, sessionID); ok {
		return helper, nil
	}

	// 加载记忆涉及 DB IO，放在全局锁之外，避免阻塞其他会话
	user, err := getUserMemoryHub().Get(ctx, userName)
	if err != nil {
		return nil, err
	}
	mem, err := memory.Load(ctx, sessionID, getMemoryConfig(), memory.NewDBStore(), user)
	if err != nil {
		return nil, err
	}
	helper, err := GetGlobalFactory().CreateAIHelper(ctx, modelType, sessionID, userName, config, mem)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	userHelpers, exists := m.helpers[userName]
	if !exists {
		userHelpers = make(map[string]*AIHelper)
		m.helpers[userName] = userHelpers
	}
	// 并发创建时以先写入者为准
	if existing, ok := userHelpers[sessionID]; ok {
		return existing, nil
	}
	userHelpers[sessionID] = helper
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

// 移除指定用户的指定会话的AIHelper
func (m *AIHelperManager) RemoveAIHelper(userName string, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	userHelpers, exists := m.helpers[userName]
	if !exists {
		return
	}

	delete(userHelpers, sessionID)

	// 如果用户没有会话了，清理用户映射
	if len(userHelpers) == 0 {
		delete(m.helpers, userName)
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
