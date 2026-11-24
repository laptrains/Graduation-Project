package aihelper

import (
	"GopherAI/config"
	"GopherAI/pkg/memory"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// DeepSeek 兼容 OpenAI 协议，以下为环境变量缺省值
const (
	defaultDeepSeekBaseURL = "https://api.deepseek.com"
	defaultDeepSeekModel   = "deepseek-chat"
)

// deepSeekSummarizer 使用 DeepSeek 生成会话摘要，实现 memory.Summarizer
type deepSeekSummarizer struct {
	llm einomodel.ToolCallingChatModel
}

// Summarize 调用 DeepSeek 生成摘要正文
func (d *deepSeekSummarizer) Summarize(ctx context.Context, msgs []*schema.Message) (string, error) {
	resp, err := d.llm.Generate(ctx, msgs)
	if err != nil {
		return "", fmt.Errorf("deepseek summarize: %w", err)
	}
	return resp.Content, nil
}

// Extract 调用 DeepSeek 提取长期记忆操作指令，实现 memory.Extractor
func (d *deepSeekSummarizer) Extract(ctx context.Context, msgs []*schema.Message) (string, error) {
	resp, err := d.llm.Generate(ctx, msgs)
	if err != nil {
		return "", fmt.Errorf("deepseek extract: %w", err)
	}
	return resp.Content, nil
}

// getExtractor 复用 DeepSeek 客户端做长期记忆提取；未配置 DEEPSEEK_API_KEY 时返回 nil
func getExtractor() memory.Extractor {
	if e, ok := getSummarizer().(memory.Extractor); ok {
		return e
	}
	return nil
}

var (
	summarizer     memory.Summarizer
	summarizerOnce sync.Once
)

// getSummarizer 懒加载全局摘要器；未配置 DEEPSEEK_API_KEY 时返回 nil，此时不做摘要，仅靠窗口上限兜底
func getSummarizer() memory.Summarizer {
	summarizerOnce.Do(func() {
		key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
		if key == "" {
			log.Println("[memory] DEEPSEEK_API_KEY not set, session digest disabled")
			return
		}
		baseURL := envOrDefault("DEEPSEEK_BASE_URL", defaultDeepSeekBaseURL)
		modelName := envOrDefault("DEEPSEEK_MODEL_NAME", defaultDeepSeekModel)
		llm, err := openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
			BaseURL: baseURL,
			Model:   modelName,
			APIKey:  key,
		})
		if err != nil {
			log.Printf("[memory] create deepseek summarizer failed, session digest disabled: %v", err)
			return
		}
		summarizer = &deepSeekSummarizer{llm: llm}
		log.Printf("[memory] deepseek summarizer ready, model=%s", modelName)
	})
	return summarizer
}

var (
	memoryCfg     memory.Config
	memoryCfgOnce sync.Once
)

// getMemoryConfig 从全局配置读取记忆参数
func getMemoryConfig() memory.Config {
	memoryCfgOnce.Do(func() {
		c := config.GetConfig().MemoryConfig
		memoryCfg = memory.Config{
			DigestThreshold:  c.DigestThreshold,
			KeepRecent:       c.KeepRecent,
			WindowHardLimit:  c.WindowHardLimit,
			DigestMaxChars:   c.DigestMaxChars,
			ContextMaxChars:  c.ContextMaxChars,
			SummarizeTimeout: time.Duration(c.SummarizeTimeoutS) * time.Second,

			ExtractThreshold:        c.ExtractThreshold,
			ExtractBatch:            c.ExtractBatch,
			UserMemoryMaxEntries:    c.UserMemoryMaxEntries,
			UserMemoryEntryMaxChars: c.UserMemoryEntryMaxChars,
			UserMemoryMaxChars:      c.UserMemoryMaxChars,
		}.Normalize()
		log.Printf("[memory] config: digestThreshold=%d keepRecent=%d windowHardLimit=%d digestMaxChars=%d contextMaxChars=%d",
			memoryCfg.DigestThreshold, memoryCfg.KeepRecent, memoryCfg.WindowHardLimit,
			memoryCfg.DigestMaxChars, memoryCfg.ContextMaxChars)
		log.Printf("[memory] user memory: enabled=%v extractThreshold=%d extractBatch=%d maxEntries=%d entryMaxChars=%d maxChars=%d",
			c.UserMemoryEnabled, memoryCfg.ExtractThreshold, memoryCfg.ExtractBatch,
			memoryCfg.UserMemoryMaxEntries, memoryCfg.UserMemoryEntryMaxChars, memoryCfg.UserMemoryMaxChars)
	})
	return memoryCfg
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
