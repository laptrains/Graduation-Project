## Why

`add-device-mcp-server` 已经提供 `list_devices` / `control_device` 两个真实工具，但 `common/aihelper` 的 MCP 模式仍把工具名写死在提示词里（旧示例工具），并把模型回复当作 JSON 手工解析。结果是：

- LLM 不知道服务端实际有哪些工具，聊天入口无法控制设备；
- 新增或重命名工具必须同步修改提示词，无法复用标准 MCP 工具发现；
- 只支持单次工具调用，无法完成"先查设备再控制"这类多步任务。

本 change 把 MCP 模式改为标准流程：`tools/list` 自动发现 → 原生 Tool Calling → 多轮工具结果回灌。

## What Changes

- `common/aihelper` 的 MCP 模式在首次生成前调用 `tools/list` 自动发现工具，转换为 eino `ToolInfo`（保留 enum、min/max、required），并绑定到 `ToolCallingChatModel`。
- 移除写死的工具提示词与 `AIToolCall` 文本解析，改为读取模型返回的 `ToolCalls`。
- 支持单轮多个工具调用和多轮工具调用（默认上限 5 轮），每轮把 MCP 结果/错误以 `tool` 角色消息回灌。
- 流式接口在无工具调用时照常逐 chunk 推送；需要调用工具时先执行工具，再流式输出最终回答。
- 工具发现失败（MCP Server 未启动/网络错误）时降级为普通对话，不阻塞用户。
- 新增 `common/aihelper` 单元/集成测试，覆盖自动发现、多轮、错误回灌、流式、降级与真实服务端 schema 转换。
- 更新 `add-device-mcp-server` 中"aihelper 提示词未同步"的已知影响说明。

不在本次范围：MCP Server 新增工具、上行传感器数据、鉴权、工具级权限控制。

## Capabilities

### New Capabilities
- `ai-mcp-tool-calling`: AI 侧 MCP 工具自动发现、原生 Tool Calling 与多轮调用。

### Modified Capabilities
（无）

## Impact

- 代码：`common/aihelper/model.go`、新增 `common/aihelper/mcp_model_test.go`
- 依赖：直接依赖 `github.com/eino-contrib/jsonschema v1.0.2`（eino 已间接引入，无新增下载）
- 运行时：仍连接 `http://localhost:8081/mcp`；MCP Server 不可用时自动降级为普通对话
- 行为：MCP 模型不再依赖写死的工具提示词，LLM 可直接调用 `list_devices` / `control_device`
