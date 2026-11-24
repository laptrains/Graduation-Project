## 1. 共享协议

- [x] 1.1 新建 `pkg/devproto`：Topic 函数（`CmdTopic/StatusTopic/DataTopic/ParseDeviceID`）、消息结构（`Command` 含 `req_id`、`ActuatorStatus` 含 `req_id`、`NodeStatus`、`SensorData`）、设备与节点 id 常量、QoS 常量；单测覆盖 Topic 解析与 JSON 字段名，`go test ./pkg/devproto/` 通过
- [x] 1.2 模拟器改用 `pkg/devproto`，删除 `cmd/simulator/message.go` 中重复定义；回执原样带回 `req_id`，缺失时为空；`go test ./cmd/simulator/` 通过，并新增"回执带回 req_id"测试（`TestParseCommand_TopicWinsAndReqIDEchoed`、`TestBuildStatus_NoReqIDAndFailure`）

## 2. 设备领域层

- [x] 2.1 实现 `pkg/device/registry.go`：静态清单（灯/风扇/窗帘，含名称、别名、类型、所属节点、action 与参数说明）与 `Validate`；表驱动单测覆盖 spec 中"不支持的 action / 角度越界 / 缺少角度 / 未知设备 / 合法指令"
- [x] 2.2 定义错误类型 `ErrValidation/ErrOffline/ErrTimeout/ErrDeviceFailed/ErrNotConnected`，可用 `errors.As` 取出中文原因；单测验证
- [x] 2.3 实现 `pkg/device/commander.go`：`Publisher` 接口、req_id 生成、挂起表、`Execute(ctx, cmd)`（返回结果含 latency）、`HandleStatus(topic, payload)`、节点在线状态（unknown/online/offline）、每次调用结束输出 `device_control` 结构化日志；使用假 Publisher 单测覆盖：成功（latency>0）、设备失败、超时、忽略旧/空 req_id 回执、并发两设备互不串扰、节点离线快速失败且不发布、重新上线后恢复、ctx 取消；`go test -race ./pkg/device/` 通过

## 3. MQTT 基础设施

- [x] 3.1 新建 `common/mqtt`：paho 连接封装（连接超时、自动重连、`OnConnect` 重新订阅 `home/+/status`）、实现 `device.Publisher`、断线时 Publish 返回 `ErrNotConnected`、消息回调转发给 `Commander.HandleStatus`；`go vet ./common/mqtt/` 通过（该包无单测，行为由 `cmd/device-mcp-server` 端到端覆盖）

## 4. MCP 工具层

- [x] 4.1 删除 `common/mcp/go.mod`、`go.sum`、`main.go` 及旧示例工具相关代码，`server`/`client` 包并入根 module；`go build ./...` 通过
- [x] 4.2 改写 `common/mcp/server`：注册 `list_devices`、`control_device`（扁平参数：`device_id`/`action` 枚举、`angle` integer 0–180；描述由 Registry 生成并含别名），`angle` 转为协议 `params.angle`，业务错误统一转 `NewToolResultError`；用 mcp-go 进程内客户端单测验证：`tools/list` 只含两个工具、schema 带枚举与 min/max 且无 object 参数、描述含别名、`list_devices` 输出三台设备、非法参数返回 `isError=true`、成功结果含耗时
- [x] 4.3 精简 `common/mcp/client`：删除示例工具专用方法，新增 `ListTools`；`go build ./...` 通过

## 5. 进程入口

- [x] 5.1 新建 `cmd/device-mcp-server/main.go`：flag（`-http-addr :8081`、`-broker`/`MQTT_BROKER`、`-client-id`（自动追加 `-<PID>`）、`-cmd-timeout`）、装配、Streamable HTTP 启动、信号优雅退出；Broker 不可达时 10s 内非零退出——用 `-broker tcp://127.0.0.1:1` 启动验证

## 6. 端到端验证

- [x] 6.1 EMQX + 模拟器 + MCP Server 运行时，用 `common/mcp/client` 编写的验证脚本（或 `go test` 集成测试，带 build tag `integration`）依次验证：`tools/list` 两个工具；`control_device(relay_fan, on)` 成功且模拟器日志显示执行；`control_device(servo_curtain, set_angle, angle=90)` 成功且 EMQX 上 cmd 消息为 `params.angle=90`；`angle=200` 返回 isError 且 EMQX 上无该 cmd 消息；MCP Server 日志中每次调用都有 `device_control` 记录与 `latency_ms`（实际以 `cmd/mcp-cli` 完成上述验证，见 `AGENTS.md` 联调记录）
- [x] 6.2 停止模拟器（Ctrl+C 优雅下线）后调用 `control_device` 立即返回离线错误；用 `-crash-after` 模拟断电后同样返回离线错误；重新启动模拟器后调用恢复成功（注意：`taskkill /F` 走得是 LWT 快速失败分支，不能用来验证回执超时）
- [ ] 6.3 用 MCP Inspector（`npx @modelcontextprotocol/inspector`，Transport 选 Streamable HTTP，URL `http://localhost:8081/mcp`）完成一次开灯，截图留作论文"跨客户端复用"证据 —— **待补截图**，功能本身已由 `mcp-cli` 间接验证
- [x] 6.4 更新根目录 `design.md`：第七章消息格式补充 `req_id` 与回执示例、MCP 工具表标注本期范围与扁平参数、分层补充 `pkg/devproto`/`cmd/*`；第六章第三方客户端示例改为 MCP Inspector；补齐第八章"1. 上行：数据采集"小节，更新下行链路示例
