package memory

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// NeedExtract 未提取消息数达到阈值且当前没有提取任务时返回 true
func (m *SessionMemory) NeedExtract() bool {
	if m.user == nil || m.extracting.Load() {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.windowStart+len(m.window)-m.extracted >= m.cfg.ExtractThreshold
}

// Extract 长期记忆提取：独立于摘要的流水线，拥有自己的水位线。
// 待提取消息仍在窗口中时直接使用内存原文；已被摘要压出窗口时回 DB 读取，因此与摘要的先后顺序无关。
// 记忆变更与水位线推进同一事务提交，失败时水位线不动，下次重试不漏不重。
func (m *SessionMemory) Extract(ctx context.Context, e Extractor) error {
	if m.user == nil {
		return nil
	}
	if e == nil {
		return errors.New("memory: extractor is nil")
	}
	if !m.extracting.CompareAndSwap(false, true) {
		return nil
	}
	defer m.extracting.Store(false)

	m.mu.RLock()
	start, total := m.windowStart, m.windowStart+len(m.window)
	from, expect, exists := m.extracted, m.extractExpect, m.extractExists
	to := min(total, from+m.cfg.ExtractBatch)
	var turns []Turn
	if from >= start && to > from {
		turns = make([]Turn, to-from)
		copy(turns, m.window[from-start:to-start])
	}
	m.mu.RUnlock()

	if from < start {
		// 混合段：本轮只提取 DB 段 [from, start)，水位线由 loadTurns 改写（不超过 start），
		// 窗口段 [start, total) 留给下一轮从内存原文提取
		var err error
		if turns, to, err = m.loadTurns(ctx, from, min(to, start), start); err != nil {
			return err
		}
	}
	if to <= from {
		return nil
	}

	var ops []MemoryOp
	if len(turns) > 0 {
		raw, err := e.Extract(ctx, buildExtractMessages(m.user.Entries(), turns, m.cfg.UserMemoryEntryMaxChars))
		if err != nil {
			return fmt.Errorf("memory: extract: %w", err)
		}
		if ops, err = parseOps(raw); err != nil {
			return err
		}
	}

	applied, rejected, err := m.user.commit(ctx, m.sessionID, ops, extractCursor{exists: exists, expect: expect, to: to})
	if err != nil {
		return fmt.Errorf("memory: commit extraction: %w", err)
	}

	m.mu.Lock()
	m.extracted, m.extractExpect, m.extractExists = to, to, true
	m.mu.Unlock()

	log.Printf("[memory] session=%s user=%s extracted messages [%d,%d), ops=%d applied=%d",
		m.sessionID, m.user.userName, from, to, len(ops), applied)
	for _, r := range rejected {
		log.Printf("[memory] user=%s op rejected: %s", m.user.userName, r)
	}
	return nil
}

// loadTurns 从 DB 读取已被压出窗口的消息 [from, to)。
// 若 DB 中缺失这段消息（批量写入失败丢弃），跳过到窗口起点，避免水位线永久卡住。
func (m *SessionMemory) loadTurns(ctx context.Context, from, to, start int) ([]Turn, int, error) {
	msgs, err := m.store.ListMessages(ctx, m.sessionID, from, to-from)
	if err != nil {
		return nil, from, fmt.Errorf("memory: load messages for extract: %w", err)
	}
	if len(msgs) == 0 {
		log.Printf("[memory] session=%s messages [%d,%d) missing in db, skip extract", m.sessionID, from, start)
		return nil, start, nil
	}
	turns := make([]Turn, 0, len(msgs))
	for _, msg := range msgs {
		turns = append(turns, Turn{IsUser: msg.IsUser, Content: msg.Content})
	}
	return turns, from + len(msgs), nil
}
