package session

import (
	"GopherAI/common/mysql"
	"GopherAI/model"
	"context"
	"errors"

	"gorm.io/gorm"
)

// FindSessionByID 查询会话，不存在时返回 nil, nil
func FindSessionByID(ctx context.Context, sessionID string) (*model.Session, error) {
	var s model.Session
	err := mysql.DB.WithContext(ctx).Where("id = ?", sessionID).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func GetSessionsByUserName(UserName string) ([]model.Session, error) {
	var sessions []model.Session
	err := mysql.DB.Where("user_name = ?", UserName).Find(&sessions).Error
	return sessions, err
}

func CreateSession(session *model.Session) (*model.Session, error) {
	err := mysql.DB.Create(session).Error
	return session, err
}

func GetSessionByID(sessionID string) (*model.Session, error) {
	var session model.Session
	err := mysql.DB.Where("id = ?", sessionID).First(&session).Error
	return &session, err
}
