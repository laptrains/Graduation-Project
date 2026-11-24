package config

import (
	"sync"

	"github.com/BurntSushi/toml"
)

type MainConfig struct {
	Port    int    `toml:"port"`
	AppName string `toml:"appName"`
	Host    string `toml:"host"`
}

type EmailConfig struct {
	Authcode string `toml:"authcode"`
	Email    string `toml:"email"`
}

type RedisConfig struct {
	RedisPort     int    `toml:"port"`
	RedisDb       int    `toml:"db"`
	RedisHost     string `toml:"host"`
	RedisPassword string `toml:"password"`
}

type MysqlConfig struct {
	MysqlPort         int    `toml:"port"`
	MysqlHost         string `toml:"host"`
	MysqlUser         string `toml:"user"`
	MysqlPassword     string `toml:"password"`
	MysqlDatabaseName string `toml:"databaseName"`
	MysqlCharset      string `toml:"charset"`
}

type JwtConfig struct {
	ExpireDuration int    `toml:"expire_duration"`
	Issuer         string `toml:"issuer"`
	Subject        string `toml:"subject"`
	Key            string `toml:"key"`
}

// MemoryConfig 会话记忆参数，未配置的项由 pkg/memory 使用缺省值
type MemoryConfig struct {
	DigestThreshold   int `toml:"digestThreshold"`
	KeepRecent        int `toml:"keepRecent"`
	WindowHardLimit   int `toml:"windowHardLimit"`
	DigestMaxChars    int `toml:"digestMaxChars"`
	ContextMaxChars   int `toml:"contextMaxChars"`
	SummarizeTimeoutS int `toml:"summarizeTimeoutS"`

	// 用户长期记忆（L3）
	UserMemoryEnabled       bool `toml:"userMemoryEnabled"`
	ExtractThreshold        int  `toml:"extractThreshold"`
	ExtractBatch            int  `toml:"extractBatch"`
	UserMemoryMaxEntries    int  `toml:"userMemoryMaxEntries"`
	UserMemoryEntryMaxChars int  `toml:"userMemoryEntryMaxChars"`
	UserMemoryMaxChars      int  `toml:"userMemoryMaxChars"`
}

type Config struct {
	EmailConfig  `toml:"emailConfig"`
	RedisConfig  `toml:"redisConfig"`
	MysqlConfig  `toml:"mysqlConfig"`
	JwtConfig    `toml:"jwtConfig"`
	MainConfig   `toml:"mainConfig"`
	MemoryConfig `toml:"memoryConfig"`
}

type RedisKeyConfig struct {
	CaptchaPrefix string
}

var DefaultRedisKeyConfig = RedisKeyConfig{
	CaptchaPrefix: "captcha:%s",
}

var config *Config
var once sync.Once

// InitConfig 初始化项目配置
func InitConfig() error {
	// 设置配置文件路径（相对于 main.go 所在的目录）
	if _, err := toml.DecodeFile("config/config.toml", config); err != nil {

		return err
	}
	return nil
}

func GetConfig() *Config {
	once.Do(
		func() {
			config = new(Config)
			_ = InitConfig()
		})
	return config
}
