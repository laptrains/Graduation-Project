## Context

- 现有 `common/mcp` 是独立 go module（`github.com/kaitai/gopherai-mcp`，go 1.25.4），只含示例工具，`main.go` 用 `-mode server|client` 切换。
- 根 module `GopherAI` 已依赖 `mark3labs/mcp-go v0.43.2` 与 `eclipse/paho.mqtt.golang v1.5.0`。
- `cmd/simulator` 已实现虚拟节点 `esp32_01`：订阅 `home/{relay_light,relay_fan,servo_curtain}/cmd`，回执发布到 `home/{id}/status`（Retain），节点状态 `home/esp32_01/status` 带 Last Will。消息结构目前定义在模拟器 `main` 包内，无法复用。
- EMQX 由 `docker-compose.yml` 提供（`127.0.0.1:1883`）。
- 项目规则：三横两竖分层、跨层方法首参 `context.Context`、禁止跨层泄露第三方结构体。

## Goals / Non-Goals

**Goals:**
- 一个可独立启动的 MCP Server 进程，用 MCP 客户端（含第三方客户端）即可控制模拟器中的设备并拿到真实回执
- 协议定义只有一份，模拟器、MCP Server、后续后端接入共用
- 关键逻辑（参数校验、回执匹配、离线快速失败）有不依赖 Broker 的单元测试

**Non-Goals:**
- 上行链路（`get_sensor_data`、`get_history`、数据缓存与落库）；`list_devices` 不返回设备当前状态
- `aihelper` 的 Tool Calling 改造
- stdio 传输、鉴权（单人本机使用）、多节点动态发现、设备能力自描述

## Decisions

### D1. 目录与分层

```
pkg/devproto/                 协议层（纯数据 + Topic 函数，无 IO）
  topic.go                    CmdTopic / StatusTopic / DataTopic / ParseDeviceID
  message.go                  Command / ActuatorStatus / NodeStatus / SensorData
pkg/device/                   领域层（无 MQTT / MCP 依赖）
  registry.go                 静态设备清单（名称/别名/节点/action）+ Validate(cmd)
  commander.go                Commander：req_id 生成、挂起表、回执匹配、离线判定
                              依赖 Publisher 接口（Publish(ctx, topic, qos, retain, payload)）
infra（common/mqtt/）          paho 封装：连接、订阅 home/+/status、实现 Publisher，
                              收到消息回调 Commander.HandleStatus
common/mcp/server/            MCP 工具层：工具定义、参数解析、结果/错误转换，调用 pkg/device
common/mcp/client/            精简为通用 MCP 客户端（去掉示例工具专用方法）
cmd/device-mcp-server/main.go 进程入口：flag 解析、装配、优雅退出
```

依赖方向：`cmd → common/mcp/server → pkg/device → pkg/devproto`；`common/mqtt → pkg/devproto`，并通过接口注入 `pkg/device`，`pkg/device` 不 import paho。
理由：Commander 的匹配逻辑是本 change 最易出错的部分，抽离 IO 后可用假 Publisher 单测。

**备选**：直接在 `common/mcp/server` 里写 paho 调用 —— 简单但无法单测回执匹配，放弃。

### D2. 回执匹配：常驻订阅 + 挂起表

```
 control_device
     |
     v
 Commander.Execute(ctx, cmd)
     |  1. Registry.Validate        -- 失败: 直接返回业务错误
     |  2. 节点离线?               -- 是: 直接返回离线错误
     |  3. req_id = 新ID; pending[req_id] = chan
     |  4. Publish home/{id}/cmd
     |  5. select { <-chan | <-timer(3s) | <-ctx.Done() }
     |  6. delete pending[req_id]
     v
 结果
                 ^
 MQTT 回调 ------+  HandleStatus(msg): 解析 req_id
                    命中 pending -> 投递(非阻塞)；未命中/空 -> 丢弃
```

- 启动时**一次性**订阅 `home/+/status`，而不是每次调用临时订阅：避免订阅建立前回执已到达的竞态，也避免 Retain 旧消息每次都被重放。
- 挂起表 `map[string]chan Result`，`sync.Mutex` 保护；channel 缓冲 1，投递用非阻塞写，超时后迟到的回执被丢弃。
- `req_id`：`uuid.NewString()`（根 module 已有 `google/uuid`）。
- 超时默认 3s，`-cmd-timeout` 可配。

**备选**：每次调用 Subscribe 单个 Topic 再 Unsubscribe —— 实现直观，但有竞态且 SUBACK 往返增加延迟，放弃。

### D3. 区分节点状态与执行器回执

`home/+/status` 上同时有两种消息：节点状态（`home/esp32_01/status`，含 `online`）与执行器回执（`home/relay_light/status`，含 `success`）。按 Topic 中的 id 区分：id 属于静态清单的节点 → 节点状态；属于设备 → 执行器回执；其他忽略。

静态清单每个设备记录所属 `node_id`（MVP 全部为 `esp32_01`）。节点在线状态存于 Commander 内存：`unknown`（初始）/ `online` / `offline`。Retain 机制保证 Server 启动订阅后立即拿到节点最新状态。

### D4. 混合粒度的工具 schema（扁平参数）

- `list_devices`：无参数，返回 JSON 文本（设备数组，含名称、别名、action 与参数说明），便于 LLM 与人工阅读。本期**不返回设备当前状态**——本 change 只覆盖下行链路，设备状态展示属于上行链路范围。
- `control_device` 使用扁平参数：
  - `device_id`：`mcp.Enum(...)`（从 Registry 生成）
  - `action`：`mcp.Enum("on","off","set_angle")`
  - `angle`：`mcp.WithNumber` + `mcp.Min(0)` + `mcp.Max(180)`，选填
  - 不使用嵌套 `params` 对象：LLM 生成嵌套对象易出错（填成 JSON 字符串、漏层级），扁平参数可直接在 schema 上表达取值范围。工具层负责把 `angle` 转成协议的 `params.angle`，MQTT 协议本身不变。后续新增参数（如亮度）按同样方式追加选填字段。
- 工具描述由 Registry 动态生成，每个设备一行：`relay_light 灯（别名：电灯/台灯/照明）：on/off`。新增静态设备时无需改工具代码。
- 成功结果文本示例：`已执行：窗帘 set_angle(angle=90)，当前状态 angle=90（设备响应 38ms）`。

**备选**：保留 `params` object —— 通用性更好，但 LLM 填错率高且无法在 schema 层约束范围，放弃。

### D5. 错误语义

`pkg/device` 定义错误类型（`ErrValidation`、`ErrOffline`、`ErrTimeout`、`ErrDeviceFailed`），工具层统一 `errors.As` 后用 `mcp.NewToolResultError(中文原因)` 返回；只有工具层自身的编程错误才返回 Go error（→ JSON-RPC 错误）。

### D6. 进程与传输

- `cmd/device-mcp-server`：`-http-addr :8081`、`-broker tcp://127.0.0.1:1883`（也读 `MQTT_BROKER`）、`-client-id device-mcp-server`（实际 clientId 追加 `-<PID>` 后缀，避免多开时互踢）、`-cmd-timeout 3s`。
- 使用 `server.NewStreamableHTTPServer(mcpServer).Start(addr)`，端点 `/mcp`；`SIGINT/SIGTERM` 时 `Shutdown(ctx)` 并断开 MQTT。
- 不做鉴权：单人本机使用，监听 `:8081` 即可。
- MQTT 首次连接失败（10s 超时）直接退出；运行中断线由 paho 自动重连，`OnConnect` 中重新订阅；断线期间调用返回"MQTT 未连接"的业务错误。

### D6.1 时延日志

`Commander.Execute` 在返回前用 `log/slog` 输出一条结构化日志：

```
level=INFO msg=device_control req_id=... device_id=servo_curtain action=set_angle result=success latency_ms=38
```

`latency_ms` 从 Publish 成功前一刻计到收到回执 / 超时；校验失败、离线等未发布的情况记 0。成功结果文本同时附带耗时。论文的"响应时延测试"直接从这条日志取样，不额外写压测工具。

> 规则要求统一使用 `pkg/observability`，但该包尚不存在；本期先使用标准库 `slog`，字段命名保持一致，待可观测性封装落地后替换。

### D7. 迁移 `common/mcp`

- 删除 `common/mcp/go.mod`、`go.sum`、`main.go` 与示例工具相关代码；`server`、`client` 包并入根 module（import 路径变为 `GopherAI/common/mcp/server`）。
- `client` 保留 `NewMCPClient/Initialize/Ping/CallTool/GetToolResultText/Close`，新增 `ListTools`，删除示例工具专用方法。
- 模拟器改用 `pkg/devproto`，`onCommand` 回执带回 `req_id`。

## Risks / Trade-offs

- [多个 MCP Server 实例使用相同 clientId 会互踢] → clientId 自动追加 PID 后缀
- [设备执行成功但回执丢失 → 报超时，LLM 告诉用户失败而实际已执行] → 超时文案写"未收到确认，状态未知"，而不是"失败"；QoS 1 降低丢失概率
- [节点 Retain 状态陈旧（Broker 重启丢失 Retain）→ 状态为 unknown] → unknown 时按正常下发 + 超时处理，不误判离线
- [静态清单与模拟器/固件设备不一致] → 设备 id 常量放在 `pkg/devproto`，清单与模拟器共同引用
- [`aihelper` MCP 模式在本 change 后失效] → proposal 中已声明，由下一个 change 修复；本期用 MCP 客户端直连验证
