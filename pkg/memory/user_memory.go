package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
)

// 记忆条目类型
const (
	EntryConstraint = "constraint"
	EntryPreference = "preference"
	EntryFact       = "fact"
)

// MemoryEntry 一条长期记忆
type MemoryEntry struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
	// Src 来源会话，仅用于排查
	Src string `json:"src,omitempty"`
}

// MemoryOp 提取模型输出的增量操作
type MemoryOp struct {
	Op   string `json:"op"` // add / update / delete
	ID   int    `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

// UserMemory 用户级长期记忆（L3）：跨会话共享，不参与滚动摘要。
// 变更只通过 add/update/delete 操作增量合并，未被点名的条目保持不变。
type UserMemory struct {
	userName string
	cfg      Config
	store    UserMemoryStore

	mu      sync.RWMutex
	entries []MemoryEntry
	version int

	// commitMu 串行化同一用户的记忆写入（多个会话可能同时提取）
	commitMu sync.Mutex
}

// LoadUserMemory 从存储加载用户长期记忆
func LoadUserMemory(ctx context.Context, userName string, cfg Config, store UserMemoryStore) (*UserMemory, error) {
	u := &UserMemory{userName: userName, cfg: cfg.Normalize(), store: store}
	if err := u.reload(ctx); err != nil {
		return nil, err
	}
	return u, nil
}

func (u *UserMemory) reload(ctx context.Context) error {
	rec, err := u.store.GetUserMemory(ctx, u.userName)
	if err != nil {
		return fmt.Errorf("load user memory: %w", err)
	}
	var entries []MemoryEntry
	version := 0
	if rec != nil {
		if entries, err = decodeEntries(rec.Entries); err != nil {
			return fmt.Errorf("decode user memory: %w", err)
		}
		version = rec.Version
	}
	u.mu.Lock()
	u.entries, u.version = entries, version
	u.mu.Unlock()
	return nil
}

// Entries 返回条目快照
func (u *UserMemory) Entries() []MemoryEntry {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return cloneEntries(u.entries)
}

// Render 渲染为注入主对话的上位规则文本，无条目返回空串
func (u *UserMemory) Render() string {
	if u == nil {
		return ""
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	return renderEntries(u.entries)
}

// extractCursor 本次提取对应的会话水位线推进
type extractCursor struct {
	exists bool
	expect int
	to     int
}

// commit 合并 ops 并与会话水位线在同一事务中提交；
// 记忆版本冲突（其他实例写入）时重新加载并重放一次 ops。
func (u *UserMemory) commit(ctx context.Context, sessionID string, ops []MemoryOp, cur extractCursor) (int, []string, error) {
	u.commitMu.Lock()
	defer u.commitMu.Unlock()

	for attempt := 0; ; attempt++ {
		u.mu.RLock()
		base, version := cloneEntries(u.entries), u.version
		u.mu.RUnlock()

		next, applied, rejected := applyOps(base, ops, sessionID, u.cfg)
		c := ExtractionCommit{
			UserName:      u.userName,
			SessionID:     sessionID,
			ExtractExists: cur.exists,
			ExtractExpect: cur.expect,
			ExtractTo:     cur.to,
		}
		if applied > 0 {
			data, err := json.Marshal(next)
			if err != nil {
				return 0, nil, fmt.Errorf("encode user memory: %w", err)
			}
			c.WriteMemory, c.Entries = true, string(data)
			c.ExpectVersion, c.NewVersion = version, version+1
		}

		err := u.store.CommitExtraction(ctx, c)
		if err == nil {
			if applied > 0 {
				u.mu.Lock()
				u.entries, u.version = next, version+1
				u.mu.Unlock()
			}
			return applied, rejected, nil
		}
		if !errors.Is(err, ErrUserMemoryConflict) || attempt >= 1 {
			return 0, nil, err
		}
		log.Printf("[memory] user=%s memory version conflict, reload and replay", u.userName)
		if err := u.reload(ctx); err != nil {
			return 0, nil, err
		}
	}
}

// applyOps 纯函数：把 ops 增量合并到 base。
// 超出上限的新增/修改被拒绝而不是淘汰旧条目，保证早期约束不会被悄悄挤掉。
func applyOps(base []MemoryEntry, ops []MemoryOp, src string, cfg Config) ([]MemoryEntry, int, []string) {
	out := cloneEntries(base)
	nextID, total := 1, 0
	for _, e := range out {
		if e.ID >= nextID {
			nextID = e.ID + 1
		}
		total += runeLen(e.Text)
	}

	applied := 0
	var rejected []string
	reject := func(op MemoryOp, reason string) {
		rejected = append(rejected, fmt.Sprintf("%s(id=%d,%q): %s", op.Op, op.ID, op.Text, reason))
	}

	for _, op := range ops {
		text := strings.TrimSpace(op.Text)
		n := runeLen(text)
		switch strings.ToLower(strings.TrimSpace(op.Op)) {
		case "add":
			switch {
			case text == "":
				reject(op, "empty text")
			case n > cfg.UserMemoryEntryMaxChars:
				reject(op, "entry too long")
			case indexOfText(out, text) >= 0:
				// 已存在相同内容，忽略
			case len(out) >= cfg.UserMemoryMaxEntries:
				reject(op, "entries full")
			case total+n > cfg.UserMemoryMaxChars:
				reject(op, "total chars exceeded")
			default:
				out = append(out, MemoryEntry{ID: nextID, Type: normalizeType(op.Type), Text: text, Src: src})
				nextID++
				total += n
				applied++
			}
		case "update":
			i := indexOfID(out, op.ID)
			switch {
			case i < 0:
				reject(op, "id not found")
			case text == "":
				reject(op, "empty text")
			case n > cfg.UserMemoryEntryMaxChars:
				reject(op, "entry too long")
			case total-runeLen(out[i].Text)+n > cfg.UserMemoryMaxChars:
				reject(op, "total chars exceeded")
			case text == out[i].Text && (op.Type == "" || normalizeType(op.Type) == out[i].Type):
				// 无变化
			default:
				total += n - runeLen(out[i].Text)
				out[i].Text, out[i].Src = text, src
				if op.Type != "" {
					out[i].Type = normalizeType(op.Type)
				}
				applied++
			}
		case "delete":
			i := indexOfID(out, op.ID)
			if i < 0 {
				reject(op, "id not found")
				continue
			}
			total -= runeLen(out[i].Text)
			out = append(out[:i], out[i+1:]...)
			applied++
		default:
			reject(op, "unknown op")
		}
	}
	return out, applied, rejected
}

// renderEntries 按 约束→偏好→事实 的顺序渲染
func renderEntries(entries []MemoryEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	for _, typ := range []string{EntryConstraint, EntryPreference, EntryFact} {
		for _, e := range entries {
			if e.Type == typ {
				fmt.Fprintf(&b, "- [%s] %s\n", typeLabel(typ), e.Text)
			}
		}
	}
	return fmt.Sprintf(userMemoryInjectPrompt, strings.TrimRight(b.String(), "\n"))
}

func normalizeType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case EntryConstraint, "约束":
		return EntryConstraint
	case EntryPreference, "偏好":
		return EntryPreference
	default:
		return EntryFact
	}
}

func typeLabel(t string) string {
	switch t {
	case EntryConstraint:
		return "约束"
	case EntryPreference:
		return "偏好"
	default:
		return "事实"
	}
}

func indexOfID(entries []MemoryEntry, id int) int {
	for i, e := range entries {
		if e.ID == id {
			return i
		}
	}
	return -1
}

func indexOfText(entries []MemoryEntry, text string) int {
	for i, e := range entries {
		if e.Text == text {
			return i
		}
	}
	return -1
}

func cloneEntries(in []MemoryEntry) []MemoryEntry {
	if in == nil {
		return nil
	}
	out := make([]MemoryEntry, len(in))
	copy(out, in)
	return out
}

func decodeEntries(s string) ([]MemoryEntry, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var entries []MemoryEntry
	if err := json.Unmarshal([]byte(s), &entries); err != nil {
		return nil, err
	}
	return entries, nil
}
