## Context

`common/aihelper` 的 MCP 模式原先采用"两段式提示词"：

1. 用固定提示词要求模型返回 `{"isToolCall":true,"toolName":"...","args":{...}}`；
2. 代码用 `json.Unmarshal`（外加固定工具名兜底）解析后再调用 MCP。

`add-device-mcp-server` 删除示例工具、新增 `list_devices` / `control_device` 后，提示词没有同步，模型无法发现真实工具，聊天入口的设备控制链路实际不可用。同时该方案无法表达工具 schema，也不支持多轮调用。

本次改造把工具发现、调用和结果回灌交还给模型协议本身。

## Goals

- 工具清单完全由 `tools/list` 决定，新增/改名工具不需要改提示词。
- 使用 eino `ToolCallingChatModel` 的原生 Tool Calling，支持一次多个 ToolCall。
- 支持多轮工具调用，并设上限避免死循环。
- 保留流式输出体验；MCP 不可用时不影响普通聊天。
- 通过注入客户端工厂与模型接口实现无外部依赖测试。

## Non-Goals

- 不在 MCP Server 侧新增上行工具（`get_sensor_data` / `get_history`）。
- 不做工具级权限、审计或人工确认。
- 不改变 `common/mcp/client` 的传输实现与 `device-mcp-server` 的工具定义。

## Decisions

### 1. 用最小接口隔离 MCP 客户端

新增内部接口：

```go
type mcpToolClient interface {
    ListTools(ctx context.Context) ([]mcp.Tool, error)
    CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error)
    Close() error
}
```

`*client.MCPClient` 已满足该接口；`MCPModel` 通过 `newClient func(context.Context) (mcpToolClient, error)` 注入，默认实现完成 `NewMCPClient` + `Initialize`。测试可注入进程内客户端或假实现。

### 2. MCP schema 原样转换为 eino ToolInfo

`mcp.Tool` 的 `InputSchema` 先序列化为 JSON，再反序列化为 `jsonschema.Schema`，最后通过 `schema.NewParamsOneOfByJSONSchema` 绑定。这样 enum、minimum/maximum、required 不会被手工转换丢失。无参数工具不设置 `ParamsOneOf`。

### 3. 首次使用时发现并缓存

`discoverTools` 以 `sync.Mutex` 保护 `client`、`tools`、`discovered`。发现成功后缓存；失败不缓存，下一次请求会重试。并发首次调用可能重复一次 `tools/list`，但不影响正确性。

### 4. 单一循环同时支持 Generate 与 Stream

`GenerateResponse` 与 `StreamResponse` 共用同一套轮次逻辑：

1. 绑定工具（发现失败则用未绑定模型）；
2. 调用模型；
3. 无 `ToolCalls` → 返回；
4. 有 `ToolCalls` → 每个调用生成一条带 `tool_call_id` 的 `tool` 消息，连同 assistant 消息一起追加到上下文；
5. 轮数达到上限 → 返回可读提示。

`StreamResponse` 每轮用 `schema.ConcatMessages` 合并 chunk 后再判断 `ToolCalls`；正常文本轮的内容随收随回调。工具轮的正文（通常为空）也会被推送，这是流式场景下无法提前判断"是否最终回答"的取舍。

### 5. 工具错误也是上下文

MCP 业务错误通过 `CallToolResult.IsError` 返回。实现把文本前缀为"工具返回错误："后作为 tool 消息回灌，模型可据此解释失败；协议/传输错误同样转成 tool 消息，避免整个请求 500。

### 6. 降级而非失败

`tools/list` 失败或 `WithTools` 失败时记录日志并使用原始 `llm`，不注入工具系统提示，保证 MCP Server 未启动时普通对话仍可用。

## Risks / Trade-offs

- **工具轮正文被提前推送**：若模型在发起 ToolCall 的同一条消息里输出正文，前端可能先看到一段中间文字。主流模型通常不在工具调用时输出正文，暂不额外缓冲。
- **工具清单缓存**：MCP Server 运行期间新增工具不会立即生效，重启 AI 服务或新建会话后重新发现。当前工具清单是静态的，可接受。
- **最大轮数**：默认 5 轮，极端多步任务会被截断并提示；可通过 `maxToolRounds` 调整。
- **模型兼容性**：依赖底层 OpenAI 兼容接口支持 `tools` / `tool_calls`。DeepSeek/OpenAI 兼容端点均支持；不支持时表现为模型不返回 ToolCall，退化为普通回答。

## Testing Strategy

先写测试，再实现：

| 用例 | 覆盖点 |
| :--- | :--- |
| `TestMCPModelGenerateDiscoversToolsAndCalls` | tools/list 调用一次、绑定工具、tool 结果回灌 |
| `TestMCPModelGenerateMultiRoundToolCalls` | 两轮工具调用与调用顺序、参数透传 |
| `TestMCPModelGenerateToolErrorIsFedBack` | `isError=true` 文本进入上下文 |
| `TestMCPModelStreamFinalAnswer` | 纯文本流式 chunk 与完整返回值 |
| `TestMCPModelStreamCallsToolThenStreamsFinal` | 工具轮后继续流式生成 |
| `TestMCPModelDiscoveryFailureFallsBackToPlainLLM` | 发现失败降级且不注入工具提示 |
| `TestMCPModelMaxToolRounds` | 轮数上限保护 |
| `TestWithToolSystemPromptRequiresToolCall` | 设备变更类请求必须调用工具的提示约束，且不修改原始消息 |
| `TestToolInfoFromMCP` / `TestToolInfoFromMCPWithoutParams` | schema 枚举/范围/必填与无参工具 |
| `TestMCPModelDiscoversRealDeviceTools` | 真实 `device-mcp-server` 工具层的端到端发现 |

验证命令：

```bash
go test ./common/aihelper/
go test ./...
go vet ./common/aihelper/
go build ./...
```
