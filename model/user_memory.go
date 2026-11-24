package model

import (
	"time"
)

// UserMemory 用户级长期记忆（L3），每个用户一条记录，跨会话共享
type UserMemory struct {
	ID       uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	UserName string `gorm:"uniqueIndex;not null;type:varchar(50)" json:"username"`
	// Entries 记忆条目列表的 JSON
	Entries string `gorm:"type:text" json:"entries"`
	// Version 乐观锁版本号，每次变更 +1
	Version   int       `gorm:"not null;default:0" json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 指定表名
func (UserMemory) TableName() string {
	return "t_user_memory"
}

// SessionExtract 会话的长期记忆提取进度，每个会话一条记录
type SessionExtract struct {
	ID        uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID string `gorm:"uniqueIndex;not null;type:varchar(36)" json:"session_id"`
	// ExtractedCount 提取水位线：该会话按 id 升序的前 N 条消息已做过长期记忆提取
	ExtractedCount int       `gorm:"not null;default:0" json:"extracted_count"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// TableName 指定表名
func (SessionExtract) TableName() string {
	return "t_session_extract"
}
