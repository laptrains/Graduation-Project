package message

import (
	"GopherAI/common/mysql"
	"GopherAI/model"
	"context"
)

// CountBySessionID 统计会话的消息总数
func CountBySessionID(ctx context.Context, sessionID string) (int64, error) {
	var total int64
	err := mysql.DB.WithContext(ctx).Model(&model.Message{}).Where("session_id = ?", sessionID).Count(&total).Error
	return total, err
}

// ListBySessionIDRange 按 id 升序，从第 offset 条开始取最多 limit 条消息
func ListBySessionIDRange(ctx context.Context, sessionID string, offset, limit int) ([]model.Message, error) {
	var msgs []model.Message
	err := mysql.DB.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("id asc").
		Offset(offset).
		Limit(limit).
		Find(&msgs).Error
	return msgs, err
}

func GetMessagesBySessionID(sessionID string) ([]model.Message, error) {
	var msgs []model.Message
	err := mysql.DB.Where("session_id = ?", sessionID).Order("created_at asc").Find(&msgs).Error
	return msgs, err
}

func GetMessagesBySessionIDs(sessionIDs []string) ([]model.Message, error) {
	var msgs []model.Message
	if len(sessionIDs) == 0 {
		return msgs, nil
	}
	err := mysql.DB.Where("session_id IN ?", sessionIDs).Order("created_at asc").Find(&msgs).Error
	return msgs, err
}

func CreateMessage(message *model.Message) (*model.Message, error) {
	err := mysql.DB.Create(message).Error
	return message, err
}

// BatchCreate 批量插入消息，合并多次 INSERT 为一次，降低 DB 连接往返。
func BatchCreate(messages []*model.Message) error {
	if len(messages) == 0 {
		return nil
	}
	//以len(messages)为批次大小插入
	return mysql.DB.CreateInBatches(messages, len(messages)).Error
}

func GetAllMessages() ([]model.Message, error) {
	var msgs []model.Message
	err := mysql.DB.Order("created_at asc").Find(&msgs).Error
	return msgs, err
}
