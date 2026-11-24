package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"GopherAI/common/mcp/client"
	"GopherAI/pkg/device"
	"GopherAI/pkg/devproto"
)

// echoPublisher 模拟设备：收到指令后立即回执成功
type echoPublisher struct {
	cmd       *device.Commander
	published []devproto.Command
}

func (p *echoPublisher) Publish(_ context.Context, _ string, _ byte, _ bool, payload []byte) error {
	var c devproto.Command
	if err := json.Unmarshal(payload, &c); err != nil {
		return err
	}
	p.published = append(p.published, c)
	state := map[string]any{"power": c.Action}
	if a, ok := c.Params[devproto.ParamAngle]; ok {
		state = map[string]any{"angle": a}
	}
	st, _ := json.Marshal(devproto.ActuatorStatus{DeviceID: c.DeviceID, ReqID: c.ReqID, Success: true, State: state})
	go func() {
		time.Sleep(5 * time.Millisecond)
		p.cmd.HandleStatus(devproto.StatusTopic(c.DeviceID), st)
	}()
	return nil
}

func setup(t *testing.T) (*client.MCPClient, *echoPublisher) {
	t.Helper()
	pub := &echoPublisher{}
	cmd := device.NewCommander(device.NewRegistry(device.DefaultDevices()), pub,
		device.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	pub.cmd = cmd

	raw, err := mcpclient.NewInProcessClient(New(cmd))
	if err != nil {
		t.Fatal(err)
	}
	c := client.NewFromClient(raw)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, pub
}

func call(t *testing.T, c *client.MCPClient, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := c.CallTool(context.Background(), ToolControlDevice, args)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	return res
}

func TestToolsList(t *testing.T) {
	c, _ := setup(t)
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]mcp.Tool{}
	for _, tl := range tools {
		names[tl.Name] = tl
	}
	if len(tools) != 3 || names[ToolListDevices].Name == "" || names[ToolControlDevice].Name == "" || names[ToolGetDeviceState].Name == "" {
		t.Fatalf("unexpected tools: %v", tools)
	}

	ctl := names[ToolControlDevice]
	schema, _ := json.Marshal(ctl.InputSchema)
	s := string(schema)
	for _, want := range []string{`"enum":["relay_light","relay_fan","servo_curtain"]`, `"type":"integer"`, `"minimum":0`, `"maximum":180`} {
		if !strings.Contains(s, want) {
			t.Errorf("schema missing %s: %s", want, s)
		}
	}
	for name, p := range ctl.InputSchema.Properties {
		if m, ok := p.(map[string]any); ok && m["type"] == "object" {
			t.Errorf("param %s must not be object", name)
		}
	}
	for _, alias := range []string{"电灯", "台灯", "电扇", "帘子"} {
		if !strings.Contains(ctl.Description, alias) {
			t.Errorf("description missing alias %s", alias)
		}
	}

	// get_device_state：device_id 必填且带枚举
	gs := names[ToolGetDeviceState]
	gsSchema, _ := json.Marshal(gs.InputSchema)
	if !strings.Contains(string(gsSchema), `"enum":["relay_light","relay_fan","servo_curtain"]`) {
		t.Errorf("get_device_state schema missing device enum: %s", gsSchema)
	}
}

func TestListDevices(t *testing.T) {
	c, _ := setup(t)
	res, err := c.CallTool(context.Background(), ToolListDevices, nil)
	if err != nil || res.IsError {
		t.Fatalf("list_devices failed: %v %v", err, client.ResultText(res))
	}
	var devs []device.Device
	if err := json.Unmarshal([]byte(client.ResultText(res)), &devs); err != nil {
		t.Fatal(err)
	}
	if len(devs) != 3 {
		t.Fatalf("want 3 devices, got %d", len(devs))
	}
}

func TestControlDevice_Success(t *testing.T) {
	c, pub := setup(t)
	res := call(t, c, map[string]any{argDeviceID: devproto.DeviceCurtain, argAction: devproto.ActionSetAngle, argAngle: 90})
	text := client.ResultText(res)
	if res.IsError || !strings.Contains(text, "窗帘") || !strings.Contains(text, "angle=90") || !strings.Contains(text, "ms") {
		t.Fatalf("unexpected result: %s", text)
	}
	if len(pub.published) != 1 || pub.published[0].Params[devproto.ParamAngle] != float64(90) {
		t.Fatalf("protocol params wrong: %+v", pub.published)
	}
}

func TestControlDevice_ValidationIsToolError(t *testing.T) {
	c, pub := setup(t)
	cases := []map[string]any{
		{argDeviceID: devproto.DeviceCurtain, argAction: devproto.ActionSetAngle, argAngle: 200},
		{argDeviceID: devproto.DeviceCurtain, argAction: devproto.ActionSetAngle},
		{argDeviceID: devproto.DeviceLight, argAction: devproto.ActionSetAngle, argAngle: 90},
		{argDeviceID: "tv", argAction: devproto.ActionOn},
		{argAction: devproto.ActionOn},
	}
	for _, args := range cases {
		res := call(t, c, args)
		if !res.IsError || client.ResultText(res) == "" {
			t.Errorf("args %v should be tool error, got %s", args, client.ResultText(res))
		}
	}
	if len(pub.published) != 0 {
		t.Fatalf("invalid calls must not publish, got %d", len(pub.published))
	}
}

func callState(t *testing.T, c *client.MCPClient, deviceID string) *mcp.CallToolResult {
	t.Helper()
	res, err := c.CallTool(context.Background(), ToolGetDeviceState, map[string]any{argDeviceID: deviceID})
	if err != nil {
		t.Fatalf("call get_device_state: %v", err)
	}
	return res
}

// 查询设备状态：未上报过 → 明确说明；控制后 → 返回缓存的最新状态
func TestGetDeviceState(t *testing.T) {
	c, _ := setup(t)

	// 从未上报过状态：正常结果（非错误）但明确说明没有记录，模型不得编造状态
	res := callState(t, c, devproto.DeviceLight)
	text := client.ResultText(res)
	if res.IsError || !strings.Contains(text, "暂无状态上报") {
		t.Fatalf("no-record state should explain itself, got %q isError=%v", text, res.IsError)
	}

	// 控制后状态被缓存
	call(t, c, map[string]any{argDeviceID: devproto.DeviceLight, argAction: devproto.ActionOn})
	res = callState(t, c, devproto.DeviceLight)
	text = client.ResultText(res)
	if res.IsError || !strings.Contains(text, "power=on") {
		t.Fatalf("want power=on after control, got %q", text)
	}

	// 窗帘角度
	call(t, c, map[string]any{argDeviceID: devproto.DeviceCurtain, argAction: devproto.ActionSetAngle, argAngle: 90})
	res = callState(t, c, devproto.DeviceCurtain)
	if text := client.ResultText(res); res.IsError || !strings.Contains(text, "angle=90") {
		t.Fatalf("want angle=90, got %q", text)
	}

	// 未知设备 → tool error
	res = callState(t, c, "tv")
	if !res.IsError {
		t.Fatalf("unknown device should be tool error, got %q", client.ResultText(res))
	}
}

// 节点离线时查询状态要提示"可能已过期"，不得让用户误以为状态是实时的
func TestGetDeviceStateOfflineWarning(t *testing.T) {
	c, pub := setup(t)

	call(t, c, map[string]any{argDeviceID: devproto.DeviceLight, argAction: devproto.ActionOn})
	// 节点离线
	pub.cmd.HandleStatus(devproto.StatusTopic(devproto.NodeESP32), []byte(`{"online":false,"reason":"unexpected_disconnect"}`))

	res := callState(t, c, devproto.DeviceLight)
	text := client.ResultText(res)
	if res.IsError || !strings.Contains(text, "power=on") {
		t.Fatalf("cached state should still be returned, got %q", text)
	}
	if !strings.Contains(text, "离线") {
		t.Fatalf("offline node should be flagged, got %q", text)
	}
}
