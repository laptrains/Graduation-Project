// Package device 设备控制领域层：静态设备注册表、参数校验、指令下发与回执匹配。
// 本包不依赖 MQTT / MCP 实现，IO 通过 Publisher 接口注入。
package device

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"GopherAI/pkg/devproto"
)

// Kind 设备类型
type Kind string

const (
	// KindRelay 继电器，开/关两态
	KindRelay Kind = "relay"
	// KindServo 舵机，角度控制
	KindServo Kind = "servo"
)

// Device 可控设备描述
type Device struct {
	ID      string   `json:"device_id"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	Kind    Kind     `json:"kind"`
	NodeID  string   `json:"node_id"`
	Actions []Action `json:"actions"`
}

// Action 设备支持的动作及参数说明
type Action struct {
	Name   string `json:"name"`
	Params string `json:"params,omitempty"`
}

// Supports 判断设备是否支持指定 action
func (d Device) Supports(action string) bool {
	for _, a := range d.Actions {
		if a.Name == action {
			return true
		}
	}
	return false
}

// ActionNames 返回设备支持的 action 名称
func (d Device) ActionNames() []string {
	names := make([]string, 0, len(d.Actions))
	for _, a := range d.Actions {
		names = append(names, a.Name)
	}
	return names
}

// Request 一次控制请求（扁平参数）
type Request struct {
	DeviceID string
	Action   string
	// Angle 仅 set_angle 使用；nil 表示未提供
	Angle *float64
}

// Registry 静态设备注册表
type Registry struct {
	devices []Device
	byID    map[string]Device
}

var relayActions = []Action{{Name: devproto.ActionOn}, {Name: devproto.ActionOff}}

var servoActions = []Action{{
	Name:   devproto.ActionSetAngle,
	Params: fmt.Sprintf("angle：%d–%d 的整数，%d 全关，%d 全开", devproto.MinAngle, devproto.MaxAngle, devproto.MinAngle, devproto.MaxAngle),
}}

// DefaultDevices MVP 静态设备清单
func DefaultDevices() []Device {
	return []Device{
		{ID: devproto.DeviceLight, Name: "灯", Aliases: []string{"电灯", "台灯", "照明"}, Kind: KindRelay, NodeID: devproto.NodeESP32, Actions: relayActions},
		{ID: devproto.DeviceFan, Name: "风扇", Aliases: []string{"电扇"}, Kind: KindRelay, NodeID: devproto.NodeESP32, Actions: relayActions},
		{ID: devproto.DeviceCurtain, Name: "窗帘", Aliases: []string{"帘子"}, Kind: KindServo, NodeID: devproto.NodeESP32, Actions: servoActions},
	}
}

// NewRegistry 创建注册表
func NewRegistry(devices []Device) *Registry {
	r := &Registry{devices: devices, byID: make(map[string]Device, len(devices))}
	for _, d := range devices {
		r.byID[d.ID] = d
	}
	return r
}

// Devices 返回全部设备
func (r *Registry) Devices() []Device { return r.devices }

// Get 按 id 查找设备
func (r *Registry) Get(id string) (Device, bool) {
	d, ok := r.byID[id]
	return d, ok
}

// IDs 返回全部设备 id
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.devices))
	for _, d := range r.devices {
		ids = append(ids, d.ID)
	}
	return ids
}

// AllActions 返回全部设备支持的 action 去重集合（有序）
func (r *Registry) AllActions() []string {
	set := map[string]struct{}{}
	for _, d := range r.devices {
		for _, a := range d.Actions {
			set[a.Name] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// IsNode 判断 id 是否为某设备所属节点
func (r *Registry) IsNode(id string) bool {
	for _, d := range r.devices {
		if d.NodeID == id {
			return true
		}
	}
	return false
}

// Validate 校验请求并转换为协议指令（不含 req_id）
func (r *Registry) Validate(req Request) (Device, devproto.Command, error) {
	d, ok := r.byID[req.DeviceID]
	if !ok {
		return Device{}, devproto.Command{}, validationErr("未知设备 %q，可用设备：%s", req.DeviceID, strings.Join(r.IDs(), ", "))
	}
	if !d.Supports(req.Action) {
		return d, devproto.Command{}, validationErr("%s（%s）不支持 %q，只支持：%s", d.Name, d.ID, req.Action, strings.Join(d.ActionNames(), "/"))
	}

	cmd := devproto.Command{DeviceID: d.ID, Action: req.Action, Params: map[string]any{}}
	if req.Action != devproto.ActionSetAngle {
		return d, cmd, nil
	}
	if req.Angle == nil {
		return d, devproto.Command{}, validationErr("set_angle 需要 angle 参数（%d–%d）", devproto.MinAngle, devproto.MaxAngle)
	}
	a := *req.Angle
	if a != math.Trunc(a) {
		return d, devproto.Command{}, validationErr("angle 必须是整数，收到 %v", a)
	}
	if a < devproto.MinAngle || a > devproto.MaxAngle {
		return d, devproto.Command{}, validationErr("angle 超出范围：%v，允许 %d–%d", a, devproto.MinAngle, devproto.MaxAngle)
	}
	cmd.Params[devproto.ParamAngle] = int(a)
	return d, cmd, nil
}
