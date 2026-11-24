package memory

import (
	"GopherAI/dao/digest"
	"GopherAI/dao/message"
	"GopherAI/dao/usermemory"
	"GopherAI/model"
	"context"
	"errors"

	"github.com/cloudwego/eino/schema"
)

// Store 记忆依赖的持久化能力，便于单测替换
type Store interface {
	// GetDigest 查询会话摘要，不存在返回 nil, nil
	GetDigest(ctx context.Context, sessionID string) (*model.SessionDigest, error)
	// SaveDigest 乐观锁写入摘要
	SaveDigest(ctx context.Context, d *model.SessionDigest, expectVersion int) error
	// CountMessages 统计会话消息总数
	CountMessages(ctx context.Context, sessionID string) (int64, error)
	// ListMessages 按 id 升序分段读取会话消息
	ListMessages(ctx context.Context, sessionID string, offset, limit int) ([]model.Message, error)
	// GetExtractState 查询会话长期记忆提取水位线，返回 (水位线, 记录是否存在)
	GetExtractState(ctx context.Context, sessionID string) (int, bool, error)
}

// 长期记忆提交冲突
var (
	ErrUserMemoryConflict = errors.New("memory: user memory version conflict")
	ErrExtractConflict    = errors.New("memory: session extract cursor conflict")
)

// ExtractionCommit 一次提取的提交内容：记忆变更（可选）+ 会话水位线推进，须原子提交
type ExtractionCommit struct {
	UserName      string
	WriteMemory   bool
	Entries       string
	ExpectVersion int
	NewVersion    int

	SessionID     string
	ExtractExists bool
	ExtractExpect int
	ExtractTo     int
}

// UserMemoryStore 长期记忆的持久化能力
type UserMemoryStore interface {
	// GetUserMemory 查询用户长期记忆，不存在返回 nil, nil
	GetUserMemory(ctx context.Context, userName string) (*model.UserMemory, error)
	// CommitExtraction 事务提交记忆变更与水位线；冲突返回 ErrUserMemoryConflict / ErrExtractConflict
	CommitExtraction(ctx context.Context, c ExtractionCommit) error
}

// Summarizer 摘要生成能力（由 LLM 实现），输入完整提示词消息，返回摘要正文
type Summarizer interface {
	Summarize(ctx context.Context, msgs []*schema.Message) (string, error)
}

// Extractor 长期记忆提取能力（由 LLM 实现），输入完整提示词消息，返回 JSON 操作指令
type Extractor interface {
	Extract(ctx context.Context, msgs []*schema.Message) (string, error)
}

type dbStore struct{}

// NewDBStore 返回基于 MySQL 的 Store 实现
func NewDBStore() Store {
	return dbStore{}
}

// NewUserDBStore 返回基于 MySQL 的 UserMemoryStore 实现
func NewUserDBStore() UserMemoryStore {
	return dbStore{}
}

func (dbStore) GetDigest(ctx context.Context, sessionID string) (*model.SessionDigest, error) {
	return digest.GetBySessionID(ctx, sessionID)
}

func (dbStore) SaveDigest(ctx context.Context, d *model.SessionDigest, expectVersion int) error {
	return digest.Save(ctx, d, expectVersion)
}

func (dbStore) CountMessages(ctx context.Context, sessionID string) (int64, error) {
	return message.CountBySessionID(ctx, sessionID)
}

func (dbStore) ListMessages(ctx context.Context, sessionID string, offset, limit int) ([]model.Message, error) {
	return message.ListBySessionIDRange(ctx, sessionID, offset, limit)
}

func (dbStore) GetExtractState(ctx context.Context, sessionID string) (int, bool, error) {
	return usermemory.GetExtractState(ctx, sessionID)
}

func (dbStore) GetUserMemory(ctx context.Context, userName string) (*model.UserMemory, error) {
	return usermemory.GetByUserName(ctx, userName)
}

func (dbStore) CommitExtraction(ctx context.Context, c ExtractionCommit) error {
	err := usermemory.Commit(ctx, usermemory.CommitParams{
		UserName:      c.UserName,
		WriteMemory:   c.WriteMemory,
		Entries:       c.Entries,
		ExpectVersion: c.ExpectVersion,
		NewVersion:    c.NewVersion,
		SessionID:     c.SessionID,
		ExtractExists: c.ExtractExists,
		ExtractExpect: c.ExtractExpect,
		ExtractTo:     c.ExtractTo,
	})
	switch {
	case errors.Is(err, usermemory.ErrMemoryConflict):
		return ErrUserMemoryConflict
	case errors.Is(err, usermemory.ErrExtractConflict):
		return ErrExtractConflict
	}
	return err
}
