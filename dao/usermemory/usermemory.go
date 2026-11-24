// Package usermemory 用户长期记忆与会话提取进度的数据访问层
package usermemory

import (
	"GopherAI/common/mysql"
	"GopherAI/model"
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

var (
	// ErrMemoryConflict 长期记忆已被其他任务更新（乐观锁冲突）
	ErrMemoryConflict = errors.New("user memory version conflict")
	// ErrExtractConflict 会话提取水位线已被其他任务推进
	ErrExtractConflict = errors.New("session extract cursor conflict")
)

// CommitParams 一次提取的提交内容：长期记忆变更（可选）+ 会话水位线推进，同一事务提交
type CommitParams struct {
	UserName      string
	WriteMemory   bool
	Entries       string
	ExpectVersion int // 0 表示记忆记录尚不存在
	NewVersion    int

	SessionID     string
	ExtractExists bool // 水位线记录是否已存在
	ExtractExpect int
	ExtractTo     int
}

// GetByUserName 查询用户长期记忆，不存在时返回 nil, nil
func GetByUserName(ctx context.Context, userName string) (*model.UserMemory, error) {
	var m model.UserMemory
	err := mysql.DB.WithContext(ctx).Where("user_name = ?", userName).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user memory: %w", err)
	}
	return &m, nil
}

// GetExtractState 查询会话提取水位线，返回 (水位线, 记录是否存在)
func GetExtractState(ctx context.Context, sessionID string) (int, bool, error) {
	var e model.SessionExtract
	err := mysql.DB.WithContext(ctx).Where("session_id = ?", sessionID).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("get session extract: %w", err)
	}
	return e.ExtractedCount, true, nil
}

// Commit 在同一事务内写入长期记忆并推进会话水位线，任一步失败整体回滚，
// 保证重试时不漏提取、不重复写入。
func Commit(ctx context.Context, p CommitParams) error {
	return mysql.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if p.WriteMemory {
			if err := saveMemory(tx, p); err != nil {
				return err
			}
		}
		return advanceExtract(tx, p)
	})
}

// saveMemory 保存长期记忆
func saveMemory(tx *gorm.DB, p CommitParams) error {
	if p.ExpectVersion == 0 {
		err := tx.Create(&model.UserMemory{UserName: p.UserName, Entries: p.Entries, Version: p.NewVersion}).Error
		if isDuplicate(err) {
			return ErrMemoryConflict
		}
		if err != nil {
			return fmt.Errorf("create user memory: %w", err)
		}
		return nil
	}
	res := tx.Model(&model.UserMemory{}).
		Where("user_name = ? AND version = ?", p.UserName, p.ExpectVersion).
		Updates(map[string]interface{}{"entries": p.Entries, "version": p.NewVersion})
	if res.Error != nil {
		return fmt.Errorf("update user memory: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrMemoryConflict
	}
	return nil
}

// advanceExtract 推进会话水位线
func advanceExtract(tx *gorm.DB, p CommitParams) error {
	if !p.ExtractExists {
		err := tx.Create(&model.SessionExtract{SessionID: p.SessionID, ExtractedCount: p.ExtractTo}).Error
		if isDuplicate(err) {
			return ErrExtractConflict
		}
		if err != nil {
			return fmt.Errorf("create session extract: %w", err)
		}
		return nil
	}
	res := tx.Model(&model.SessionExtract{}).
		Where("session_id = ? AND extracted_count = ?", p.SessionID, p.ExtractExpect).
		Update("extracted_count", p.ExtractTo)
	if res.Error != nil {
		return fmt.Errorf("update session extract: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrExtractConflict
	}
	return nil
}

// isDuplicate 判断是否为 MySQL 唯一键冲突（Error 1062）
func isDuplicate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Duplicate entry")
}
