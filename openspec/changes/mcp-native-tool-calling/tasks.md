## 1. 测试先行

- [x] 1.1 新增 `common/aihelper/mcp_model_test.go`：假 MCP 客户端 + 假 `ToolCallingChatModel`，覆盖 `tools/list` 只调用一次、工具绑定、ToolCall 参数透传
- [x] 1.2 覆盖多轮工具调用：第一轮 `list_devices`、第二轮 `control_device`，验证 3 次模型调用与 2 次工具调用
- [x] 1.3 覆盖 `isError=true` 的工具错误回灌，模型最终回答基于错误文本
- [x] 1.4 覆盖流式：纯文本逐 chunk 回调；工具轮后继续流式输出最终回答
- [x] 1.5 覆盖 `tools/list` 失败降级为普通对话、不绑定工具、不改写原始消息
- [x] 1.6 覆盖轮数上限：连续 ToolCall 时停止执行并返回"工具调用次数已达到上限"提示
- [x] 1.7 覆盖 MCP schema 转换：enum、integer、minimum/maximum、required；无参工具不生成 `ParamsOneOf`
- [x] 1.8 用真实 `common/mcp/server` + 进程内 MCP 客户端验证 `list_devices` / `control_device` 自动发现与 schema

## 2. 实现

- [x] 2.1 `MCPModel` 改为注入 `mcpToolClient` 接口与 `newClient` 工厂；默认实现完成 Streamable HTTP 客户端创建与 initialize
- [x] 2.2 实现 `discoverTools`：调用 `tools/list` 并缓存结果；`toolInfoFromMCP` 保留服务端 schema 约束
- [x] 2.3 删除写死的工具提示词、`AIToolCall`、`parseAIResponse` 与两段式提示词构造
- [x] 2.4 `GenerateResponse` 改为原生 Tool Calling 多轮循环，超过默认 5 轮返回提示
- [x] 2.5 `StreamResponse` 每轮用 `schema.ConcatMessages` 合并 chunk，有 ToolCall 时执行后进入下一轮
- [x] 2.6 `executeToolCalls` 生成带 `tool_call_id` 的 tool 消息；业务错误、协议错误、参数解析错误均回灌
- [x] 2.7 `tools/list` 或 `WithTools` 失败时降级为普通对话，并只在实际绑定工具时注入工具系统提示
- [x] 2.8 `Close` 关闭底层 MCP 客户端并清空引用；`go mod tidy` 将 `eino-contrib/jsonschema` 提升为直接依赖
- [x] 2.9 强化工具系统提示：设备开关/角度/状态变更必须先调用 `control_device`，不得根据历史或工具描述推断成功；补充 `TestWithToolSystemPromptRequiresToolCall`

## 3. 验证

- [x] 3.1 `go test -v ./common/aihelper/` 全部通过（11 个用例）
- [x] 3.2 `go test ./...` 全量通过
- [x] 3.3 `go vet ./common/aihelper/`、`go build ./...` 通过
- [x] 3.4 本地端到端：mcp-cli 工具发现/控制、聊天同步与流式、节点离线如实报错、MCP Server 停止后普通聊天降级均已验证

## 4. OpenSpec

- [x] 4.1 新增 `openspec/changes/mcp-native-tool-calling/`：proposal、design、tasks、spec
- [x] 4.2 更新 `add-device-mcp-server/proposal.md` 的已知影响，指向本 change
- [x] 4.3 `openspec validate mcp-native-tool-calling` 通过
