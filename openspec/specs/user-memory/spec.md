# user-memory 用户级长期记忆（L3）

## 背景
L2 摘要保留的是"这个会话聊过什么"，仍有两个洞：
1. **重写会丢东西**：约束（"不许用 emoji"）可能在某次摘要中被挤掉
2. **不跨会话**：A 会话说过的偏好，B 会话不知道

因此引入用户级长期记忆：跨会话、只增量合并、永不重写、永不被预算裁掉。

## 数据模型

### `t_user_memory`（每用户一行，唯一索引 user_name）
`entries`（条目 JSON 数组）+ `version`（乐观锁）。**只存内容，不存进度。**

### `t_session_extract`（每会话一行）
`extracted_count`：提取水位线。**进度是会话级的**（每个会话提取到哪各自独立），内容是用户级的（所有会话共享一行）。

## 需求

### Requirement: 独立的提取流水线
提取与摘要是两条互不依赖的流水线：各自的触发阈值（`extractThreshold`，默认 6 条）、各自的水位线、各自的防重入标志。摘要把消息压出窗口不影响提取（提取可回 DB 读原文）。

#### Scenario: 先提取后摘要
- **WHEN** 6 条消息先触发提取、之后才触发摘要
- **THEN** 两条流水线各自正常推进，互不影响

#### Scenario: 提取水位线落后于摘要水位线
- **WHEN** 待提取消息已被摘要压出窗口
- **THEN** 提取从 DB 按 `ListMessages(offset, limit)` 读回原文；若一批跨越窗口起点，本轮只提取 DB 段、水位线停在窗口起点，下一轮从窗口接续（不漏不重）

#### Scenario: DB 缺失该段消息
- **WHEN** 批量写入失败导致该段消息在 DB 中不存在
- **THEN** 水位线跳过到窗口起点，避免永久卡住

### Requirement: 条目内容只记三类，且用户明确表达
LLM 只提取：constraint（约束，如"回答不用 emoji"）、preference（偏好）、fact（长期事实）。**不记录**：临时要求（"这题用英文回答"）、任务内容、助手说的话、推测信息、已有重复内容。

#### Scenario: 用户声明约束
- **WHEN** 用户说"以后回答都不要用 emoji"
- **THEN** 新增一条 type=constraint 的条目

#### Scenario: 临时要求不入库
- **WHEN** 用户说"这道题用英文回答"
- **THEN** 提取输出空操作 `{"ops":[]}`，不产生新条目

### Requirement: 增量合并，禁止重写淘汰
LLM 输出 add/update/delete 操作，程序增量合并；未被点名的条目保持不变。删除后 id 不复用。
提取时（写）：
  Extract → 模型输出 add/update/delete → ApplyOps 增量合并 → 事务提交到 DB

对话时（读）：
  BuildContext → Render 把条目渲染成一段文字 → 注入 system 提示词

#### Scenario: 用户撤销约束
- **WHEN** 用户说"现在可以用 emoji 了"
- **THEN** LLM 输出 delete（含条目 id），程序删除该条目，其余条目不动

#### Scenario: 用户更正记忆
- **WHEN** 用户更正已有信息
- **THEN** LLM 输出 update（含 id 与新文本），程序原地更新

### Requirement: 上限拒绝而非淘汰
条目数 ≤ `userMemoryMaxEntries`（30）、单条 ≤ `userMemoryEntryMaxChars`（100 字）、总计 ≤ `userMemoryMaxChars`（1000 字）。超限**拒绝新增并记日志**，不淘汰旧条目（防止早期约束被悄悄挤掉）。运行中若频繁出现拒绝日志，应优先检查提取提示词是否放进了不该记的内容，而不是调大上限。

#### Scenario: 条目已满
- **WHEN** 已有 30 条，LLM 又输出 add
- **THEN** 该 add 被拒绝并记日志，已有 30 条原封不动

### Requirement: 记忆与水位线同事务提交
一次提取的「写记忆」与「推进会话水位线」在同一 DB 事务提交，失败整体回滚。

#### Scenario: 提交中途失败
- **WHEN** 记忆已写入但水位线推进失败（或反之）
- **THEN** 整体回滚，水位线不动；下一轮重试同一批消息，不漏不重

#### Scenario: 用户记忆版本冲突
- **WHEN** 提交时 `WHERE version=?` 影响 0 行（另一实例刚写入）
- **THEN** 重新加载最新记忆并重放本批 ops（最多重试 1 次），最终在最新数据上合并，不覆盖他人写入

### Requirement: 跨会话即时生效
同一用户的所有会话共享同一个 `*UserMemory` 内存实例（UserMemoryHub 按 userName 缓存）。A 会话提取到的条目，B 会话下一轮 `BuildContext` 即可读到。限制：仅单进程内成立，多实例部署时另一实例的内存缓存可能滞后（DB 乐观锁仍保证写入不互相覆盖）。

#### Scenario: 跨会话共享
- **WHEN** 用户在 A 会话建立约束后新开会话 B
- **THEN** B 的第一轮回复即遵守该约束，无需等待 B 自己达到提取阈值

### Requirement: 作为上位规则注入，不被预算裁掉
长期记忆渲染为 `[长期记忆·上位规则]` 段，按 约束→偏好→事实 排序，置于 system 消息最开头、摘要之上；预算截断只裁窗口，不裁长期记忆。`userMemoryEnabled=false` 或未配置 DEEPSEEK_API_KEY 时：已有记忆照常注入，仅不再提取新条目。

#### Scenario: 注入顺序
- **WHEN** 长期记忆与摘要同时存在
- **THEN** system 消息中上位规则在前、摘要其次、窗口消息在后

## 配置（config.toml [memory]）
`userMemoryEnabled=true` / `extractThreshold=6`（生产，开发 4）/ `extractBatch=20` / `userMemoryMaxEntries=30` / `userMemoryEntryMaxChars=100` / `userMemoryMaxChars=1000`

## 关键实现位置
- 领域：`pkg/memory/user_memory.go`（条目/applyOps(写)/Render(读)/commit 冲突重放）、`pkg/memory/extract.go`（NeedExtract/Extract/混合段处理）、`pkg/memory/prompt.go`（extractSystemPrompt/parseOps）
- 存储：`dao/usermemory/usermemory.go`（事务 Commit、双乐观锁）、`model/user_memory.go`
- 接入：`common/aihelper/user_memory_hub.go`（按用户共享缓存）、`common/aihelper/aihelper.go`（maybeExtract）
- 测试：`pkg/memory/user_memory_test.go`
