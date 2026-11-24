## Purpose

让 MCP 模型通过标准工具发现和原生 Tool Calling 使用设备控制服务，不再依赖写死的工具名与 JSON 提示词。

## ADDED Requirements

### Requirement: 工具自动发现

`MCPModel` SHALL 在首次生成前调用 MCP `tools/list`，把返回工具转换为 eino `ToolInfo` 并绑定到模型；工具名、描述、参数 schema（枚举、最小/最大值、必填）MUST 来自服务端，不得写死。发现成功后 SHALL 缓存，避免每次对话重复请求。

#### Scenario: 启动 MCP 模式

- **WHEN** `device-mcp-server` 在 `http://localhost:8081/mcp` 运行，用户首次向 MCP 模型发消息
- **THEN** 客户端完成 initialize 与 tools/list，模型绑定的工具包含 `list_devices` 与 `control_device`

#### Scenario: schema 保留约束

- **WHEN** 转换 `control_device` 的输入 schema
- **THEN** `device_id` / `action` 的枚举、`angle` 的 integer 与 0–180 范围、required 字段在模型工具定义中可见

#### Scenario: 发现失败降级

- **WHEN** MCP Server 不可连接或 `tools/list` 返回错误
- **THEN** 本次请求仍以普通对话完成，不向用户抛出连接错误，也不绑定任何工具

### Requirement: 原生 Tool Calling

MCP 模型 SHALL 使用 `ToolCallingChatModel.WithTools` 绑定工具，并通过模型返回的 `ToolCalls` 字段判断调用；MUST NOT 要求模型输出固定 JSON 文本再由代码解析，MUST NOT 在提示词中写死工具清单。涉及设备开关、角度调节或状态变更的请求，系统提示 SHALL 要求模型先调用 `control_device`，只有收到成功工具结果后才可声称操作完成。

#### Scenario: 用户要求列出设备

- **WHEN** 用户问"有哪些设备"
- **THEN** 模型返回 `list_devices` 的 ToolCall，系统执行后把工具结果作为 tool 消息回灌

#### Scenario: 用户要求开灯

- **WHEN** 用户说"打开灯"
- **THEN** 模型返回 `control_device`，参数 `device_id=relay_light`、`action=on`，系统调用 MCP 工具

#### Scenario: 历史中有成功记录时仍需调用工具

- **WHEN** 会话历史包含此前控制成功的结果，用户再次要求操作设备而节点已离线
- **THEN** 模型仍先调用 `control_device`，收到离线错误后如实告知用户，不得根据历史对话或工具描述声称成功

### Requirement: 多轮工具调用

当模型一次回复包含多个 ToolCall，或工具结果后仍需继续调用时，`MCPModel` SHALL 在单次用户请求内继续循环，直到模型不再请求工具或达到轮数上限。默认上限 SHALL 为 5 轮；超过上限 SHALL 返回可读提示，不得无限循环。

#### Scenario: 先查后控

- **WHEN** 模型第一轮调用 `list_devices`，第二轮调用 `control_device`
- **THEN** 两次工具都会执行，最终回答基于两次结果生成

#### Scenario: 超轮数保护

- **WHEN** 模型连续请求工具超过上限
- **THEN** 停止调用并返回"工具调用次数已达到上限"的提示

### Requirement: 工具结果与错误回灌

每次工具调用 SHALL 生成带 `tool_call_id` 的 `tool` 角色消息；MCP 工具返回 `isError=true` 时，错误文本 SHALL 一并回灌给模型，使模型能向用户解释失败原因，而非编造成功。

#### Scenario: 设备离线

- **WHEN** `control_device` 返回"设备当前离线"
- **THEN** 该文本进入 tool 消息，模型据实回答失败原因

### Requirement: 流式响应

`StreamResponse` SHALL 对最终回答逐 chunk 调用回调；当某轮流式响应包含 ToolCall 时，执行工具后继续下一轮流式生成，最终返回值为最后一轮的完整正文。

#### Scenario: 工具后流式回答

- **WHEN** 第一轮流式响应要求调用 `control_device`，第二轮返回"灯已打开"
- **THEN** 回调收到最终回答的 chunk，返回值为完整最终回答

### Requirement: 可测试性

实现 SHALL 通过注入 MCP 客户端工厂与 `ToolCallingChatModel` 支持单元测试；测试 MUST 覆盖工具发现、多轮调用、错误回灌、流式、降级和真实服务端 schema 转换。

#### Scenario: 无外部依赖运行测试

- **WHEN** 执行 `go test ./common/aihelper/`
- **THEN** 全部用例通过，且不要求本地启动 OpenAI 或 EMQX
