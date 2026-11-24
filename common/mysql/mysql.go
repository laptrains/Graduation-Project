package mysql

import (
	"GopherAI/config"
	"GopherAI/model"
	"fmt"
	stdlog "log"
	"os"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

var DB *gorm.DB

func InitMysql() error {
	host := config.GetConfig().MysqlHost
	port := config.GetConfig().MysqlPort
	dbname := config.GetConfig().MysqlDatabaseName
	username := config.GetConfig().MysqlUser
	password := config.GetConfig().MysqlPassword
	charset := config.GetConfig().MysqlCharset

	//dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=%s&parseTime=true&loc=Local", username, password, host, port, dbname, charset)
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=true&loc=Local", username, password, host, port, dbname, charset)

	// SQL 日志默认静默，只记录慢查询（>200ms）与错误，避免每条 SQL 都刷屏。
	// ParameterizedQueries 让参数以 ? 占位输出，避免摘要正文等长文本被整段打进日志
	dbLogger := gormlog.New(
		stdlog.New(os.Stdout, "[gorm] ", stdlog.LstdFlags),
		gormlog.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlog.Warn,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
		},
	)

	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       dsn,
		DefaultStringSize:         256,
		DisableDatetimePrecision:  true,
		DontSupportRenameIndex:    true,
		DontSupportRenameColumn:   true,
		SkipInitializeWithVersion: false,
	}), &gorm.Config{
		Logger: dbLogger,
	})
	if err != nil {
		return err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	DB = db

	return migration()
}

func migration() error {
	return DB.AutoMigrate(
		new(model.Session),
		new(model.Message),
		new(model.SessionDigest), //摘要表
		new(model.UserMemory),     //用户长期记忆
		new(model.SessionExtract), //会话长期记忆提取进度
	)
}
