package aihelper

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	mcpgo "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	mcpclient "GopherAI/common/mcp/client"
	mcpserver "GopherAI/common/mcp/server"
	"GopherAI/pkg/device"
)

// fakeToolLLM 模拟支持原生 Tool Calling 的模型。
// WithTools 仅记录绑定的工具并返回自身，便于测试断言"自动发现"结果。
type fakeToolLLM struct {
	round  int
	bound  []*schema.ToolInfo
	seen   [][]*schema.Message
	gen    func(round int, msgs []*schema.Message) (*schema.Message, error)
	stream func(round int, msgs []*schema.Message) []*schema.Message
}

func (f *fakeToolLLM) Generate(ctx context.Context, in []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	f.seen = append(f.seen, in)
	r := f.round
	f.round++
	if f.gen == nil {
		return schema.AssistantMessage("ok", nil), nil
	}
	return f.gen(r, in)
}

func (f *fakeToolLLM) Stream(ctx context.Context, in []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	f.seen = append(f.seen, in)
	r := f.round
	f.round++
	return schema.StreamReaderFromArray(f.stream(r, in)), nil
}

func (f *fakeToolLLM) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	f.bound = tools
	return f, nil
}

type fakeToolCall struct {
	name string
	args map[string]any
}

type fakeMCPToolClient struct {
	tools     []mcp.Tool
	listErr   error
	listCalls int
	calls     []fakeToolCall
	callFn    func(call int, name string, args map[string]any) (*mcp.CallToolResult, error)
	closed    bool
}

func (f *fakeMCPToolClient) ListTools(context.Context) ([]mcp.Tool, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.tools, nil
}

func (f *fakeMCPToolClient) CallTool(_ context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	f.calls = append(f.calls, fakeToolCall{name: name, args: args})
	if f.callFn != nil {
		return f.callFn(len(f.calls)-1, name, args)
	}
	return mcp.NewToolResultText("ok"), nil
}

func (f *fakeMCPToolClient) Close() error {
	f.closed = true
	return nil
}

func testMCPTools() []mcp.Tool {
	return []mcp.Tool{
		{
			Name:        "list_devices",
			Description: "列出可控设备",
			InputSchema: mcp.ToolInputSchema{Type: "object"},
		},
		{
			Name:        "control_device",
			Description: "控制设备。设备别名：电灯、电扇、帘子",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"device_id": map[string]any{"type": "string", "enum": []any{"relay_light", "relay_fan", "servo_curtain"}},
					"action":    map[string]any{"type": "string", "enum": []any{"on", "off", "set_angle"}},
					"angle":     map[string]any{"type": "integer", "minimum": 0, "maximum": 180},
				},
				Required: []string{"device_id", "action"},
			},
		},
	}
}

func toolCallMessage(id, name, args string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: args,
		},
	}})
}

func newTestMCPModel(llm *fakeToolLLM, c *fakeMCPToolClient) *MCPModel {
	return &MCPModel{
		llm:           llm,
		maxToolRounds: 5,
		newClient: func(context.Context) (mcpToolClient, error) {
			return c, nil
		},
	}
}

func findToolMessage(msgs []*schema.Message, id string) *schema.Message {
	for _, m := range msgs {
		if m.Role == schema.Tool && m.ToolCallID == id {
			return m
		}
	}
	return nil
}

func TestMCPModelGenerateDiscoversToolsAndCalls(t *testing.T) {
	llm := &fakeToolLLM{}
	llm.gen = func(round int, _ []*schema.Message) (*schema.Message, error) {
		if round == 0 {
			return toolCallMessage("call_1", "list_devices", `{}`), nil
		}
		return schema.AssistantMessage("共有三台设备", nil), nil
	}
	c := &fakeMCPToolClient{
		tools: testMCPTools(),
		callFn: func(int, string, map[string]any) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("设备清单"), nil
		},
	}
	m := newTestMCPModel(llm, c)

	got, err := m.GenerateResponse(context.Background(), []*schema.Message{schema.UserMessage("有哪些设备")})
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if got.Content != "共有三台设备" {
		t.Fatalf("unexpected content: %q", got.Content)
	}
	if c.listCalls != 1 {
		t.Fatalf("tools/list should be called once, got %d", c.listCalls)
	}
	if len(llm.bound) != 2 || llm.bound[0].Name != "list_devices" || llm.bound[1].Name != "control_device" {
		t.Fatalf("unexpected bound tools: %+v", llm.bound)
	}
	if len(llm.seen) != 2 {
		t.Fatalf("want 2 LLM calls, got %d", len(llm.seen))
	}
	if len(c.calls) != 1 || c.calls[0].name != "list_devices" {
		t.Fatalf("unexpected tool calls: %+v", c.calls)
	}
	second := llm.seen[1]
	if second[0].Role != schema.System || !strings.Contains(second[0].Content, "工具") {
		t.Fatalf("tool system prompt missing: %+v", second[0])
	}
	if tm := findToolMessage(second, "call_1"); tm == nil || tm.Content != "设备清单" {
		t.Fatalf("tool result not fed back: %+v", second)
	}
}

func TestMCPModelGenerateMultiRoundToolCalls(t *testing.T) {
	llm := &fakeToolLLM{}
	llm.gen = func(round int, _ []*schema.Message) (*schema.Message, error) {
		switch round {
		case 0:
			return toolCallMessage("call_1", "list_devices", `{}`), nil
		case 1:
			return toolCallMessage("call_2", "control_device", `{"device_id":"relay_light","action":"on"}`), nil
		default:
			return schema.AssistantMessage("灯已打开", nil), nil
		}
	}
	c := &fakeMCPToolClient{
		tools: testMCPTools(),
		callFn: func(_ int, name string, _ map[string]any) (*mcp.CallToolResult, error) {
			if name == "control_device" {
				return mcp.NewToolResultText("已执行：灯 on"), nil
			}
			return mcp.NewToolResultText("设备列表"), nil
		},
	}
	m := newTestMCPModel(llm, c)

	got, err := m.GenerateResponse(context.Background(), []*schema.Message{schema.UserMessage("开灯")})
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if got.Content != "灯已打开" {
		t.Fatalf("unexpected content: %q", got.Content)
	}
	if len(c.calls) != 2 || c.calls[0].name != "list_devices" || c.calls[1].name != "control_device" {
		t.Fatalf("unexpected call order: %+v", c.calls)
	}
	if got := c.calls[1].args["device_id"]; got != "relay_light" {
		t.Fatalf("arguments not passed through: %+v", c.calls[1].args)
	}
	if len(llm.seen) != 3 {
		t.Fatalf("want 3 LLM calls, got %d", len(llm.seen))
	}
	if tm := findToolMessage(llm.seen[2], "call_2"); tm == nil || !strings.Contains(tm.Content, "已执行") {
		t.Fatalf("second tool result not fed back: %+v", tm)
	}
}

func TestMCPModelGenerateToolErrorIsFedBack(t *testing.T) {
	llm := &fakeToolLLM{}
	llm.gen = func(round int, _ []*schema.Message) (*schema.Message, error) {
		if round == 0 {
			return toolCallMessage("call_1", "control_device", `{"device_id":"relay_light","action":"on"}`), nil
		}
		return schema.AssistantMessage("设备当前离线，无法开灯", nil), nil
	}
	c := &fakeMCPToolClient{
		tools: testMCPTools(),
		callFn: func(int, string, map[string]any) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultError("设备当前离线"), nil
		},
	}
	m := newTestMCPModel(llm, c)

	got, err := m.GenerateResponse(context.Background(), []*schema.Message{schema.UserMessage("开灯")})
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if !strings.Contains(got.Content, "离线") {
		t.Fatalf("unexpected content: %q", got.Content)
	}
	tm := findToolMessage(llm.seen[1], "call_1")
	if tm == nil || !strings.Contains(tm.Content, "设备当前离线") {
		t.Fatalf("tool error not fed back: %+v", tm)
	}
}

func TestMCPModelStreamFinalAnswer(t *testing.T) {
	llm := &fakeToolLLM{}
	llm.stream = func(_ int, _ []*schema.Message) []*schema.Message {
		return []*schema.Message{
			schema.AssistantMessage("灯", nil),
			schema.AssistantMessage("已打开", nil),
		}
	}
	c := &fakeMCPToolClient{tools: testMCPTools()}
	m := newTestMCPModel(llm, c)

	var chunks []string
	got, err := m.StreamResponse(context.Background(), []*schema.Message{schema.UserMessage("开灯")}, func(s string) {
		chunks = append(chunks, s)
	})
	if err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	if got != "灯已打开" || strings.Join(chunks, "") != "灯已打开" {
		t.Fatalf("unexpected stream result: content=%q chunks=%v", got, chunks)
	}
	if len(c.calls) != 0 {
		t.Fatalf("no tool should be called: %+v", c.calls)
	}
	if len(llm.bound) != 2 {
		t.Fatalf("tools not bound: %+v", llm.bound)
	}
}

func TestMCPModelStreamCallsToolThenStreamsFinal(t *testing.T) {
	llm := &fakeToolLLM{}
	llm.stream = func(round int, _ []*schema.Message) []*schema.Message {
		if round == 0 {
			return []*schema.Message{toolCallMessage("call_1", "control_device", `{"device_id":"relay_light","action":"on"}`)}
		}
		return []*schema.Message{
			schema.AssistantMessage("灯", nil),
			schema.AssistantMessage("已打开", nil),
		}
	}
	c := &fakeMCPToolClient{
		tools: testMCPTools(),
		callFn: func(int, string, map[string]any) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("已执行：灯 on"), nil
		},
	}
	m := newTestMCPModel(llm, c)

	var chunks []string
	got, err := m.StreamResponse(context.Background(), []*schema.Message{schema.UserMessage("开灯")}, func(s string) {
		chunks = append(chunks, s)
	})
	if err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	if got != "灯已打开" || strings.Join(chunks, "") != "灯已打开" {
		t.Fatalf("unexpected stream result: content=%q chunks=%v", got, chunks)
	}
	if len(c.calls) != 1 || c.calls[0].name != "control_device" {
		t.Fatalf("unexpected tool calls: %+v", c.calls)
	}
	if tm := findToolMessage(llm.seen[1], "call_1"); tm == nil || !strings.Contains(tm.Content, "已执行") {
		t.Fatalf("tool result not fed back: %+v", tm)
	}
}

// #1 工具发现失败必须显式报错：不允许静默降级为普通聊天，否则设备控制模式会"未执行却回答成功"
func TestMCPModelDiscoveryFailureReturnsError(t *testing.T) {
	llm := &fakeToolLLM{}
	llm.gen = func(_ int, _ []*schema.Message) (*schema.Message, error) {
		return schema.AssistantMessage("直接回答", nil), nil
	}
	c := &fakeMCPToolClient{listErr: errors.New("connection refused")}
	m := newTestMCPModel(llm, c)

	_, err := m.GenerateResponse(context.Background(), []*schema.Message{schema.UserMessage("开灯")})
	if err == nil {
		t.Fatal("GenerateResponse should fail loudly when tool discovery fails, got nil")
	}
	if !strings.Contains(err.Error(), "MCP") {
		t.Fatalf("error should mention MCP, got %q", err.Error())
	}
	if len(llm.seen) != 0 {
		t.Fatalf("LLM must not be called when discovery fails, got %d calls", len(llm.seen))
	}
	if len(llm.bound) != 0 {
		t.Fatalf("tools should not be bound when discovery fails: %+v", llm.bound)
	}
	if len(c.calls) != 0 {
		t.Fatalf("no tool call expected: %+v", c.calls)
	}
}

// #1 流式路径同样不允许静默降级
func TestMCPModelStreamDiscoveryFailureReturnsError(t *testing.T) {
	llm := &fakeToolLLM{}
	llm.stream = func(_ int, _ []*schema.Message) []*schema.Message {
		return []*schema.Message{schema.AssistantMessage("直接回答", nil)}
	}
	c := &fakeMCPToolClient{listErr: errors.New("connection refused")}
	m := newTestMCPModel(llm, c)

	var chunks []string
	_, err := m.StreamResponse(context.Background(), []*schema.Message{schema.UserMessage("开灯")}, func(s string) {
		chunks = append(chunks, s)
	})
	if err == nil {
		t.Fatal("StreamResponse should fail loudly when tool discovery fails, got nil")
	}
	if len(llm.seen) != 0 {
		t.Fatalf("LLM must not be called when discovery fails, got %d calls", len(llm.seen))
	}
	if len(chunks) != 0 {
		t.Fatalf("no chunk should reach the client, got %v", chunks)
	}
}

// #1 工具列表为空同样视为 MCP 服务异常
func TestMCPModelNoToolsReturnsError(t *testing.T) {
	llm := &fakeToolLLM{}
	c := &fakeMCPToolClient{tools: nil}
	m := newTestMCPModel(llm, c)

	_, err := m.GenerateResponse(context.Background(), []*schema.Message{schema.UserMessage("开灯")})
	if err == nil {
		t.Fatal("GenerateResponse should fail when MCP server exposes no tools")
	}
}

func TestMCPModelMaxToolRounds(t *testing.T) {
	llm := &fakeToolLLM{}
	llm.gen = func(_ int, _ []*schema.Message) (*schema.Message, error) {
		return toolCallMessage("call_x", "list_devices", `{}`), nil
	}
	c := &fakeMCPToolClient{tools: testMCPTools()}
	m := newTestMCPModel(llm, c)
	m.maxToolRounds = 2

	got, err := m.GenerateResponse(context.Background(), []*schema.Message{schema.UserMessage("无限调用")})
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if !strings.Contains(got.Content, "上限") {
		t.Fatalf("want limit notice, got %q", got.Content)
	}
	if len(c.calls) != 2 {
		t.Fatalf("want 2 executed tool calls, got %d", len(c.calls))
	}
	if len(llm.seen) != 3 {
		t.Fatalf("want 3 LLM calls, got %d", len(llm.seen))
	}
}

func TestWithToolSystemPromptRequiresToolCall(t *testing.T) {
	original := []*schema.Message{
		schema.SystemMessage("记忆规则"),
		schema.UserMessage("打开风扇"),
	}
	out := withToolSystemPrompt(original)
	if len(out) != 2 {
		t.Fatalf("system prompt should be merged, got %d messages", len(out))
	}
	if original[0].Content != "记忆规则" {
		t.Fatalf("input message must not be mutated: %q", original[0].Content)
	}
	for _, want := range []string{"记忆规则", "必须先调用 control_device", "不要编造成功结果"} {
		if !strings.Contains(out[0].Content, want) {
			t.Errorf("system prompt missing %q: %s", want, out[0].Content)
		}
	}

	withoutSystem := withToolSystemPrompt([]*schema.Message{schema.UserMessage("你好")})
	if len(withoutSystem) != 2 || withoutSystem[0].Role != schema.System {
		t.Fatalf("tool prompt should be prepended: %+v", withoutSystem)
	}
}

func TestToolInfoFromMCP(t *testing.T) {
	info, err := toolInfoFromMCP(testMCPTools()[1])
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "control_device" || !strings.Contains(info.Desc, "帘子") {
		t.Fatalf("unexpected tool info: %+v", info)
	}
	js, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(js)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		`"enum":["relay_light","relay_fan","servo_curtain"]`,
		`"type":"integer"`,
		`"minimum":0`,
		`"maximum":180`,
		`"required":["device_id","action"]`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("schema missing %s: %s", want, s)
		}
	}
}

func TestToolInfoFromMCPWithoutParams(t *testing.T) {
	info, err := toolInfoFromMCP(testMCPTools()[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.ParamsOneOf != nil {
		t.Fatalf("no-arg tool should not have params: %+v", info.ParamsOneOf)
	}
}

func TestMCPModelClose(t *testing.T) {
	c := &fakeMCPToolClient{tools: testMCPTools()}
	m := newTestMCPModel(&fakeToolLLM{}, c)
	if _, err := m.getClient(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.Close()
	if !c.closed {
		t.Fatal("Close should close underlying client")
	}
}

// 用真实的 device-mcp-server 工具层验证自动发现结果，避免测试只覆盖假数据。
func TestMCPModelDiscoversRealDeviceTools(t *testing.T) {
	cmd := device.NewCommander(device.NewRegistry(device.DefaultDevices()), nil)
	raw, err := mcpgo.NewInProcessClient(mcpserver.New(cmd))
	if err != nil {
		t.Fatal(err)
	}
	c := mcpclient.NewFromClient(raw)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	llm := &fakeToolLLM{}
	llm.gen = func(_ int, _ []*schema.Message) (*schema.Message, error) {
		return schema.AssistantMessage("好的", nil), nil
	}
	m := &MCPModel{
		llm:           llm,
		maxToolRounds: 5,
		newClient: func(context.Context) (mcpToolClient, error) {
			return c, nil
		},
	}

	if _, err := m.GenerateResponse(ctx, []*schema.Message{schema.UserMessage("有哪些设备")}); err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if len(llm.bound) != 3 {
		t.Fatalf("unexpected discovered tools: %+v", llm.bound)
	}
	byName := map[string]*schema.ToolInfo{}
	for _, info := range llm.bound {
		byName[info.Name] = info
	}
	control, ok := byName["control_device"]
	if !ok || byName["list_devices"] == nil || byName["get_device_state"] == nil {
		t.Fatalf("unexpected discovered tools: %+v", byName)
	}
	js, err := control.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	rawSchema, err := json.Marshal(js)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"enum":["relay_light","relay_fan","servo_curtain"]`,
		`"minimum":0`,
		`"maximum":180`,
	} {
		if !strings.Contains(string(rawSchema), want) {
			t.Errorf("real tool schema missing %s: %s", want, rawSchema)
		}
	}
}
