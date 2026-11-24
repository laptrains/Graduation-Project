# session-memory 会话记忆与滚动摘要（L1 + L2）

## 背景
消息按会话持久化且**永不删除**。若每轮把全量历史喂给模型，token 成本与 DB IO 随历史线性增长。因此引入：

- **L1 近期窗口**：内存中保留最近消息，硬上限 `windowHardLimit`
- **L2 会话摘要**：未摘要消息达到阈值时，异步让 LLM 把旧消息增量滚进摘要，窗口只保留最近 `keepRecent` 条

进度存在 `t_session_digest`（每个会话一行）：`content`（摘要正文）+ `digested_count`（摘要水位线：按 id 升序前 N 条已并入摘要）+ `version`（乐观锁）。

## 需求

### Requirement: 懒加载与水位线
系统加载会话记忆时，只读取摘要行 + 水位线之后最近的消息，加载量与历史总量无关。

#### Scenario: 已有摘要的会话重启
- **WHEN** 会话已摘要前 20 条、共 30 条，服务重启后该会话再次被访问
- **THEN** 只从 DB 读摘要行和最近 `windowHardLimit` 条窗口消息，不读前 20 条原文

#### Scenario: 存量老会话
- **WHEN** 会话有大量历史且从未摘要
- **THEN** 只加载最近 `windowHardLimit` 条进入窗口，更早的历史不进上下文

### Requirement: 阈值触发与异步压缩
未摘要消息数（`窗口起点 + 窗口长度 - digested_count`）达到 `digestThreshold` 时，回复返回后在后台异步压缩；压缩中再次触发不重复执行（CAS 防重入）。

#### Scenario: 达到阈值
- **WHEN** 一轮对话结束后未摘要消息数 ≥ `digestThreshold`
- **THEN** 后台调用 LLM 增量更新摘要并推进水位线，本次响应不受影响

#### Scenario: 压缩中重复触发
- **WHEN** 一次压缩尚未完成时又来一轮对话
- **THEN** 不启动第二次压缩

### Requirement: 增量摘要而非全文重写
摘要输入为「已有摘要 + 新增消息」，输出为更新后的摘要；程序不删除消息，仅靠水位线推进。

#### Scenario: 第二次压缩
- **WHEN** 已有摘要、又积累了阈值数量的新消息
- **THEN** LLM 收到的提示词包含已有摘要与新增消息，生成合并后的新摘要

### Requirement: 失败不重试当前轮、不丢消息
压缩失败（LLM 错误 / DB 错误 / 版本冲突）时，水位线与摘要保持不变；下一轮再次达到阈值时重试同一批消息。

#### Scenario: LLM 调用失败
- **WHEN** 摘要 LLM 返回错误
- **THEN** `digested_count` 不前进，窗口内容不变，下一轮重试同一批

#### Scenario: 乐观锁冲突
- **WHEN** 两次压缩并发提交（`WHERE version=?` 影响 0 行）
- **THEN** 后者失败并放弃，状态仍是第一次提交的结果

### Requirement: 上下文组装与预算截断
注入 LLM 的上下文为 `[system: 长期记忆上位规则(如有) + 对话摘要] + 近期窗口`。预算 `contextMaxChars` 不够时只裁窗口最旧的消息，长期记忆与摘要不被截断；本轮用户问题（最后一条）永远保留。摘要正文自身限制 `digestMaxChars`。

#### Scenario: 预算不足
- **WHEN** 窗口总字数超预算
- **THEN** 从窗口头部（最旧）开始丢弃，直到满足预算，摘要与最后一条消息保留

## 配置（config.toml [memory]）
`digestThreshold=50`（生产）/ `keepRecent=10` / `windowHardLimit=30` / `digestMaxChars=800` / `contextMaxChars=24000` / `summarizeTimeoutS=60`

## 关键实现位置
- 领域：`pkg/memory/session_memory.go`（Load/NeedCompress/Compress）、`pkg/memory/assembler.go`（BuildContext/fitBudget）、`pkg/memory/prompt.go`（摘要提示词）
- 存储：`dao/digest/digest.go`（乐观锁 Save）
- 触发：`common/aihelper/aihelper.go`（maybeCompress）、`common/aihelper/summarizer.go`（DeepSeek 实现）
- 测试：`pkg/memory/memory_test.go`
