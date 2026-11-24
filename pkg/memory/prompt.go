package memory

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// digestSystemPrompt 摘要生成指令，%d 为字数上限
const digestSystemPrompt = `你是对话摘要助手。请将对话历史整合为一份简洁的摘要，帮助后续对话延续上下文。

## 重点保留
- 用户的身份背景、偏好与明确提出的约束
- 已确认的结论、决策与关键事实、数据
- 尚未完成的任务或待解决的问题

## 可以丢弃
- 寒暄、客套等无信息量内容
- 已被推翻或过时的中间方案
- 重复内容

## 规则
1. 用简洁的自然语言概述，不使用复杂格式模板
2. 总长度控制在 %d 字以内
3. 如果提供了之前的摘要，将新内容整合进去，冲突时以新信息为准，去除过时信息

直接输出摘要正文，不要添加任何前缀或说明。`

// digestInjectPrompt 注入主对话的摘要前缀，%s 为摘要正文
const digestInjectPrompt = "[对话摘要]\n以下是之前对话的摘要，请基于这些背景信息继续对话：\n\n%s"

// buildSummarizeMessages 组装送给摘要模型的提示词
func buildSummarizeMessages(oldDigest string, turns []Turn, maxChars int) []*schema.Message {
	var b strings.Builder
	if oldDigest != "" {
		b.WriteString("## 之前的摘要\n")
		b.WriteString(oldDigest)
		b.WriteString("\n\n")
	}
	b.WriteString("## 新增对话\n")
	for _, t := range turns {
		if t.IsUser {
			b.WriteString("用户：")
		} else {
			b.WriteString("助手：")
		}
		b.WriteString(t.Content)
		b.WriteString("\n")
	}
	return []*schema.Message{
		schema.SystemMessage(fmt.Sprintf(digestSystemPrompt, maxChars)),
		schema.UserMessage(b.String()),
	}
}

// composeDigestPrompt 将摘要包装为单条 system message 的正文，无摘要返回空串
func composeDigestPrompt(digestText string) string {
	if digestText == "" {
		return ""
	}
	return fmt.Sprintf(digestInjectPrompt, digestText)
}

// userMemoryInjectPrompt 注入主对话的长期记忆前缀，%s 为条目列表
const userMemoryInjectPrompt = "[长期记忆·上位规则]\n以下是该用户在所有会话中都适用的长期约束、偏好与背景。你的每一次回答都必须遵守；若与后面的对话摘要或历史消息冲突，以此处为准：\n%s"

// extractSystemPrompt 长期记忆提取指令，%d 为单条字数上限
const extractSystemPrompt = `你是用户长期记忆管理助手。从新增对话中识别值得跨会话长期记住的用户信息，以增量操作指令更新记忆库。

## 只记录以下三类，且必须是用户本人明确表达的
- constraint（约束）：用户对回答方式的长期要求，如"以后回答都不要用 emoji"
- preference（偏好）：用户稳定的喜好或习惯，如"我喜欢先看结论再看细节"
- fact（事实）：用户的身份背景等长期事实，如"我是后端工程师，主要写 Go"

## 不要记录
- 只针对当前这一个问题的临时要求，如"这道题用英文回答"
- 当前任务的具体内容、知识问答本身
- 助手说的话，以及用户没有明确说过、只是推测出来的信息
- 记忆库中已存在的相同信息

## 操作规则
- add：新增一条记忆
- update：仅当用户明确更正某条已有记忆时使用，必须给出该条目的 id
- delete：仅当用户明确撤销某条已有记忆时使用（如"现在可以用 emoji 了"），必须给出该条目的 id
- 未被提及的已有记忆不要输出任何操作
- 每条 text 用一句话表述，不超过 %d 字
- 没有需要变更的内容时输出 {"ops":[]}

## 输出格式
只输出一个 JSON 对象，不要输出任何其他内容，例如：
{"ops":[{"op":"add","type":"constraint","text":"回答中不使用 emoji"},{"op":"delete","id":3}]}`

// extractTurnMaxChars 提取时单条消息的截断长度，控制提取调用成本
const extractTurnMaxChars = 1000

// buildExtractMessages 组装送给提取模型的提示词
func buildExtractMessages(existing []MemoryEntry, turns []Turn, entryMaxChars int) []*schema.Message {
	var b strings.Builder
	b.WriteString("## 现有记忆\n")
	if len(existing) == 0 {
		b.WriteString("（空）\n")
	}
	for _, e := range existing {
		fmt.Fprintf(&b, "[id=%d][%s] %s\n", e.ID, e.Type, e.Text)
	}
	b.WriteString("\n## 新增对话\n")
	for _, t := range turns {
		if t.IsUser {
			b.WriteString("用户：")
		} else {
			b.WriteString("助手：")
		}
		b.WriteString(truncateRunes(t.Content, extractTurnMaxChars))
		b.WriteString("\n")
	}
	return []*schema.Message{
		schema.SystemMessage(fmt.Sprintf(extractSystemPrompt, entryMaxChars)),
		schema.UserMessage(b.String()),
	}
}

// parseOps 解析提取模型输出，容忍代码块包裹和前后多余文本
func parseOps(raw string) ([]MemoryOp, error) {
	s := strings.TrimSpace(raw)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return nil, fmt.Errorf("memory: extract output is not json: %q", truncateRunes(s, 100))
	}
	var out struct {
		Ops []MemoryOp `json:"ops"`
	}
	if err := json.Unmarshal([]byte(s[i:j+1]), &out); err != nil {
		return nil, fmt.Errorf("memory: parse extract output: %w", err)
	}
	return out.Ops, nil
}
