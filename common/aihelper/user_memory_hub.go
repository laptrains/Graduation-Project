package aihelper

import (
	"GopherAI/config"
	"GopherAI/pkg/memory"
	"context"
	"sync"
)

// userMemoryHub 按用户缓存长期记忆：同一用户的所有会话共享同一个 *memory.UserMemory，
// 因此 A 会话提取到的记忆，B 会话下一轮组装上下文时即可读到。
type userMemoryHub struct {
	mu    sync.Mutex
	users map[string]*memory.UserMemory
}

var (
	hub     *userMemoryHub
	hubOnce sync.Once
)

func getUserMemoryHub() *userMemoryHub {
	hubOnce.Do(func() {
		hub = &userMemoryHub{users: make(map[string]*memory.UserMemory)}
	})
	return hub
}

// Get 获取用户长期记忆，首次访问时从 DB 加载；配置关闭时返回 nil（不启用 L3）
func (h *userMemoryHub) Get(ctx context.Context, userName string) (*memory.UserMemory, error) {
	if !config.GetConfig().MemoryConfig.UserMemoryEnabled {
		return nil, nil
	}

	h.mu.Lock()
	u, ok := h.users[userName]
	h.mu.Unlock()
	if ok {
		return u, nil
	}

	// DB 加载放在锁外，避免阻塞其他用户
	loaded, err := memory.LoadUserMemory(ctx, userName, getMemoryConfig(), memory.NewUserDBStore())
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	// 并发加载时以先写入者为准，保证同一用户只有一份实例
	if existing, ok := h.users[userName]; ok {
		return existing, nil
	}
	h.users[userName] = loaded
	return loaded, nil
}
