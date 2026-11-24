package devproto

// 节点与设备 ID，静态注册，固件与模拟器需保持一致
const (
	NodeESP32 = "esp32_01"

	DeviceLight   = "relay_light"
	DeviceFan     = "relay_fan"
	DeviceCurtain = "servo_curtain"

	DeviceDHT22  = "dht22_01"
	DeviceBH1750 = "bh1750_01"
)

// 设备 action
const (
	ActionOn       = "on"
	ActionOff      = "off"
	ActionSetAngle = "set_angle"
)

// ParamAngle 舵机角度参数名
const ParamAngle = "angle"

// 舵机角度范围
const (
	MinAngle = 0
	MaxAngle = 180
)

// 节点离线原因
const (
	ReasonUnexpected = "unexpected_disconnect" // Last Will
	ReasonGraceful   = "graceful_shutdown"
)

// Command 下行控制指令，发布到 home/{device_id}/cmd
type Command struct {
	DeviceID string         `json:"device_id"`
	Action   string         `json:"action"`
	Params   map[string]any `json:"params"`
	// ReqID 由发送方生成，设备回执原样带回，用于请求与回执匹配
	ReqID string `json:"req_id,omitempty"`
}

// ActuatorStatus 执行器回执与当前状态，发布到 home/{device_id}/status（Retain）
type ActuatorStatus struct {
	DeviceID string         `json:"device_id"`
	Action   string         `json:"action,omitempty"`
	ReqID    string         `json:"req_id,omitempty"`
	Success  bool           `json:"success"`
	Error    string         `json:"error,omitempty"`
	State    map[string]any `json:"state"`
	TS       int64          `json:"ts"`
}

// NodeStatus 节点在线状态，发布到 home/{node_id}/status（Retain，含 Last Will）
type NodeStatus struct {
	Online  bool     `json:"online"`
	Reason  string   `json:"reason,omitempty"`
	Devices []string `json:"devices,omitempty"`
	TS      int64    `json:"ts,omitempty"`
	// Conn 连接代次：节点每次进程启动/重连时生成新值，online 消息与遗嘱携带同一个值，
	// 服务端据此丢弃旧连接迟到到达的遗嘱，避免覆盖新连接的在线状态。可为空（兼容旧固件，无此保护）
	Conn string `json:"conn,omitempty"`
}

// SensorData 上行传感器数据，ts 为设备侧时间，仅作参考
type SensorData struct {
	DeviceID string  `json:"device_id"`
	Type     string  `json:"type"`
	Value    float64 `json:"value"`
	TS       int64   `json:"ts"`
}
