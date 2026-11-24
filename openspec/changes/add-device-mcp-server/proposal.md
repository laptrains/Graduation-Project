## Why

`design.md` 的核心卖点是"LLM 通过 MCP 协议调用独立的设备控制服务"，但现有 `common/mcp` 只有一个示例工具，且是独立 go module，无法复用设备协议定义。需要先打通**下行链路**（用户指令 → MCP Server → EMQX → 设备），在无硬件阶段即可用模拟器端到端验证。

## What Changes

- 新增设备控制 MCP Server（独立进程，Streamable HTTP 传输，监听 `:8081/mcp`），注册两个工具：
  - `list_devices`：返回可控设备清单及每个设备的名称、别名、支持的 action / 参数范围
  - `control_device`：扁平参数（`device_id`、`action`、`angle`），下发控制指令，**等待设备回执**后返回真实执行结果与设备响应耗时
- 设备清单使用**静态注册表**（灯、风扇、窗帘，含中文别名），工具参数采用"通用工具 + 枚举/范围约束"的混合粒度
- 每次控制调用输出结构化时延日志（`req_id`、`latency_ms`、`result`），支撑论文响应时延测试
- MQTT 下行协议增加 `req_id` 字段：指令携带、设备回执原样带回，用于回执精确匹配
- 新增共享协议包，模拟器与 MCP Server 共用 Topic 与消息结构定义
- 模拟器适配：回执中回传 `req_id`
- 工具执行失败（设备离线、参数非法、回执超时、设备执行失败）以 MCP 工具错误结果返回，LLM 可读取原因
- **BREAKING**：移除 `common/mcp` 下的示例工具及其独立 `go.mod`，统一并入根 module；`common/mcp/client` 中示例工具专用方法同步移除

不在本次范围（后续 change）：
- 上行链路：`get_sensor_data`、`get_history`、传感器数据订阅与缓存、Redis/MySQL 落库
- `aihelper` 改造为原生 Tool Calling + `tools/list` 自动发现 + 多轮工具调用（下一个 change `mcp-native-tool-calling`）

## Capabilities

### New Capabilities
- `device-control-mcp`: 设备控制 MCP 服务——工具清单、指令下发、回执匹配、错误语义与传输方式
- `device-mqtt-protocol`: 设备下行指令与回执的 MQTT 协议约定（Topic、消息字段、`req_id` 关联、QoS/Retain）

### Modified Capabilities
（无。现有 `session-memory`、`user-memory` 规格不受影响）

## Impact

- 代码：删除 `common/mcp/go.mod`、`common/mcp/go.sum`、`common/mcp/main.go`、示例工具；新增 `cmd/device-mcp-server/`、共享协议包；改写 `common/mcp/server`、精简 `common/mcp/client`；修改 `cmd/simulator` 回执逻辑
- 依赖：复用根 module 已有的 `mark3labs/mcp-go v0.43.2` 与 `eclipse/paho.mqtt.golang v1.5.0`，无新增
- 运行时：MCP Server 依赖 EMQX（`tcp://127.0.0.1:1883`）
- 已知影响：`common/aihelper` 的 MCP 模式提示词仍写死旧示例工具名，本 change 后该模式无法调用到示例工具；已由后续 change `mcp-native-tool-calling` 修复（`tools/list` 自动发现 + 原生 Tool Calling），本期仍通过 MCP 客户端直连验证
