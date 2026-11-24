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

// ListBySessionIDBefore 返回 id 小于 beforeID 的最多 limit 条消息（按 id 升序输出）。
// 游标分页：以消息 id 为锚点，翻页期间会话追加新消息不会导致页间重复或漏读
func ListBySessionIDBefore(ctx context.Context, sessionID string, beforeID int64, limit int) ([]model.Message, error) {
	var msgs []model.Message
	err := mysql.DB.WithContext(ctx).
		Where("session_id = ? AND id < ?", sessionID, beforeID).
		Order("id desc").
		Limit(limit).
		Find(&msgs).Error
	if err != nil {
		return nil, err
	}
	// 倒序取出后反转为升序，与 page 分页的返回顺序保持一致
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
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
