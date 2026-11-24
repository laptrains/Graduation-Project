// Package digest 会话摘要的数据访问层
package digest

import (
	"GopherAI/common/mysql"
	"GopherAI/model"
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ErrVersionConflict 乐观锁冲突：摘要已被其他任务更新
var ErrVersionConflict = errors.New("session digest version conflict")

// GetBySessionID 查询会话摘要，不存在时返回 nil, nil
func GetBySessionID(ctx context.Context, sessionID string) (*model.SessionDigest, error) {
	var d model.SessionDigest
	err := mysql.DB.WithContext(ctx).Where("session_id = ?", sessionID).First(&d).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session digest: %w", err)
	}
	return &d, nil
}

// Save 以乐观锁方式写入摘要。expectVersion 为写入前的版本号，0 表示首次创建。
// d.Version 应为 expectVersion+1。
func Save(ctx context.Context, d *model.SessionDigest, expectVersion int) error {
	db := mysql.DB.WithContext(ctx)
	if expectVersion == 0 {
		if err := db.Create(d).Error; err != nil {
			return fmt.Errorf("create session digest: %w", err)
		}
		return nil
	}

	res := db.Model(&model.SessionDigest{}).
		Where("session_id = ? AND version = ?", d.SessionID, expectVersion).
		Updates(map[string]interface{}{
			"content":        d.Content,
			"digested_count": d.DigestedCount,
			"version":        d.Version,
		})
	if res.Error != nil {
		return fmt.Errorf("update session digest: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrVersionConflict
	}
	return nil
}
