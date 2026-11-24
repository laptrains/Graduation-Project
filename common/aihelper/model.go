package aihelper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/cloudwego/eino-ext/components/model/ollama"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/mark3labs/mcp-go/mcp"

	mcpclient "GopherAI/common/mcp/client"
)

type StreamCallback func(msg string)

// AIModel 定义AI模型接口
type AIModel interface {
	GenerateResponse(ctx context.Context, messages []*schema.Message) (*schema.Message, error)
	StreamResponse(ctx context.Context, messages []*schema.Message, cb StreamCallback) (string, error)
	GetModelType() string
}

// =================== OpenAI 实现 ===================
type OpenAIModel struct {
	llm model.ToolCallingChatModel
}

func NewOpenAIModel(ctx context.Context) (*OpenAIModel, error) {
	// TrimSpace 兜底：.env 里值跨行时 key 会带上换行，导致 HTTP header 非法
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	modelName := strings.TrimSpace(os.Getenv("OPENAI_MODEL_NAME"))
	baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	log.Printf("[model] openai model=%q baseURL=%q apiKeySet=%v", modelName, baseURL, key != "")

	llm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL: baseURL,
		Model:   modelName,
		APIKey:  key,
	})
	if err != nil {
		return nil, fmt.Errorf("create openai model failed: %v", err)
	}
	return &OpenAIModel{llm: llm}, nil
}

func (o *OpenAIModel) GenerateResponse(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
	resp, err := o.llm.Generate(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("openai generate failed: %v", err)
	}
	return resp, nil
}

func (o *OpenAIModel) StreamResponse(ctx context.Context, messages []*schema.Message, cb StreamCallback) (string, error) {
	stream, err := o.llm.Stream(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("openai stream failed: %v", err)
	}
	defer stream.Close()

	var fullResp strings.Builder

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("openai stream recv failed: %v", err)
		}
		if len(msg.Content) > 0 {
			fullResp.WriteString(msg.Content) // 聚合

			cb(msg.Content) // 实时调用cb函数，方便主动发送给前端
		}
	}

	return fullResp.String(), nil //返回完整内容，方便后续存储
}

func (o *OpenAIModel) GetModelType() string { return "1" }

// =================== Ollama 实现 ===================

// OllamaModel Ollama模型实现
type OllamaModel struct {
	llm model.ToolCallingChatModel
}

func NewOllamaModel(ctx context.Context, baseURL, modelName string) (*OllamaModel, error) {
	llm, err := ollama.NewChatModel(ctx, &ollama.ChatModelConfig{
		BaseURL: baseURL,
		Model:   modelName,
	})
	if err != nil {
		return nil, fmt.Errorf("create ollama model failed: %v", err)
	}
	return &OllamaModel{llm: llm}, nil
}

func (o *OllamaModel) GenerateResponse(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
	resp, err := o.llm.Generate(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("ollama generate failed: %v", err)
	}
	return resp, nil
}

func (o *OllamaModel) StreamResponse(ctx context.Context, messages []*schema.Message, cb StreamCallback) (string, error) {
	stream, err := o.llm.Stream(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("ollama stream failed: %v", err)
	}
	defer stream.Close()
	var fullResp strings.Builder
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("openai stream recv failed: %v", err)
		}
		if len(msg.Content) > 0 {
			fullResp.WriteString(msg.Content) // 聚合
			cb(msg.Content)                   // 实时调用cb函数，方便主动发送给前端
		}
	}
	return fullResp.String(), nil //返回完整内容，方便后续存储
}

func (o *OllamaModel) GetModelType() string { return "4" }

// =================== MCP 实现 ===================

const (
	defaultMCPBaseURL    = "http://localhost:8081/mcp"
	defaultMaxToolRounds = 5

	toolRoundLimitMessage = "工具调用次数已达到上限，请稍后重试或换一种说法。"
	toolSystemPrompt      = "你可以使用 MCP 工具完成用户的请求。凡是涉及设备开关、角度调节或状态变更的请求，必须先调用 control_device，并在收到工具成功结果后才能声称操作已完成；一次涉及多个设备时，对每个设备分别调用一次。查询设备当前状态（灯是否开着、风扇状态、窗帘角度等）时使用 get_device_state，不要根据对话历史或之前的控制结果推断当前状态。不要根据历史对话、工具描述或已有状态推断本次操作已成功。工具返回离线、超时或错误时，如实向用户说明原因，不要编造成功结果。"
)

// mcpToolClient 自动发现与调用所需的最小 MCP 客户端能力
type mcpToolClient interface {
	ListTools(ctx context.Context) ([]mcp.Tool, error)
	CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error)
	Close() error
}

// MCPModel MCP模型实现：工具由 tools/list 自动发现，通过原生 Tool Calling 驱动多轮调用
type MCPModel struct {
	llm           model.ToolCallingChatModel
	username      string
	newClient     func(context.Context) (mcpToolClient, error)
	maxToolRounds int

	mu         sync.Mutex
	client     mcpToolClient
	tools      []*schema.ToolInfo
	discovered bool
}

// NewMCPModel 创建MCP模型实例
func NewMCPModel(ctx context.Context, username string) (*MCPModel, error) {
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	modelName := strings.TrimSpace(os.Getenv("OPENAI_MODEL_NAME"))
	baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	log.Printf("[mcp] openai model=%q baseURL=%q apiKeySet=%v", modelName, baseURL, key != "")

	// 创建LLM
	llm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL: baseURL,
		Model:   modelName,
		APIKey:  key,
	})
	if err != nil {
		return nil, fmt.Errorf("create mcp model failed: %v", err)
	}

	mcpURL := strings.TrimSpace(os.Getenv("MCP_SERVER_URL"))
	if mcpURL == "" {
		mcpURL = defaultMCPBaseURL
	}
	log.Printf("[mcp] tool server=%s", mcpURL)

	return &MCPModel{
		llm:           llm,
		username:      username,
		newClient:     newHTTPMCPClient(mcpURL),
		maxToolRounds: defaultMaxToolRounds,
	}, nil
}

// newHTTPMCPClient 创建并初始化 Streamable HTTP 客户端
func newHTTPMCPClient(baseURL string) func(context.Context) (mcpToolClient, error) {
	return func(ctx context.Context) (mcpToolClient, error) {
		c, err := mcpclient.NewMCPClient(baseURL)
		if err != nil {
			return nil, fmt.Errorf("create mcp client: %w", err)
		}
		if _, err := c.Initialize(ctx); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("initialize mcp client: %w", err)
		}
		return c, nil
	}
}

// getClient 获取或创建MCP客户端
func (m *MCPModel) getClient(ctx context.Context) (mcpToolClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		return m.client, nil
	}
	if m.newClient == nil {
		return nil, fmt.Errorf("mcp client is not configured")
	}
	c, err := m.newClient(ctx)
	if err != nil {
		return nil, err
	}
	m.client = c
	return c, nil
}

// discoverTools 首次使用时通过 tools/list 自动发现工具，成功后缓存
func (m *MCPModel) discoverTools(ctx context.Context) ([]*schema.ToolInfo, error) {
	m.mu.Lock()
	if m.discovered {
		tools := m.tools
		m.mu.Unlock()
		return tools, nil
	}
	m.mu.Unlock()

	c, err := m.getClient(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := c.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list mcp tools: %w", err)
	}
	tools := make([]*schema.ToolInfo, 0, len(raw))
	for _, t := range raw {
		info, err := toolInfoFromMCP(t)
		if err != nil {
			return nil, err
		}
		tools = append(tools, info)
	}

	m.mu.Lock()
	m.tools = tools
	m.discovered = true
	m.mu.Unlock()
	return tools, nil
}

// bindTools 将发现的工具绑定到模型。
// 发现或绑定失败时返回错误而非静默降级为普通聊天：
// 设备控制模式下"降级继续聊"会导致未执行任何工具却对用户声称操作成功（幻觉）。
func (m *MCPModel) bindTools(ctx context.Context) (model.ToolCallingChatModel, error) {
	tools, err := m.discoverTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("MCP 工具服务不可用，设备控制模式无法工作：%w", err)
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("MCP 工具服务未暴露任何工具，设备控制模式无法工作")
	}
	bound, err := m.llm.WithTools(tools)
	if err != nil {
		return nil, fmt.Errorf("MCP 工具绑定失败，设备控制模式无法工作：%w", err)
	}
	return bound, nil
}

// toolInfoFromMCP 把 MCP 工具定义转换为 eino ToolInfo，保留枚举、范围与必填约束
func toolInfoFromMCP(t mcp.Tool) (*schema.ToolInfo, error) {
	info := &schema.ToolInfo{Name: t.Name, Desc: t.Description}

	raw := t.RawInputSchema
	if len(raw) == 0 {
		if len(t.InputSchema.Properties) == 0 {
			return info, nil
		}
		var err error
		raw, err = json.Marshal(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("marshal tool %s schema: %w", t.Name, err)
		}
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse tool %s schema: %w", t.Name, err)
	}
	if s.Properties != nil && s.Properties.Len() > 0 {
		info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&s)
	}
	return info, nil
}

// GenerateResponse 生成响应：原生 Tool Calling，支持多轮工具调用
func (m *MCPModel) GenerateResponse(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages provided")
	}

	llm, err := m.bindTools(ctx)
	if err != nil {
		return nil, err
	}
	msgs := withToolSystemPrompt(append([]*schema.Message(nil), messages...))

	for round := 0; ; round++ {
		resp, err := llm.Generate(ctx, msgs)
		if err != nil {
			return nil, fmt.Errorf("mcp generate failed: %w", err)
		}
		if len(resp.ToolCalls) == 0 {
			return resp, nil
		}
		if round >= m.roundLimit() {
			return schema.AssistantMessage(toolRoundLimitMessage, nil), nil
		}
		msgs = append(msgs, resp)
		msgs = append(msgs, m.executeToolCalls(ctx, resp.ToolCalls)...)
	}
}

// StreamResponse 流式响应：每轮先流式生成，若模型要求调用工具则执行后继续下一轮
func (m *MCPModel) StreamResponse(ctx context.Context, messages []*schema.Message, cb StreamCallback) (string, error) {
	if len(messages) == 0 {
		return "", fmt.Errorf("no messages provided")
	}

	llm, err := m.bindTools(ctx)
	if err != nil {
		return "", err
	}
	msgs := withToolSystemPrompt(append([]*schema.Message(nil), messages...))

	for round := 0; ; round++ {
		stream, err := llm.Stream(ctx, msgs)
		if err != nil {
			return "", fmt.Errorf("mcp stream failed: %w", err)
		}

		chunks := make([]*schema.Message, 0, 8)
		var buf strings.Builder
		for {
			chunk, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				stream.Close()
				return buf.String(), fmt.Errorf("mcp stream recv failed: %w", err)
			}
			chunks = append(chunks, chunk)
			if chunk.Content != "" {
				buf.WriteString(chunk.Content)
				if cb != nil {
					cb(chunk.Content)
				}
			}
		}
		stream.Close()

		var merged *schema.Message
		if len(chunks) > 0 {
			merged, err = schema.ConcatMessages(chunks)
			if err != nil {
				return buf.String(), fmt.Errorf("merge mcp stream: %w", err)
			}
		}
		if merged == nil || len(merged.ToolCalls) == 0 {
			return buf.String(), nil
		}
		if round >= m.roundLimit() {
			if cb != nil {
				cb(toolRoundLimitMessage)
			}
			return buf.String() + toolRoundLimitMessage, nil
		}
		msgs = append(msgs, merged)
		msgs = append(msgs, m.executeToolCalls(ctx, merged.ToolCalls)...)
	}
}

// roundLimit 最大工具调用轮数，超过后停止并给出提示
func (m *MCPModel) roundLimit() int {
	if m.maxToolRounds > 0 {
		return m.maxToolRounds
	}
	return defaultMaxToolRounds
}

// withToolSystemPrompt 在保留原有系统提示（记忆规则/摘要）的基础上追加工具使用说明
func withToolSystemPrompt(msgs []*schema.Message) []*schema.Message {
	out := make([]*schema.Message, len(msgs), len(msgs)+1)
	copy(out, msgs)
	if len(out) > 0 && out[0].Role == schema.System {
		first := *out[0]
		if strings.TrimSpace(first.Content) == "" {
			first.Content = toolSystemPrompt
		} else {
			first.Content += "\n\n" + toolSystemPrompt
		}
		out[0] = &first
		return out
	}
	return append([]*schema.Message{schema.SystemMessage(toolSystemPrompt)}, out...)
}

// executeToolCalls 依次执行模型发起的工具调用，并把结果包装成 Tool 消息回灌给模型
func (m *MCPModel) executeToolCalls(ctx context.Context, calls []schema.ToolCall) []*schema.Message {
	out := make([]*schema.Message, 0, len(calls))
	c, err := m.getClient(ctx)
	for _, call := range calls {
		var text string
		switch {
		case err != nil:
			text = "工具调用失败：" + err.Error()
		default:
			args, perr := parseToolArguments(call.Function.Arguments)
			if perr != nil {
				text = "参数解析失败：" + perr.Error()
				break
			}
			res, cerr := c.CallTool(ctx, call.Function.Name, args)
			if cerr != nil {
				text = "工具调用失败：" + cerr.Error()
				break
			}
			text = mcpclient.ResultText(res)
			if res != nil && res.IsError {
				text = "工具返回错误：" + text
			}
			if text == "" {
				text = "工具已执行，无返回内容"
			}
		}
		out = append(out, schema.ToolMessage(text, call.ID, schema.WithToolName(call.Function.Name)))
	}
	return out
}

// parseToolArguments 解析模型给出的 JSON 参数；空字符串视为无参数
func parseToolArguments(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

// GetModelType 获取模型类型
func (m *MCPModel) GetModelType() string { return "3" }

// Close 关闭MCP客户端
func (m *MCPModel) Close() {
	m.mu.Lock()
	c := m.client
	m.client = nil
	m.tools = nil
	m.discovered = false
	m.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}
