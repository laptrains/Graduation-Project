package device

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"GopherAI/pkg/devproto"
)

// DefaultTimeout 等待设备回执的默认超时
const DefaultTimeout = 3 * time.Second

// Publisher MQTT 发布能力，由基础设施层实现；未连接时应返回 ErrNotConnected
type Publisher interface {
	Publish(ctx context.Context, topic string, qos byte, retained bool, payload []byte) error
}

// NodeState 节点在线状态
type NodeState string

const (
	NodeUnknown NodeState = "unknown"
	NodeOnline  NodeState = "online"
	NodeOffline NodeState = "offline"
)

// Outcome 一次成功控制的结果
type Outcome struct {
	Device  Device
	Command devproto.Command
	State   map[string]any
	Latency time.Duration
}

// Commander 指令下发与回执匹配
type Commander struct {
	reg     *Registry
	pub     Publisher
	timeout time.Duration
	logger  *slog.Logger
	newID   func() string

	mu      sync.Mutex
	pending map[string]chan devproto.ActuatorStatus
	nodes   map[string]NodeState
}

// CommanderOption 可选配置
type CommanderOption func(*Commander)

// WithTimeout 设置回执超时
func WithTimeout(d time.Duration) CommanderOption {
	return func(c *Commander) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithLogger 设置日志
func WithLogger(l *slog.Logger) CommanderOption {
	return func(c *Commander) { c.logger = l }
}

// WithIDGenerator 设置 req_id 生成器（测试用）
func WithIDGenerator(f func() string) CommanderOption {
	return func(c *Commander) { c.newID = f }
}

// NewCommander 创建 Commander
func NewCommander(reg *Registry, pub Publisher, opts ...CommanderOption) *Commander {
	c := &Commander{
		reg:     reg,
		pub:     pub,
		timeout: DefaultTimeout,
		logger:  slog.Default(),
		newID:   uuid.NewString,
		pending: map[string]chan devproto.ActuatorStatus{},
		nodes:   map[string]NodeState{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Registry 返回设备注册表
func (c *Commander) Registry() *Registry { return c.reg }

// NodeState 返回节点在线状态
func (c *Commander) NodeState(nodeID string) NodeState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.nodes[nodeID]; ok {
		return s
	}
	return NodeUnknown
}

// Execute 校验、下发指令并等待设备回执
func (c *Commander) Execute(ctx context.Context, req Request) (out Outcome, err error) {
	var (
		reqID     string
		published time.Time
	)
	defer func() {
		latency := time.Duration(0)
		if !published.IsZero() {
			latency = time.Since(published)
			out.Latency = latency
		}
		c.logger.InfoContext(ctx, "device_control",
			"req_id", reqID,
			"device_id", req.DeviceID,
			"action", req.Action,
			"result", string(resultOf(err)),
			"latency_ms", latency.Milliseconds(),
		)
	}()

	dev, cmd, err := c.reg.Validate(req)
	if err != nil {
		return Outcome{}, err
	}
	if c.NodeState(dev.NodeID) == NodeOffline {
		return Outcome{}, newErr(ErrOffline, "%s（%s）所在节点 %s 已离线，指令未下发", dev.Name, dev.ID, dev.NodeID)
	}

	reqID = c.newID()
	cmd.ReqID = reqID
	payload, err := json.Marshal(cmd)
	if err != nil {
		return Outcome{}, err
	}

	ch := make(chan devproto.ActuatorStatus, 1)
	c.mu.Lock()
	c.pending[reqID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, reqID)
		c.mu.Unlock()
	}()

	published = time.Now()
	if err := c.pub.Publish(ctx, devproto.CmdTopic(dev.ID), devproto.QoSCmd, false, payload); err != nil {
		published = time.Time{}
		if errors.Is(err, ErrNotConnected) {
			return Outcome{}, newErr(ErrNotConnected, "MQTT 未连接，指令未下发：%v", err)
		}
		return Outcome{}, err
	}

	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case st := <-ch:
		if !st.Success {
			return Outcome{}, newErr(ErrDeviceFailed, "%s 执行失败：%s", dev.Name, st.Error)
		}
		return Outcome{Device: dev, Command: cmd, State: st.State}, nil
	case <-timer.C:
		return Outcome{}, newErr(ErrTimeout, "%s 在 %s 内未确认，可能离线，执行状态未知", dev.Name, c.timeout)
	case <-ctx.Done():
		return Outcome{}, ctx.Err()
	}
}

// HandleStatus 处理 home/+/status 消息：节点状态更新在线表，执行器回执投递给挂起请求
func (c *Commander) HandleStatus(topic string, payload []byte) {
	id, kind, ok := devproto.ParseDeviceID(topic)
	if !ok || kind != "status" {
		return
	}
	if c.reg.IsNode(id) {
		c.handleNode(id, payload)
		return
	}
	if _, ok := c.reg.Get(id); ok {
		c.handleActuator(payload)
	}
}

func (c *Commander) handleNode(id string, payload []byte) {
	var st devproto.NodeStatus
	if err := json.Unmarshal(payload, &st); err != nil {
		c.logger.Warn("invalid node status", "node", id, "err", err)
		return
	}
	state := NodeOffline
	if st.Online {
		state = NodeOnline
	}
	c.mu.Lock()
	c.nodes[id] = state
	c.mu.Unlock()
	c.logger.Info("node_status", "node", id, "state", string(state), "reason", st.Reason)
}

func (c *Commander) handleActuator(payload []byte) {
	var st devproto.ActuatorStatus
	if err := json.Unmarshal(payload, &st); err != nil || st.ReqID == "" {
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[st.ReqID]
	c.mu.Unlock()
	if !ok {
		// 不在挂起表：请求已超时/已结束，或为 Retain 旧回执，一律丢弃
		c.logger.Info("drop unmatched status",
			"device_id", st.DeviceID, "req_id", st.ReqID, "action", st.Action)
		return
	}
	select {
	case ch <- st:
	default:
	}
}
