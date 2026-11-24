# GopherAI-v2 项目说明

## 简介
基于 Go + Gin + GORM/MySQL + eino 的个人 AI 助手后端，并在此基础上实现"大语言模型 + MCP 协议的智能家居下行控制链路"。支持普通模型 / RAG / MCP 三种 AI 类型，会话消息持久化到 MySQL。

## 分层约定

```
controller → service → common/aihelper → pkg/memory → dao → model
cmd/device-mcp-server → common/mcp/server → pkg/device → common/mqtt → pkg/devproto
```

- `pkg/memory`：纯领域逻辑，不依赖 Gin / GORM / LLM 具体实现，通过 `Store` / `Summarizer` / `Extractor` 接口访问外部
- `pkg/device` / `pkg/devproto`：设备领域与 MQTT 协议定义，同样不依赖 MQTT 客户端与 MCP 实现，便于单测
- `dao/*`：唯一允许写 GORM 的层
- `common/aihelper`：会话生命周期管理（AIHelper / manager / factory / 后台任务触发点）
- `common/mcp/*`：MCP Server（工具定义）与 MCP Client（供 aihelper 调用），两者都不含业务校验

## 记忆系统（核心能力）

三层记忆，解决"消息永不删除、全量重读随历史变长越来越慢"的问题：

| 层 | 名称 | 归属 | 触发方式 |
|---|---|---|---|
| L1 | 近期窗口 | 会话 | 常驻内存 |
| L2 | 会话滚动摘要 | 会话 | 未摘要消息数达到 `digestThreshold` 时异步压缩 |
| L3 | 用户长期记忆 | 用户 | 未提取消息数达到 `extractThreshold` 时异步提取 |

规格详见：
- [会话记忆与摘要](specs/session-memory/spec.md)
- [用户长期记忆](specs/user-memory/spec.md)

## 系统现状

| 能力 | 状态 | 说明 |
|---|---|---|
| 会话聊天（同步 / SSE 流式） | ✅ 已完成 | `/api/v1/AI/chat/*` 六条接口；前端直读 `text/event-stream` |
| 三层记忆（L1 / L2 / L3） | ✅ 已完成 | `pkg/memory` + `t_session_digest` / `t_user_memory` / `t_session_extract` |
| MCP 设备控制（下行链路） | ✅ 已完成 | `pkg/devproto` → `pkg/device` → `common/mqtt` → `common/mcp/server` → `cmd/device-mcp-server` |
| 原生 Tool Calling（自动发现 + 多轮 + 流式） | ✅ 已完成 | `common/aihelper`，`tools/list` 自动发现，轮数上限 5 |
| 虚拟设备节点 | ✅ 已完成 | `cmd/simulator`，支持 `-drop-status` / `-status-delay` / `-anomaly-rate` / `-crash-after` |
| ESP32 实际固件 | ⏳ 未开始 | `firmware/` 尚未创建，节点侧由模拟器顶替，后端无需改动 |
| 上行传感器数据（采集 / 缓存 / 落库） | ❌ 不在范围 | 模拟器会周期性上报 `home/{node_id}/data`，服务端不消费 |

## 变更台账

| change | 状态 | 备注 |
|---|---|---|
| `mcp-native-tool-calling` | 实现已完成 | tasks 全部勾选；`go test ./common/aihelper/` 通过 |
| `add-device-mcp-server` | 实现已完成 | tasks 1.1–5.1、6.1–6.2 已验证；仅 6.3（MCP Inspector 截图）待补 |
| 待归档 | — | 两个 change 的 delta spec（`device-control-mcp`、`device-mqtt-protocol`、`ai-mcp-tool-calling`）尚未并入 `specs/`，需执行 `openspec archive <change>` |

## 共同设计原则
1. **进度用会话题内水位线记录**（摘要 `digested_count` / 提取 `extracted_count`），失败时水位线不动，下一轮重试同一批，不漏不重
2. **异步任务不阻塞主流程**：摘要与提取均在回复返回后后台执行
3. **增量合并，禁止全文重写**：摘要与记忆都只追加/修改被点名的部分，防止旧约束被悄悄挤掉
4. **配置关闭时优雅降级**：不提取、不注入，不报错
5. **工具结果以真实回执为准**：设备状态变更必须先调用 `control_device` 并等待回执，禁止根据历史或工具描述推断成功
