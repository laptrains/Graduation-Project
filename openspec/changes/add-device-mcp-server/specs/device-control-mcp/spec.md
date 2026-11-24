## Purpose

提供一个独立部署的设备控制 MCP 服务，把可控设备能力以标准 MCP 工具暴露给任意 MCP 客户端，并保证工具返回的是设备真实执行结果而非"已下发"。

## ADDED Requirements

### Requirement: 独立进程与 Streamable HTTP 传输
设备控制 MCP 服务 SHALL 作为独立进程运行，通过 MCP Streamable HTTP 传输对外提供服务，默认监听地址 `:8081`，端点路径 `/mcp`；监听地址与 MQTT Broker 地址 SHALL 可通过启动参数配置。

#### Scenario: 客户端初始化与工具发现
- **WHEN** MCP 客户端连接 `http://localhost:8081/mcp` 并依次调用 `initialize`、`tools/list`
- **THEN** 初始化成功，工具列表包含且仅包含 `list_devices` 与 `control_device`

#### Scenario: Broker 不可用时启动
- **WHEN** 启动时 MQTT Broker 不可连接
- **THEN** 服务在有限时间内以非零退出码退出并输出明确错误，而不是对外提供无法工作的工具

### Requirement: 静态设备清单
服务 SHALL 维护一份静态的可控设备清单，MVP 包含：

| device_id | 名称 | 别名 | 类型 | 支持的 action |
| :--- | :--- | :--- | :--- | :--- |
| `relay_light` | 灯 | 电灯、台灯、照明 | relay | `on`、`off` |
| `relay_fan` | 风扇 | 电扇 | relay | `on`、`off` |
| `servo_curtain` | 窗帘 | 帘子 | servo | `set_angle`（`angle`：0–180 的整数，0 全关、180 全开） |

#### Scenario: 列出设备
- **WHEN** 调用 `list_devices`（无参数）
- **THEN** 返回上表所有设备，每项包含 `device_id`、中文名称、别名、类型、支持的 action 及其参数说明（含取值范围）

### Requirement: 控制工具参数约束
`control_device` SHALL 使用扁平参数：`device_id`（必填，字符串，枚举为设备清单中的 id）、`action`（必填，字符串，枚举为 `on`/`off`/`set_angle`）、`angle`（选填，整数，最小 0、最大 180，仅 `set_angle` 使用）。工具 MUST NOT 要求调用方传入嵌套参数对象。工具描述 SHALL 列出每个设备的名称、别名、支持的 action 与参数范围，以便 LLM 把口语化说法映射到正确设备。

#### Scenario: 工具 schema 暴露约束
- **WHEN** 客户端读取 `tools/list` 中 `control_device` 的输入 schema
- **THEN** `device_id` 与 `action` 带有枚举值列表，`angle` 为 integer 且带 `minimum: 0`、`maximum: 180`，schema 中不存在 object 类型参数

#### Scenario: 工具描述包含别名
- **WHEN** 客户端读取 `control_device` 的描述
- **THEN** 描述中包含"电灯""台灯""电扇""帘子"等别名及其对应的 device_id

### Requirement: 服务端参数校验
服务 SHALL 在下发前按设备清单校验参数：设备存在、action 为该设备所支持、`set_angle` 必须带 0–180 的整数 `angle`；非 `set_angle` 的 action 传入 `angle` 时忽略该参数。校验失败 MUST NOT 发布任何 MQTT 指令，并以工具错误结果返回具体原因。合法指令下发时，`angle` SHALL 转换为协议中的 `params.angle`。

#### Scenario: 设备不支持该 action
- **WHEN** 调用 `control_device(device_id="relay_light", action="set_angle", angle=90)`
- **THEN** 返回工具错误结果，说明灯只支持 on/off，且没有指令发布到 `home/relay_light/cmd`

#### Scenario: 角度越界
- **WHEN** 调用 `control_device(device_id="servo_curtain", action="set_angle", angle=200)`
- **THEN** 返回工具错误结果，说明角度范围为 0–180，且没有指令被发布

#### Scenario: 缺少角度
- **WHEN** 调用 `control_device(device_id="servo_curtain", action="set_angle")`
- **THEN** 返回工具错误结果，说明 set_angle 需要 angle 参数，且没有指令被发布

#### Scenario: 扁平参数转换为协议参数
- **WHEN** 调用 `control_device(device_id="servo_curtain", action="set_angle", angle=90)`
- **THEN** 发布到 `home/servo_curtain/cmd` 的指令为 `{"action":"set_angle","params":{"angle":90},...}`

#### Scenario: 未知设备
- **WHEN** 调用 `control_device(device_id="tv", action="on")`
- **THEN** 返回工具错误结果并列出可用设备 id

### Requirement: 等待设备回执后返回
服务 SHALL 在发布指令后等待 `req_id` 相同的回执，在超时时间（默认 3 秒，可配置）内收到回执才返回结果；`req_id` 不同或为空的回执（包括 Broker 推送的旧 Retain 回执）MUST 被忽略。

#### Scenario: 执行成功
- **WHEN** 调用 `control_device(device_id="relay_fan", action="on")`，设备在超时内回执 `success:true`
- **THEN** 返回成功结果，包含设备名称、执行的 action、执行后状态（如 `power: on`）与设备响应耗时（毫秒）

#### Scenario: 设备执行失败
- **WHEN** 设备回执 `success:false` 且带 `error`
- **THEN** 返回工具错误结果，包含设备回报的错误原因

#### Scenario: 回执超时
- **WHEN** 超时时间内未收到匹配回执（如设备未运行）
- **THEN** 返回工具错误结果，说明设备未响应、可能离线，且结果中不得声称操作已成功

#### Scenario: 忽略旧回执
- **WHEN** 订阅回执 Topic 时 Broker 立即推送了一条上一次操作的 Retain 回执
- **THEN** 该回执不被当作本次结果，服务继续等待本次 `req_id` 的回执

#### Scenario: 并发控制不同设备
- **WHEN** 客户端几乎同时调用开灯和设置窗帘角度
- **THEN** 两次调用各自拿到与自己 `req_id` 匹配的结果，互不串扰

### Requirement: 已知离线时快速失败
服务 SHALL 订阅设备所属节点的在线状态；当节点最新状态为离线时，`control_device` MUST 立即返回"设备离线"的工具错误结果而不发布指令、不等待超时。节点状态未知时按正常流程下发并等待回执。

#### Scenario: 节点已离线
- **WHEN** 节点 `esp32_01` 的最新状态为 `{"online":false,...}`，此时调用 `control_device(device_id="relay_light", action="on")`
- **THEN** 立即返回设备离线的工具错误结果，没有指令被发布

#### Scenario: 节点重新上线
- **WHEN** 节点发布 `{"online":true,...}` 后再次调用控制
- **THEN** 指令正常下发并等待回执

### Requirement: 控制调用可观测
每次 `control_device` 调用结束时，服务 SHALL 输出一条结构化日志，包含 `req_id`、`device_id`、`action`、`result`（`success`/`validation_error`/`offline`/`timeout`/`device_failed`/`not_connected`）与 `latency_ms`（从发布指令到收到回执或超时的耗时；未发布指令时为 0），用于响应时延统计。

#### Scenario: 成功调用记录时延
- **WHEN** 一次控制调用在 40ms 后收到成功回执
- **THEN** 日志中出现一条 `result=success`、`latency_ms≈40` 且带该次 `req_id` 的记录

#### Scenario: 校验失败也记录
- **WHEN** 一次控制调用因参数非法被拒绝
- **THEN** 日志中出现 `result=validation_error`、`latency_ms=0` 的记录

### Requirement: 错误以工具结果返回
所有业务失败（参数非法、设备离线、超时、设备执行失败）SHALL 以 `isError=true` 的工具结果返回可读的中文原因，而非 JSON-RPC 协议错误，使 LLM 能据此向用户解释。

#### Scenario: LLM 可读取失败原因
- **WHEN** 任一业务失败发生
- **THEN** 客户端收到正常的 `tools/call` 响应，其 `isError` 为 true，文本内容说明原因
