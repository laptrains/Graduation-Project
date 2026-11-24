package memory

import (
	"GopherAI/model"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
)

// Turn 一条对话消息
type Turn struct {
	IsUser  bool
	Content string
}

// SessionMemory 单个会话的有界记忆：L1 近期窗口 + L2 滚动摘要，并关联用户级 L3 长期记忆
type SessionMemory struct {
	sessionID string
	cfg       Config
	store     Store

	mu     sync.RWMutex
	window []Turn
	digest string
	// windowStart window[0] 在该会话全部消息（id 升序）中的位置，即摘要水位线
	windowStart int
	version     int

	compressing atomic.Bool

	// user 该会话所属用户的长期记忆（同一用户的所有会话共享），nil 表示未启用
	user *UserMemory
	// extracted 提取水位线：前 extracted 条消息已做过长期记忆提取（与摘要水位线相互独立）
	extracted int
	// extractExpect/extractExists DB 中水位线的当前值与记录是否存在，用于乐观锁提交
	extractExpect int
	extractExists bool
	extracting    atomic.Bool
}

// New 创建空记忆（新会话）
func New(sessionID string, cfg Config, store Store) *SessionMemory {
	return &SessionMemory{
		sessionID: sessionID,
		cfg:       cfg.Normalize(),
		store:     store,
	}
}

// Load 从存储懒加载会话记忆：摘要 + 水位线之后的最近消息，加载量与历史总量无关。
// user 为该用户共享的长期记忆，传 nil 表示不启用 L3。
func Load(ctx context.Context, sessionID string, cfg Config, store Store, user *UserMemory) (*SessionMemory, error) {
	m := New(sessionID, cfg, store)

	d, err := store.GetDigest(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load digest: %w", err)
	}
	if d != nil {
		m.digest = d.Content
		m.windowStart = d.DigestedCount
		m.version = d.Version
	}

	total, err := store.CountMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("count messages: %w", err)
	}

	if user != nil {
		cnt, exists, err := store.GetExtractState(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("load extract state: %w", err)
		}
		m.user = user
		m.extracted, m.extractExpect, m.extractExists = cnt, cnt, exists
		// 存量老会话不回溯全部历史，只从最近 WindowHardLimit 条开始提取
		if floor := int(total) - m.cfg.WindowHardLimit; m.extracted < floor {
			m.extracted = floor
		}
	}

	// 未摘要消息过多（如存量老会话）时只加载最近一段，更早的部分不再进入上下文
	if int(total)-m.windowStart > m.cfg.WindowHardLimit {
		m.windowStart = int(total) - m.cfg.WindowHardLimit
	}
	if int(total) <= m.windowStart {
		return m, nil
	}

	msgs, err := store.ListMessages(ctx, sessionID, m.windowStart, m.cfg.WindowHardLimit)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	m.window = make([]Turn, 0, len(msgs))
	for _, msg := range msgs {
		m.window = append(m.window, Turn{IsUser: msg.IsUser, Content: msg.Content})
	}
	return m, nil
}

// Config 返回生效的记忆参数
func (m *SessionMemory) Config() Config {
	return m.cfg
}

// Append 追加一条消息到近期窗口
func (m *SessionMemory) Append(t Turn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.window = append(m.window, t)
}

// Window 返回近期窗口快照
func (m *SessionMemory) Window() []Turn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Turn, len(m.window))
	copy(out, m.window)
	return out
}

// Digest 返回当前摘要
func (m *SessionMemory) Digest() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.digest
}

// NeedCompress 窗口超过阈值且当前没有压缩任务时返回 true
func (m *SessionMemory) NeedCompress() bool {
	if m.compressing.Load() {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.window) > m.cfg.DigestThreshold
}

// Compress 滚动摘要：把窗口中除最近 KeepRecent 条以外的消息与旧摘要整合为新摘要。
// 先落库再修改内存，失败时内存保持不变；同一会话同时只会有一个压缩任务。
func (m *SessionMemory) Compress(ctx context.Context, s Summarizer) error {
	if s == nil {
		return errors.New("memory: summarizer is nil")
	}
	if !m.compressing.CompareAndSwap(false, true) { //这里？：
		return nil
	}
	defer m.compressing.Store(false)

	m.mu.RLock()
	n := len(m.window) - m.cfg.KeepRecent
	if n <= 0 {
		m.mu.RUnlock()
		return nil
	}
	//要压缩的消息toDigest
	toDigest := make([]Turn, n)
	copy(toDigest, m.window[:n])
	oldDigest, start, version := m.digest, m.windowStart, m.version
	m.mu.RUnlock()

	// LLM 调用期间不持锁，新消息只会追加到窗口尾部
	newDigest, err := s.Summarize(ctx, buildSummarizeMessages(oldDigest, toDigest, m.cfg.DigestMaxChars))
	if err != nil {
		return fmt.Errorf("memory: summarize: %w", err)
	}
	newDigest = truncateRunes(strings.TrimSpace(newDigest), m.cfg.DigestMaxChars)
	if newDigest == "" {
		return errors.New("memory: summarizer returned empty digest")
	}

	rec := &model.SessionDigest{
		SessionID:     m.sessionID,
		Content:       newDigest,
		DigestedCount: start + n,
		Version:       version + 1,
	}
	if err := m.store.SaveDigest(ctx, rec, version); err != nil {
		return fmt.Errorf("memory: save digest: %w", err)
	}

	m.mu.Lock()
	m.window = append([]Turn(nil), m.window[n:]...)
	m.digest = newDigest
	m.windowStart = start + n
	m.version = version + 1
	m.mu.Unlock()

	log.Printf("[memory] session=%s compressed %d messages, digest version=%d, window=%d",
		m.sessionID, n, version+1, len(m.window))
	return nil
}
