// Package server 设备控制 MCP 工具层：把 pkg/device 的能力暴露为 list_devices / control_device 两个工具。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"GopherAI/pkg/device"
	"GopherAI/pkg/devproto"
)

// 服务信息与工具名
const (
	ServerName    = "device-mcp-server"
	ServerVersion = "1.0.0"

	ToolListDevices   = "list_devices"
	ToolControlDevice = "control_device"

	argDeviceID = "device_id"
	argAction   = "action"
	argAngle    = devproto.ParamAngle
)

// Executor 设备控制能力，由 *device.Commander 实现
type Executor interface {
	Execute(ctx context.Context, req device.Request) (device.Outcome, error)
	Registry() *device.Registry
}

// New 创建注册了设备工具的 MCP Server
func New(exec Executor) *server.MCPServer {
	s := server.NewMCPServer(ServerName, ServerVersion, server.WithToolCapabilities(false))
	h := &handlers{exec: exec}
	s.AddTool(listDevicesTool(), h.listDevices)
	s.AddTool(controlDeviceTool(exec.Registry()), h.controlDevice)
	return s
}

func listDevicesTool() mcp.Tool {
	return mcp.NewTool(ToolListDevices,
		mcp.WithDescription("列出所有可控设备：device_id、中文名称、别名、类型、支持的 action 及参数范围。不包含设备当前状态。"),
		mcp.WithReadOnlyHintAnnotation(true),
	)
}

func controlDeviceTool(reg *device.Registry) mcp.Tool {
	return mcp.NewTool(ToolControlDevice,
		mcp.WithDescription(controlDescription(reg)),                                                                              //描述
		mcp.WithString(argDeviceID, mcp.Required(), mcp.Enum(reg.IDs()...), mcp.Description("目标设备 id")),                           //必须参数设备id
		mcp.WithString(argAction, mcp.Required(), mcp.Enum(reg.AllActions()...), mcp.Description("动作：继电器用 on/off，窗帘用 set_angle")), //必须参数:行为
		withInteger(argAngle,
			mcp.Min(devproto.MinAngle), mcp.Max(devproto.MaxAngle),
			mcp.Description(fmt.Sprintf("仅 set_angle 使用：%d 全关，%d 全开，例如拉开一半为 90", devproto.MinAngle, devproto.MaxAngle)),
		),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	)
}

// withInteger mcp-go 只提供 number，这里补充 integer 类型参数
func withInteger(name string, opts ...mcp.PropertyOption) mcp.ToolOption {
	return func(t *mcp.Tool) {
		schema := map[string]any{"type": "integer"}
		for _, o := range opts {
			o(schema)
		}
		t.InputSchema.Properties[name] = schema
	}
}

func controlDescription(reg *device.Registry) string {
	var b strings.Builder
	b.WriteString("控制智能家居设备，等待设备回执后返回真实执行结果与响应耗时。可用设备：\n")
	for _, d := range reg.Devices() {
		fmt.Fprintf(&b, "- %s %s（别名：%s）：%s", d.ID, d.Name, strings.Join(d.Aliases, "/"), strings.Join(d.ActionNames(), "/"))
		for _, a := range d.Actions {
			if a.Params != "" {
				fmt.Fprintf(&b, "，%s 参数 %s", a.Name, a.Params)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("一句话涉及多个设备时，对每个设备分别调用一次。")
	return b.String()
}

type handlers struct {
	exec Executor
}

func (h *handlers) listDevices(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(h.exec.Registry().Devices(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal devices: %w", err)
	}
	return mcp.NewToolResultText(string(b)), nil
}

func (h *handlers) controlDevice(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	req, err := parseRequest(r)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out, err := h.exec.Execute(ctx, req)
	if err != nil {
		var be *device.Error
		if errors.As(err, &be) {
			return mcp.NewToolResultError(be.Reason), nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return mcp.NewToolResultError("调用被取消或超时，执行状态未知"), nil
		}
		return nil, err
	}
	return mcp.NewToolResultText(successText(out)), nil
}

func parseRequest(r mcp.CallToolRequest) (device.Request, error) {
	args := r.GetArguments()
	id, _ := args[argDeviceID].(string)
	action, _ := args[argAction].(string)
	if id == "" || action == "" {
		return device.Request{}, errors.New("缺少必填参数 device_id 或 action")
	}
	req := device.Request{DeviceID: id, Action: action}
	raw, ok := args[argAngle]
	if !ok || raw == nil {
		return req, nil
	}
	switch v := raw.(type) {
	case float64:
		req.Angle = &v
	case int:
		f := float64(v)
		req.Angle = &f
	default:
		return device.Request{}, fmt.Errorf("angle 必须是整数，收到 %T", raw)
	}
	return req, nil
}

func successText(out device.Outcome) string {
	var call string
	if a, ok := out.Command.Params[devproto.ParamAngle]; ok {
		call = fmt.Sprintf("%s(angle=%v)", out.Command.Action, a)
	} else {
		call = out.Command.Action
	}
	return fmt.Sprintf("已执行：%s %s，当前状态 %s（设备响应 %dms）",
		out.Device.Name, call, formatState(out.State), out.Latency.Milliseconds())
}

func formatState(state map[string]any) string {
	if len(state) == 0 {
		return "未知"
	}
	keys := make([]string, 0, len(state))
	for k := range state {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, state[k]))
	}
	return strings.Join(parts, ", ")
}
