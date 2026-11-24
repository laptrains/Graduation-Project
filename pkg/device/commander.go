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

// pendingReq 挂起中的请求：回执必须同时命中 req_id 与设备，才允许投递
type pendingReq struct {
	ch       chan devproto.ActuatorStatus
	deviceID string
}

// nodeEntry 节点在线状态及最后应用的连接代次（用于丢弃旧连接迟到的遗嘱）
type nodeEntry struct {
	state NodeState
	conn  string
}

// Commander 指令下发与回执匹配
type Commander struct {
	reg     *Registry
	pub     Publisher
	timeout time.Duration
	logger  *slog.Logger
	newID   func() string

	mu      sync.Mutex
	pending map[string]*pendingReq
	nodes   map[string]nodeEntry
	// states 每个设备的最新状态（初始状态上报与控制回执都会刷新），供 get_device_state 查询
	states map[string]devproto.ActuatorStatus
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
		pending: map[string]*pendingReq{},
		nodes:   map[string]nodeEntry{},
		states:  map[string]devproto.ActuatorStatus{},
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
	if e, ok := c.nodes[nodeID]; ok {
		return e.state
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
	c.pending[reqID] = &pendingReq{ch: ch, deviceID: dev.ID}
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
		c.handleActuator(id, payload)
	}
}

func (c *Commander) handleNode(id string, payload []byte) {
	// online 用指针解析：字段缺失（如误发到节点 topic 的其他消息）时保持原状态，不误判离线
	var st struct {
		Online *bool   `json:"online"`
		Reason string  `json:"reason"`
		Conn   string  `json:"conn"`
	}
	if err := json.Unmarshal(payload, &st); err != nil {
		c.logger.Warn("invalid node status", "node", id, "err", err)
		return
	}
	if st.Online == nil {
		c.logger.Warn("node status without online field ignored", "node", id)
		return
	}

	c.mu.Lock()
	if !*st.Online {
		// 离线消息的连接代次与当前记录不一致：这是旧连接迟到到达的遗嘱，丢弃，
		// 避免覆盖同 ClientID 新连接的在线状态。任一侧为空（旧固件）时不启用该保护
		if e, ok := c.nodes[id]; ok && e.conn != "" && st.Conn != "" && e.conn != st.Conn {
			c.mu.Unlock()
			c.logger.Warn("stale node status from old connection dropped",
				"node", id, "msg_conn", st.Conn, "current_conn", e.conn, "reason", st.Reason)
			return
		}
	}
	state := NodeOffline
	if *st.Online {
		state = NodeOnline
	}
	c.nodes[id] = nodeEntry{state: state, conn: st.Conn}
	c.mu.Unlock()
	c.logger.Info("node_status", "node", id, "state", string(state), "reason", st.Reason)
}

// handleActuator 刷新设备最新状态缓存，并把带回 req_id 的执行回执投递给挂起请求；
// topic 与 payload 任一设备不匹配即整条丢弃
func (c *Commander) handleActuator(topicID string, payload []byte) {
	var st devproto.ActuatorStatus
	if err := json.Unmarshal(payload, &st); err != nil {
		return
	}
	if st.DeviceID != "" && st.DeviceID != topicID {
		// payload 声称的 device_id 与 topic 不一致：整条消息不可信，不更新缓存也不投递
		c.logger.Warn("drop status with mismatched device_id",
			"topic_device", topicID, "payload_device", st.DeviceID, "req_id", st.ReqID)
		return
	}

	c.mu.Lock()
	// 初始状态上报（req_id 为空）与执行回执都刷新最新状态；device_id 以 topic 为准
	st.DeviceID = topicID
	c.states[topicID] = st
	req, ok := c.pending[st.ReqID]
	c.mu.Unlock()

	if st.ReqID == "" {
		// 初始状态上报，无需投递
		return
	}
	if !ok {
		// 不在挂起表：请求已超时/已结束，或为 Retain 旧回执，一律丢弃
		c.logger.Info("drop unmatched status",
			"device_id", st.DeviceID, "req_id", st.ReqID, "action", st.Action)
		return
	}
	if req.deviceID != topicID {
		// 回执发布在别的设备 topic 上：req_id 命中但设备不符，可能是发错 topic 或异常发布者
		c.logger.Warn("drop status from wrong device topic",
			"topic_device", topicID, "expected_device", req.deviceID, "req_id", st.ReqID)
		return
	}
	select {
	case req.ch <- st:
	default:
	}
}

// DeviceState 返回设备最近一次上报的状态（初始状态或执行回执）；设备未注册或从未上报时 ok=false
func (c *Commander) DeviceState(deviceID string) (devproto.ActuatorStatus, bool) {
	if _, ok := c.reg.Get(deviceID); !ok {
		return devproto.ActuatorStatus{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.states[deviceID]
	return st, ok
}
