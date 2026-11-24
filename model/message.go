package model

import (
	"time"
)

type Message struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID string    `gorm:"index;not null;type:varchar(36)" json:"session_id"`
	UserName  string    `gorm:"type:varchar(20)" json:"username"`
	Content   string    `gorm:"type:text" json:"content"`
	IsUser    bool      `gorm:"not null;" json:"is_user"`
	CreatedAt time.Time `json:"created_at"`
}

type History struct {
	// ID 消息 ID，前端游标分页（加载更早消息）的锚点
	ID      int64  `json:"id"`
	IsUser  bool   `json:"is_user"`
	Content string `json:"content"`
}
