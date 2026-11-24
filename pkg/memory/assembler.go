package memory

import (
	"strings"

	"github.com/cloudwego/eino/schema"
)

// BuildContext 组装注入 LLM 的上下文：[system: 长期记忆上位规则 + 摘要] + 近期窗口，并按预算截断。
// 截断只裁窗口、不动长期记忆与摘要；最后一条（本轮用户问题）始终保留。
func (m *SessionMemory) BuildContext() []*schema.Message {
	rules := m.user.Render()

	m.mu.RLock()
	digestText := m.digest
	window := make([]Turn, len(m.window))
	copy(window, m.window)
	m.mu.RUnlock()

	return assemble(rules, digestText, window, m.cfg)
}

func assemble(rules, digestText string, window []Turn, cfg Config) []*schema.Message {
	if len(window) > cfg.WindowHardLimit {
		window = window[len(window)-cfg.WindowHardLimit:]
	}

	sys := joinNonEmpty("\n\n", rules, composeDigestPrompt(truncateRunes(digestText, cfg.DigestMaxChars)))
	window = fitBudget(window, cfg.ContextMaxChars-runeLen(sys))

	msgs := make([]*schema.Message, 0, len(window)+1)
	if sys != "" {
		msgs = append(msgs, schema.SystemMessage(sys))
	}
	for _, t := range window {
		if t.IsUser {
			msgs = append(msgs, schema.UserMessage(t.Content))
		} else {
			msgs = append(msgs, schema.AssistantMessage(t.Content, nil))
		}
	}
	return msgs
}

// fitBudget 从最新消息向前累加，超出预算后丢弃更旧的消息；
// 至少保留最后一条，并去掉开头的助手消息，保证以用户消息开头。
func fitBudget(window []Turn, budget int) []Turn {
	if len(window) == 0 {
		return window
	}
	start := len(window) - 1
	used := runeLen(window[start].Content)
	for i := start - 1; i >= 0; i-- {
		n := runeLen(window[i].Content)
		if used+n > budget {
			break
		}
		used += n
		start = i
	}
	for start < len(window)-1 && !window[start].IsUser {
		start++
	}
	return window[start:]
}

func runeLen(s string) int {
	return len([]rune(s))
}

func joinNonEmpty(sep string, parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s
	}
	return string(r[:max])
}
