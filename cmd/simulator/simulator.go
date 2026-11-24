package main

//虚拟 ESP32（订阅 home/{device}/cmd，回报 status，带 Last Will）
import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"GopherAI/pkg/devproto"
)

const (
	connectTimeout    = 10 * time.Second
	publishTimeout    = 5 * time.Second
	disconnectQuiesce = 250 // ms，优雅断开时等待在途消息
)

// simulatorConfig 模拟器运行参数
type simulatorConfig struct {
	Broker    string
	NodeID    string
	Username  string
	Password  string
	Interval  time.Duration
	KeepAlive time.Duration
	// StatusDelay 收到指令后延迟多久才回执，用于验证"回执超时"与"迟到回执被丢弃"
	StatusDelay time.Duration
	// DropStatus 收到指令后完全不回执，用于验证"设备在线但确认丢失"
	DropStatus bool
}

// simulator 虚拟 ESP32 节点：周期上报传感器数据、订阅执行器指令并回报状态
type simulator struct {
	cfg    simulatorConfig
	env    *environment
	client mqtt.Client
}

func newSimulator(cfg simulatorConfig, env *environment) *simulator {
	s := &simulator{cfg: cfg, env: env}

	will, _ := json.Marshal(devproto.NodeStatus{Online: false, Reason: devproto.ReasonUnexpected})
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.NodeID).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetKeepAlive(cfg.KeepAlive).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetBinaryWill(devproto.StatusTopic(cfg.NodeID), will, devproto.QoSStatus, true).
		SetOnConnectHandler(s.onConnect).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			slog.Warn("connection lost", "err", err)
		})
	s.client = mqtt.NewClient(opts)
	return s
}

// Run 连接 Broker 并阻塞上报，直到 ctx 取消
func (s *simulator) Run(ctx context.Context) error {
	token := s.client.Connect()
	if !token.WaitTimeout(connectTimeout) {
		return fmt.Errorf("connect %s timeout", s.cfg.Broker)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("connect %s: %w", s.cfg.Broker, err)
	}

	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.reportSensors()
		}
	}
}

// Shutdown 优雅下线：先发 graceful_shutdown 覆盖遗嘱语义，再断开
func (s *simulator) Shutdown() {
	if s.client.IsConnected() {
		s.publishJSON(devproto.StatusTopic(s.cfg.NodeID), devproto.QoSStatus, true, devproto.NodeStatus{
			Online: false,
			Reason: devproto.ReasonGraceful,
			TS:     time.Now().Unix(),
		})
	}
	s.client.Disconnect(disconnectQuiesce)
	slog.Info("simulator stopped gracefully")
}

// onConnect 每次（重）连接后重新订阅并上报在线与执行器状态
func (s *simulator) onConnect(c mqtt.Client) {
	slog.Info("connected", "broker", s.cfg.Broker, "node", s.cfg.NodeID)

	for _, id := range actuatorIDs {
		topic := devproto.CmdTopic(id)
		if token := c.Subscribe(topic, devproto.QoSCmd, s.onCommand); token.Wait() && token.Error() != nil {
			slog.Error("subscribe failed", "topic", topic, "err", token.Error())
			continue
		}
		slog.Info("subscribed", "topic", topic)
	}

	s.publishJSON(devproto.StatusTopic(s.cfg.NodeID), devproto.QoSStatus, true, devproto.NodeStatus{
		Online:  true,
		Devices: allDeviceIDs,
		TS:      time.Now().Unix(),
	})
	for _, id := range actuatorIDs {
		s.publishJSON(devproto.StatusTopic(id), devproto.QoSStatus, true, buildStatus(id, command{}, s.env.state(id), nil))
	}
}

func (s *simulator) onCommand(_ mqtt.Client, msg mqtt.Message) {
	cmd, err := parseCommand(msg.Topic(), msg.Payload())
	if err != nil {
		slog.Warn("invalid command", "topic", msg.Topic(), "err", err)
		return
	}
	state, err := s.env.apply(cmd)
	slog.Info("command received",
		"device", cmd.DeviceID, "action", cmd.Action, "params", cmd.Params, "req_id", cmd.ReqID, "state", state, "err", err)
	status := buildStatus(cmd.DeviceID, cmd, state, err)

	// 测试开关：模拟"执行了但确认丢失"，节点保持在线
	if s.cfg.DropStatus {
		slog.Warn("status dropped by config", "device", cmd.DeviceID, "req_id", cmd.ReqID)
		return
	}
	// 测试开关：延迟回执，模拟慢设备，用于验证超时与迟到回执
	if s.cfg.StatusDelay > 0 {
		go func() {
			time.Sleep(s.cfg.StatusDelay)
			slog.Warn("delayed status publishing",
				"device", cmd.DeviceID, "req_id", cmd.ReqID, "delay", s.cfg.StatusDelay.String())
			s.publishJSON(devproto.StatusTopic(cmd.DeviceID), devproto.QoSStatus, true, status)
		}()
		return
	}
	s.publishJSON(devproto.StatusTopic(cmd.DeviceID), devproto.QoSStatus, true, status)
}

// parseCommand 解析指令，device_id 以 Topic 为准
func parseCommand(topic string, payload []byte) (command, error) {
	var cmd command
	if err := json.Unmarshal(payload, &cmd); err != nil {
		return cmd, fmt.Errorf("unmarshal: %w", err)
	}
	id, kind, ok := devproto.ParseDeviceID(topic)
	if !ok || kind != "cmd" {
		return cmd, fmt.Errorf("unexpected topic %q", topic)
	}
	cmd.DeviceID = id
	return cmd, nil
}

// buildStatus 构造执行回执，原样带回指令的 req_id
func buildStatus(id string, cmd command, state map[string]any, err error) devproto.ActuatorStatus {
	st := devproto.ActuatorStatus{
		DeviceID: id,
		Action:   cmd.Action,
		ReqID:    cmd.ReqID,
		Success:  err == nil,
		State:    state,
		TS:       time.Now().Unix(),
	}
	if err != nil {
		st.Error = err.Error()
	}
	return st
}

func (s *simulator) reportSensors() {
	now := time.Now().Unix()
	for _, d := range s.env.sample() {
		d.TS = now
		s.publishJSON(devproto.DataTopic(d.DeviceID), devproto.QoSData, false, d)
		slog.Debug("sensor reported", "device", d.DeviceID, "type", d.Type, "value", d.Value)
	}
}

func (s *simulator) publishJSON(topic string, qos byte, retained bool, v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal failed", "topic", topic, "err", err)
		return
	}
	token := s.client.Publish(topic, qos, retained, payload)
	if !token.WaitTimeout(publishTimeout) {
		slog.Warn("publish timeout", "topic", topic)
		return
	}
	if err := token.Error(); err != nil {
		slog.Error("publish failed", "topic", topic, "err", err)
	}
}
