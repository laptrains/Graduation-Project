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
	if len(tools) != 2 || names[ToolListDevices].Name == "" || names[ToolControlDevice].Name == "" {
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
