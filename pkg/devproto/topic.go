// Package devproto 定义设备 MQTT 协议：Topic 规则、消息结构与 QoS 约定。
// 模拟器、MCP Server、ESP32 固件共同遵守，本包只含纯数据，不做任何 IO。
package devproto

import (
	"fmt"
	"strings"
)

// Topic 前缀与后缀
const (
	topicPrefix  = "home/"
	suffixCmd    = "/cmd"
	suffixStatus = "/status"
	suffixData   = "/data"
)

// StatusWildcard 订阅全部状态消息（节点在线状态 + 执行器回执）
const StatusWildcard = "home/+/status"

// QoS 约定
const (
	QoSData   byte = 0 // 传感器数据
	QoSCmd    byte = 1 // 控制指令，不 Retain
	QoSStatus byte = 1 // 回执与在线状态，Retain
)

// CmdTopic 返回设备的指令 Topic
func CmdTopic(id string) string { return fmt.Sprintf("%s%s%s", topicPrefix, id, suffixCmd) }

// StatusTopic 返回设备或节点的状态 Topic
func StatusTopic(id string) string { return fmt.Sprintf("%s%s%s", topicPrefix, id, suffixStatus) }

// DataTopic 返回传感器的数据 Topic
func DataTopic(id string) string { return fmt.Sprintf("%s%s%s", topicPrefix, id, suffixData) }

// ParseDeviceID 从 home/{id}/{kind} 形式的 Topic 中解析出 id 与 kind（cmd/status/data）
func ParseDeviceID(topic string) (id, kind string, ok bool) {
	rest, found := strings.CutPrefix(topic, topicPrefix)
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
