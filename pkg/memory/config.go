// Package memory 会话记忆领域能力：近期窗口(L1) + 滚动摘要(L2) + 用户长期记忆(L3) + 上下文预算。
//
// 核心思想：数据库保存全量消息，注入 LLM 的只有「摘要 + 近期窗口」，
// 上下文大小有上界，与对话轮数解耦。
package memory

import (
	"time"
)

// 生产环境缺省值，配置缺失时兜底
const (
	defaultDigestThreshold  = 40
	defaultKeepRecent       = 16
	defaultWindowHardLimit  = 80
	defaultDigestMaxChars   = 800
	defaultContextMaxChars  = 24000
	defaultSummarizeTimeout = 60 * time.Second

	defaultExtractThreshold        = 6
	defaultExtractBatch            = 20
	defaultUserMemoryMaxEntries    = 30
	defaultUserMemoryEntryMaxChars = 100
	defaultUserMemoryMaxChars      = 1000
)

// Config 记忆参数
type Config struct {
	// DigestThreshold 窗口消息数超过该值触发压缩
	DigestThreshold int
	// KeepRecent 压缩后保留的原文消息数
	KeepRecent int
	// WindowHardLimit 摘要失败时注入/加载的最大消息数兜底
	WindowHardLimit int
	// DigestMaxChars 摘要最大字符数（rune）
	DigestMaxChars int
	// ContextMaxChars 单次注入上下文总字符上限（rune）
	ContextMaxChars int
	// SummarizeTimeout 单次摘要调用超时
	SummarizeTimeout time.Duration

	// ExtractThreshold 未提取消息数达到该值触发长期记忆提取
	ExtractThreshold int
	// ExtractBatch 单次提取最多处理的消息数
	ExtractBatch int
	// UserMemoryMaxEntries 长期记忆最大条目数
	UserMemoryMaxEntries int
	// UserMemoryEntryMaxChars 单条记忆最大字符数（rune）
	UserMemoryEntryMaxChars int
	// UserMemoryMaxChars 长期记忆总字符上限（rune）
	UserMemoryMaxChars int
}

// Normalize 补齐缺省值并修正互相矛盾的参数
func (c Config) Normalize() Config {
	if c.DigestThreshold <= 0 {
		c.DigestThreshold = defaultDigestThreshold
	}
	if c.KeepRecent <= 0 {
		c.KeepRecent = defaultKeepRecent
	}
	if c.KeepRecent >= c.DigestThreshold {
		c.KeepRecent = c.DigestThreshold / 2
	}
	if c.WindowHardLimit <= 0 {
		c.WindowHardLimit = defaultWindowHardLimit
	}
	if c.WindowHardLimit < c.DigestThreshold {
		c.WindowHardLimit = c.DigestThreshold * 2
	}
	if c.DigestMaxChars <= 0 {
		c.DigestMaxChars = defaultDigestMaxChars
	}
	if c.ContextMaxChars <= 0 {
		c.ContextMaxChars = defaultContextMaxChars
	}
	if c.SummarizeTimeout <= 0 {
		c.SummarizeTimeout = defaultSummarizeTimeout
	}
	if c.ExtractThreshold <= 0 {
		c.ExtractThreshold = defaultExtractThreshold
	}
	if c.ExtractBatch <= 0 {
		c.ExtractBatch = defaultExtractBatch
	}
	if c.ExtractBatch < c.ExtractThreshold {
		c.ExtractBatch = c.ExtractThreshold
	}
	if c.UserMemoryMaxEntries <= 0 {
		c.UserMemoryMaxEntries = defaultUserMemoryMaxEntries
	}
	if c.UserMemoryEntryMaxChars <= 0 {
		c.UserMemoryEntryMaxChars = defaultUserMemoryEntryMaxChars
	}
	if c.UserMemoryMaxChars <= 0 {
		c.UserMemoryMaxChars = defaultUserMemoryMaxChars
	}
	return c
}
