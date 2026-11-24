package model

import (
	"time"
)

// SessionDigest 会话滚动摘要，每个会话只保留一条记录
type SessionDigest struct {
	ID        uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID string `gorm:"uniqueIndex;not null;type:varchar(36)" json:"session_id"`
	// Content 滚动整合后的摘要正文
	Content string `gorm:"type:text;not null" json:"content"`
	// DigestedCount 水位线：该会话按 id 升序的前 N 条消息已被摘要覆盖
	DigestedCount int `gorm:"not null;default:0" json:"digested_count"`
	// Version 乐观锁版本号，每次整合 +1
	Version   int       `gorm:"not null;default:0" json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 指定表名
func (SessionDigest) TableName() string {
	return "t_session_digest"
}
