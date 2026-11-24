这是一个用于记录踩过的坑的文件，避免重复踩坑。Agent 在跑任务或调试时先读这里，改动后如发现新坑请补充。

## 一、端口与环境速查

| 服务 | 端口 | 说明 |
|---|---|---|
| MySQL | `3307` | docker 映射；宿主 3306 被 WSL 原生 MySQL 占用 |
| Redis | `6380` | docker 映射；宿主 6379 被 WSL 原生 Redis 占用 |
| EMQX MQTT | `1883` | **不是 8083（WebSocket）也不是 18083（Dashboard）** |
| EMQX Dashboard | `18083` | 默认 admin / public，首次登录需改密 |
| MCP Server | `8081` | 端点 `http://localhost:8081/mcp` |
| 后端 | `9090` | `config/config.toml` |
| 前端 dev | `8080` | 被占用时会自动递增到 8081、8082…… |

**坑**：前端 dev server 会往上抢占端口，`8080/8081` 被占后它自己跑到 `8082`。所以**不要拿 8080~8082 当 MCP 测试端口**，用 `-http-addr :18081` 之类的高位端口。

## 二、启动与清理（每次测试前必做）

```bash

# 清场：环境里经常有上次残留的后台进程，关终端不会杀掉它们
tasklist //FI "IMAGENAME eq simulator.exe"
tasklist //FI "IMAGENAME eq device-mcp-server.exe"
taskkill //F //IM simulator.exe
taskkill //F //IM device-mcp-server.exe
```

**坑**：
- Git Bash 里 `taskkill` 必须写双斜杠：`//F //IM`，单斜杠会被当成路径。
- `bin/simulator.exe`、`bin/device-mcp-server.exe` 是编译产物，后台启动后**关终端不会退出**，表现为“我明明停了模拟器，设备还在线”。
- MCP Server 启动时如果连不上 MQTT 会直接退出，先确认 EMQX 已 ready。
- MySQL 容器 `Up` 之后还要等 10~20 秒才真正接受连接，太早连会报 `Can't connect to local MySQL server through socket`。

## 三、MCP 联调坑（重点）

### 1. MCP Server 端口冲突
`8081` 被占用时 `go run` 报 `bind: Only one usage of each socket address`。
先 `tasklist` 找旧进程并杀掉，或换 `-http-addr :18081`，**同时**改 `MCP_SERVER_URL`。

### 2. simulator 的 clientId 固定是 `esp32_01`
同时开两个 simulator 会互相踢下线，表现为“一会儿在线一会儿离线”、指令时好时坏。测试前务必确保只有一个。

### 3. 杀进程测不出“回执超时”——这是最容易错的一步
`taskkill /F` 会立刻关闭 TCP 连接，Broker **马上**发布遗嘱（`online:false`），所以设备表现为 0ms 离线，走的是“离线快速失败”分支，根本到不了超时。

- `-keepalive 60s` **没用**：keepalive 超时只对“网络静默中断”（拔网线、断电、进程假死）生效，对正常关 socket 无效。
- 要测超时/迟到回执，必须让模拟器“活着但不回执”，用内置测试开关：

```bash
# 场景A：在线但不回执（验证超时）
./bin/simulator.exe -drop-status
./bin/device-mcp-server.exe -cmd-timeout 3s
go run ./cmd/mcp-cli control relay_light on
# 期望 ~3000ms 后：[TOOL_ERROR] ... 在 3s 内未确认，可能离线，执行状态未知

# 场景B：回执迟到（验证迟到回执被丢弃）
./bin/simulator.exe -status-delay 5s
./bin/device-mcp-server.exe -cmd-timeout 3s
go run ./cmd/mcp-cli control relay_light on
# 期望：3s 超时；再过 2s，server 日志出现 drop unmatched status（req_id 相同）
```

### 4. `NodeUnknown` 不等于 `NodeOffline`
`Commander.Execute` 只在**明确收到 `online:false`** 时快速失败；从未收到状态（unknown）会照常下发并等超时。别把“3 秒超时”误判成“快速失败没生效”。

### 5. 验证工具层用 mcp-cli，不要用聊天界面
聊天里看到的是模型对工具结果的二次包装（会说“已经处于开启状态”这类话术），容易误判。判断工具行为一律用：

```bash
go run ./cmd/mcp-cli tools
go run ./cmd/mcp-cli list
go run ./cmd/mcp-cli control relay_light on
go run ./cmd/mcp-cli -url http://localhost:18081/mcp control servo_curtain set_angle 90
```

**坑**：`-url` 必须写在子命令**前面**，`flag.Parse` 之后才取子命令。

### 6. 其他约定
- `MCP_SERVER_URL` 默认 `http://localhost:8081/mcp`，分机部署必须改。
- 工具业务失败走 `result.IsError`，不是 HTTP 500，前端看到的是自然语言，排查看后端日志。
- 模型必须支持 tool calling，否则拿不到 `tool_calls`，表现为“只说不会做”。
- 回执匹配靠 `req_id`：Retain 旧回执的 `req_id` 为空直接忽略；请求超时后 `pending` 已删除，迟到的回执会被丢弃并打 `drop unmatched status` 日志。
- 已知局限：节点在线状态只在收到 status 时更新，没有心跳超时清理；设备静默失联且 LWT 未送达时，服务端会一直认为它在线。

## 四、数据与数据库坑

- **不要直接 `UPDATE`/`DELETE` `messages`、`sessions`**：后端有内存缓存（AIHelper、SessionMemory 的窗口与水位线、userMemoryHub），直改 DB 会和内存态不一致，`t_session_digest.digested_count` 会错位。只读排查没问题。
- 消息是**异步批量**落库的（`messagewriter`，最多 500ms / 50 条），进程崩溃会丢掉未 flush 的那批；刚发完立刻拉历史可能少 1~2 条，属正常。
- 查看表数据：Docker Desktop 的 **Exec 标签页已经在容器内部**，提示符是 `sh-5.1#`，此时只能敲 `mysql`，不能敲 `docker exec`：

```bash
mysql -uroot -p20050302 Gopheral      # -p 和密码之间不能有空格
```

宿主机侧（Git Bash）才用：

```bash
docker exec -it gopherai-mysql mysql -uroot -p20050302 Gopheral
# 报 "the input device is not a TTY" 时加 winpty 前缀
```

## 五、前端与构建坑

- 项目路径含括号 `GopherAI-v2(1)`，webpack 的 copy 插件 glob 会被当成 extglob 解析，`vue.config.js` 里已改成相对 glob，**不要改回去**。
- 前端接口前缀是 `/api`，靠 devServer 代理重写到 `/api/v1`（`vue.config.js`）。生产构建的 `dist` 需要 nginx 把 `/api` 反代到 `:9090/api/v1`，后端不托管静态资源。
- 历史接口分页语义：`page=1` 是**最新**一页，page 越大越早；不传 `pageSize` 返回全部。
- 流式是 SSE：每行 `data: <chunk>`，结束 `data: [DONE]`；新会话第一帧是 `data: {"sessionId": "..."}`。
