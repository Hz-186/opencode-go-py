# P0–P9 canonical V2 核心链路实施手册

> 状态：V2-only 规划草案；所有阶段均为 `pending`。本文只规定实施顺序和讨论用代码，不实施运行时。上游基线为 `dev@89130db6b0060a345548d870c51132ee71d6a828`。

## 0. 使用规则与依赖图

每个阶段按 `Red fixture → 最小 vertical slice → canonical V2 differential Green → 重构/故障注入 → DoD 审计` 执行。不得用内存假实现越过权威边界后再“补持久化”；跨阶段接口先落 ADR/Schema，再落实现。任何产品代码不得引入 Session V1 类型、legacy route/table、`SessionPrompt`、`SessionProcessor`、Permission V1 或 Bun/JS compatibility runtime。

```text
P0 ─► P1 ─► P2 ───────────────┐
 │     │     ├─► P3           │
 │     │     └─► P5 ─► P6 ─► P7
 │     └─► P4 ─────────┬──────┤
 │                     └─► P8 ─► P9
 └──────── shared fixtures / evidence ─┘
```

详细设计依赖：[核心运行时](../design/CORE_RUNTIME_BLUEPRINT.md)、[SQLite](../design/DATABASE_BLUEPRINT.md)、[Python Integration Runtime、Provider RPC 与跨语言边界](../design/RPC_AND_TYPES_BLUEPRINT.md)。推荐目录只是实施建议，创建前仍需用 ADR 确认最终模块边界；Python/Provider RPC 不是 P0–P9 或首个 canonical vertical slice 的前置。

## P0 基准冻结与考古设施

### 目标、非目标与证据范围

- 目标：把上游 Commit、包/测试/Schema/API inventory 和本文证据做成可重复生成、可 diff 的 baseline。
- 非目标：不翻译源码、不实现产品、不用 README 推断未读模块。
- 对照：根目录及所有适用 `AGENTS.md`、license、workspace packages、tests、generated schema/OpenAPI、平台脚本。
- 前置：冻结 Commit 可 checkout；明确用户已有文件与只允许修改的目录。

### 推荐目录与逐步任务

```text
internal/baseline/       manifest model、hash、diff
cmd/baseline/            只读生成命令
testdata/baseline/       冻结 manifest 与允许的 generated snapshot
docs/adr/                架构决策记录
```

1. Red：固定 manifest JSON fixture，先证明不同 checkout、dirty tree、缺 submodule 会失败。
2. 记录 commit、branch、version、license hash、package/test/source count、generated artifact hash。
3. 生成 package dependency、public symbol、route/event/schema/test inventory；每条带 source path，并标记 `canonical-v2 / v1-archaeology / shared`。
4. 建 machine-readable Feature Matrix mirror，校验与主计划 Feature ID 一一对应。
5. 建 `baseline diff`：新增/删除/改名/Schema breaking/test drift 分层报告。
6. 建文档绝对源码链接与行号检查器；行号漂移只在升级基线时更新。

```go
type Manifest struct {
	Commit, Version, LicenseSHA256 string
	Packages []PackageRecord
	Schemas  []ArtifactDigest
	Tests    []TestRecord
}
```

### 测试、失败与 DoD

- 测试：同一 checkout 连续生成两次字节相同；脏文件、文件消失、case-only rename、LF/CRLF 均有 fixture。
- 回滚：生成器版本与 manifest 同 commit；失败不得覆盖上一份 baseline，写临时文件验证后原子替换。
- 评审：generated 文件是否计入源码规模；平台 E2E 是否按 case 还是文件计数。
- DoD：一条命令生成相同 hash/包图；Feature、源码链接、许可证台账均可机器校验；状态仍为 `pending`。

## P1 canonical Go Domain、Schema 与 Codec

### 目标、非目标与证据范围

- 目标：建立 DB/Event、Go Domain、HTTP JSON 三层 canonical Schema 及显式 mapper，并冻结未来 MCP/可选 Provider wire 所需的 JSON fixtures。
- 非目标：不建立 Session runner、不让 protobuf 或 Python model 成为 Domain/数据库模型、不为尚未启用的 Worker 生成代码。
- 对照：`packages/schema`、`packages/protocol`、`packages/llm/src/schema` 的 canonical V2 types；不生成 legacy SDK types。
- 前置：P0 manifest；JSON 的 missing/null/number 语义 ADR。

### 推荐目录与逐步任务

```text
schema/json/               canonical JSON fixtures/spec
internal/domain/           Go branded IDs/tagged unions
internal/codec/            DB/HTTP mapper
internal/domain/llm/       canonical LLM request/event/failure
```

1. Red：从上游 canonical 路径提取 ID、SessionMessage、Durable Event、Session Event、Tool、Permission、Usage、ProviderError fixtures。
2. 冻结 Go Domain/JSON compatibility policy；CI 执行 schema breaking check。
3. 实现 canonical JSON：UTF-8、key order、大整数、negative zero、unknown/missing/null 和非法浮点向量。
4. 建立 Go Domain types 与显式 codec，禁止业务包 import OpenAPI/MCP/未来 transport DTO。
5. 为 canonical HTTP tagged union 写双向 codec；未知 event 保留 raw payload 并 fail-safe。
6. 建 TypeScript→JSON→Go Domain→JSON 的 golden roundtrip；跨语言 roundtrip 延后到具体 Python capability 被选中时增量加入。

```text
Upstream JSON fixture ─► HTTP codec ─► Go Domain
                              ▲             │
                              └─ DB/Event codec ◄─┘
```

### 测试、失败与 DoD

- 测试：所有 union variant、可选字段、未知字段、Unicode、媒体、Provider metadata、Usage 不变量。
- 回滚：新增字段只 expand；breaking Domain/JSON change 需要版本 ADR，不可覆盖旧 fixture。
- 评审：JCS 与 JavaScript number 差异；unknown event 是透传、隔离还是拒绝。
- DoD：冻结公共 fixture 字段级差异为 0；生成/codec 可重复；依赖检查证明 Domain 不依赖 protobuf、MCP 或 OpenAPI DTO。

## P2 Go 运行时骨架、错误、日志与资源作用域

### 目标、非目标与证据范围

- 目标：建立 Go 生命周期树、通用受管资源/子进程、取消、日志和错误边界，为 Provider、MCP、PTY/LSP 等统一提供作用域。
- 非目标：不接真实 Provider；不实现 Python/Provider 专用 supervisor、握手或跨语言 transport。
- 对照：上游 Effect Scope/Layer、process/global/log/flag、shutdown 和 signal 测试。
- 前置：P1 Domain/error model。

### 推荐目录与逐步任务

```text
internal/runtime/scope/    context、resource stack
internal/runtime/process/  child process、deadline、kill escalation
internal/telemetry/        redaction、trace、structured log
```

1. Red：fake resource/child 覆盖 crash、hang、取消后输出、重复 close 和 secret log。
2. 实现 root→project→session→turn scope；资源按逆序、有 deadline 地关闭。
3. 通用 process manager 使用显式 argv/env/cwd/stdio limit 拉起 child；关闭执行 graceful→kill escalation。
4. 实现有界 sink 与 backpressure primitive，供 Provider HTTP、MCP 和 PTY 复用。
5. signal 只触发一次 shutdown；第二次强退但写明 operational 状态。
6. 结构化日志统一 trace/session/turn/attempt，不记录 prompt、key、raw header。

```go
defer scope.Close(shutdownCtx)
child, err := process.Start(ctx, launchSpec)
defer child.Close(shutdownCtx)
```

### 测试、失败与 DoD

- 测试：10,000 次 scope、1,000 次 child restart；race detector；FD/process 泄漏；慢消费者；signal escalation。
- 回滚：resource 注册失败只关闭本次创建项；通用 process manager 不维护业务 generation。
- 评审：grace period、process group/job object、日志截断和 backpressure primitive。
- DoD：scope/child 全链路可取消/限流/清理；fixture secret scan 为 0；不需要 Python 即可通过。

## P3 Config、Auth、Project、Workspace 与 Worktree

### 目标、非目标与证据范围

- 目标：复刻配置解析/合并、认证引用、项目实例作用域、workspace/worktree 和路径语义。
- 非目标：任何可选 integration 不自行扫描宿主 config/环境变量；本阶段不调用真实模型或启动 Python。
- 对照：`packages/core/src/config`、auth、project、workspace、worktree、Instance/Location 及测试。
- 前置：P1 类型、P2 scope/log；macOS case-insensitive 与 Linux case-sensitive fixture。

### 推荐目录与逐步任务

```text
internal/config/  internal/auth/  internal/project/
internal/workspace/  internal/worktree/  internal/platform/pathx/
```

1. Red：同一目录树在原版输出 resolved config，覆盖 global/project/custom/inline/managed 和环境替换。
2. 分层 parse→validate→merge→resolve；保留来源/provenance，错误指向文件与字段。
3. auth 只返回 `SecretRef/CredentialLease`；日志/DB 禁止明文 credential。
4. project identity 处理 git/non-git、symlink、中文、空格、大小写与多工作区。
5. instance cache 绑定 scope，配置变更使用 generation fencing，旧 Tool/Provider snapshot 不混用。
6. worktree create/remove 先预检路径与 dirty 状态；失败做补偿并保留可诊断记录。

```go
type ResolvedConfig struct { Value Config; Sources []SourceRef; Generation uint64 }
type ProjectContext struct { ID ProjectID; Root Path; Config ResolvedConfig }
```

### 测试、失败与 DoD

- 测试：config merge vectors、权限/坏 JSON、symlink loop、case-only path、并发 reload、git worktree fault。
- 回滚：配置 reload 失败继续用最后有效 generation；worktree 只删除本次创建且已验证的路径。
- 评审：MDM/managed precedence、keychain 后端、project ID 跨 clone 稳定性。
- DoD：resolved config 与上游 fixture 100%；三平台路径矩阵；未来 integration snapshot 可由 Go 最小化生成且默认不产生。

## P4 Event Store、SQLite 与 Projector 基础

### 目标、非目标与证据范围

- 目标：完成单 writer、migration、Durable Event、同步 Projector、replay/subscription 与完整性检查。
- 非目标：不把 SSE 当 durable queue；不让 Python、MCP server、Provider adapter 或 HTTP handler 绕过 command transaction 写表。
- 对照：`core/src/event.ts`、`event/sql.ts`、database migration/schema、projector tests。
- 前置：P1 Event codec、P2 scope；采用 [SQLite 详细设计](../design/DATABASE_BLUEPRINT.md)。

### 推荐目录与逐步任务

```text
internal/store/sqlite/  migrations/
internal/event/         registry、publisher、replay、subscription
internal/projector/     sync projector、rebuild
cmd/dbtool/             check、backup、replay（开发/维护）
```

1. Red：移植 Event transaction、sequence、projector failure、handoff、slow subscriber 测试。
2. 实现 PRAGMA/connection policy 和 checksum migration；启动前执行 schema version/完整性预检。
3. `BEGIN IMMEDIATE` 内 claim sequence→insert event→同步 projector→commit hook；commit 后 publish。
4. history/live handoff 以 aggregate seq 为边界，禁止时间戳排序；慢订阅者有界隔离。
5. 实现 replay/rebuild 到影子表，校验 hash/count/max seq 后切换。
6. fault injection 覆盖每个 SQL/commit/publish 点和 kill -9 恢复。

```sql
BEGIN IMMEDIATE;
-- claim aggregate sequence, append event, run all synchronous projectors
COMMIT;
-- only now notify live subscribers
```

### 测试、失败与 DoD

- 测试：10M synthetic events、10k property seeds、WAL crash、disk full、busy、migration downgrade 拒绝。
- 回滚：migration 先备份且校验；projector rebuild 影子切换；event append 失败整体 rollback。
- 评审：busy timeout、blob 阈值、projector schema version 与备份保留策略。
- DoD：序列无洞/重复；无半投影；replay snapshot 一致；SQLite writer 静态依赖只在 Go store。

## P5 Go Tier-1 Provider 与 LLMEvent

> ADR-0001：P5 以 fake `ProviderPort` 为测试底座，并由 Go 实现三种 Tier-1 adapters；Provider RPC 只允许在 P14+ 经独立 ADR 批准后实现。

### 目标、非目标与证据范围

- 目标：冻结 `ProviderPort` 与 `ProviderTurnRequest/LLMEvent` canonical 边界，用 Go 将三种 Tier-1 native request/stream 无损映射为 canonical request/16 类事件。
- 非目标：Provider adapter 不结算 Tool、不决定 Session retry/compaction、不保存 checkpoint；Python、Protobuf 和 gRPC 不进入本阶段依赖图。
- 对照：`packages/llm` canonical routes/protocols/provider、catalog/auth/transform tests；legacy AI SDK adapter 只作行为考古。
- 前置：P1、P2；P3 resolved credential/config；采用主计划与 ADR-0001 的 `ProviderPort` 边界。

### 推荐目录与逐步任务

```text
internal/provider/{port,catalog,transport,normalize}/
internal/provider/{openai,anthropic,compatible}/   # Go Tier-1 adapters
test/provider/cassette/                            # 脱敏记录与确定性回放
python/experiments/providers/                      # L3 对照实验，不进生产
```

1. 先建 fake Provider 底座（脚本化 cassette），Red 测 Runner/状态机；直连 API 后再补轻量脱敏记录。
2. 实现 model/route/capability 解析与 raw request preview；未支持组合 fail-fast。
3. 按 text/reasoning/tool-input block 状态机构造事件；metadata/usage 保留 escape hatch。
4. 在 Go 实现 transport retry，仅对白名单 status/未开始语义流的 attempt 生效并报告 diagnostic。
5. 分别实现 OpenAI Responses、Anthropic Messages 和 OpenAI-compatible 的 Go request/framing/normalize adapter；每个 adapter 禁用 SDK 隐式 retry/fallback，保留 usage/cache/provider metadata。（V2 native 上游仅支持三种 API type，见主计划 8.1；Gemini/Bedrock 不进入 canonical P5。）
6. Go stream validator 拒绝非法 block/tool 状态、重复 ID、terminal 后 event、Usage 不变量破坏。
7. 对照实验（L3，可选）：用 Python 官方 SDK/PydanticAI 重写一次 adapter 的 normalize，与 Go 简化直连对比 metadata/流事件形状损耗。

```go
func (p *OpenAIResponsesProvider) RunTurn(ctx context.Context, request ProviderTurnRequest, sink LLMEventSink) error {
    // Project request, stream one provider turn, normalize chunks; Tool settlement remains in Runner.
}
```

### 测试、失败与 DoD

- 测试：fake 驱动闭环；直连 API 的 malformed chunk、disconnect、429/Retry-After、401、overflow、tool JSON fragments、cancel、metadata。
- 回滚：Provider capability 可逐个关闭；不自动落回语义不同的 protocol/model。
- 评审：原始 metadata 保留期限、Provider-side tool、retry 计费歧义和 cassette 分发限制。
- DoD：fake Provider 闭环稳定；直连 API 的流式 text/reasoning/tool call 可用；未知 route 清晰失败；无 secret 泄漏。

## P6 Tool、Permission 与 Question

### 目标、非目标与证据范围

- 目标：Go Tool Registry、built-ins、单一 canonical Permission、Question、输出投影和 durable settlement。
- 非目标：Python 不执行 canonical 文件/Shell/PTY 等本地 built-in Tool；permission 不以 UI 是否在线为前提；不默认信任模型路径/命令或 integration schema。
- 对照：Tool registry/built-ins、Permission schemas/services、Question、tool state tests。
- 前置：P3 project/config、P4 event、P5 Tool Call wire。

### 推荐目录与逐步任务

```text
internal/tool/{registry,builtin,settlement}/
internal/permission/  internal/question/  internal/sandbox/
```

1. Red：建立 definition/input/output/error、permission vector、取消/race/stale generation fixtures。
2. Registry snapshot 带 generation/hash；Runner 每 turn 冻结 definitions，reload 不改变进行中 call。
3. Tool Call 先 durable `pending`；参数 schema 校验后进入 permission leaf evaluation。
4. ask 状态持久化并可由任意客户端答复；once/session/project rule 有明确作用域和过期。
5. executor 运行在受控 scope，输出/媒体限额；success/error/cancel 都写 terminal settlement。
6. Question 复用 durable request/reply 生命周期但不与权限规则混为一表。
7. 冻结语言无关 `ToolExecutor` 约定，使 P7 MCP/Python connector 只能替换 execution leaf，不能替换 registry、permission 或 settlement。
8. 对照实验（L3）：用 PydanticAI 写工具 schema 验证原型，与自研实现对比，产出"为什么先 durable 记录再执行副作用不能被框架工具节点替代"的说明。

```text
pending → validating → permission(allow|ask|deny) → running → completed|failed|cancelled
```

### 测试、失败与 DoD

- 测试：路径穿越、symlink race、命令注入、TOCTOU、重复答复、断线重连、输出炸弹、cancel。
- 回滚：注册 reload 失败保留旧 generation；Tool 副作用不可假回滚，需标明 idempotency/compensation。
- 评审：sandbox 平台边界、session permission 缓存、MCP/custom Tool 信任等级。
- DoD：核心 Tool diff 100%；每个 Call 恰有一个 terminal；拒绝/取消后无进程/文件句柄泄漏。

## P7 Skill、MCP 与 Python Integration Reference

### 目标、非目标与证据范围

- 目标：Skill discovery/load/watch 与 MCP stdio/HTTP/SSE/OAuth、resource/prompt/tool 动态目录；用一个 Python Wiki/Web reference connector 证明 Python 库可在不分叉 Agent Core 的前提下接入。
- 非目标：MCP/Python server 不直接写 DB、不拥有 Permission/settlement/Agent Loop；新增 Wiki 等 connector 不伪装成上游 canonical 功能；core 测试不依赖 Python。
- 对照：Skill loader、MCP client/auth/event/tool conversion 及 mock server tests。
- 前置：P3 config/project、P6 registry/permission；P2 resource scope。

### 推荐目录与逐步任务

```text
internal/skill/  internal/mcp/{client,transport,oauth,catalog}/
internal/integration/{manifest,capability,supervisor,lease}/
python/tools/knowledge/     # 可选 Wiki/Web reference MCP server
test/mcpserver/  testdata/skills/
```

1. Red：本地/远程 Skill 优先级、坏 frontmatter、watch race；MCP reconnect/OAuth/slow shutdown fixtures。
2. Skill source→discover→validate→content hash→snapshot；Prompt 只引用 frozen snapshot。
3. MCP connection 归 project scope；stdio child、HTTP/SSE stream、OAuth token 都有明确 close/refresh。
4. 动态 Tool 名称冲突、schema 变化使用 registry generation fencing；调用仍走 P6 permission。
5. resource/prompt 内容带 provenance/trust label，注入 context 前做大小和媒体限制。
6. OAuth callback 校验 state/PKCE/redirect/origin；token 进入 credential store 而非普通 config。
7. 定义 Python integration manifest：name/version/entrypoint/schema/capability/network/files/credential/resource limits/dependency lock hash。
8. Go 在配置启用且首次 discovery/call 时懒启动 Python MCP server；用户不手动启动第二后端，禁用时不创建 Python 进程。
9. reference connector 至少实现 `wiki.search`/`wiki.fetch` 或等价 Tool/Resource；输出携带 source URL/revision/retrieved_at/provenance，并遵守大小、超时和内容信任标签。
10. Python 只接收结构化参数、受限临时目录和短期 credential lease；任意 URL、宿主路径、全量环境变量和长期 token 默认拒绝。

### 测试、失败与 DoD

- 测试：malformed JSON-RPC/binary、server/Python crash、token refresh race、duplicate tool、remote timeout、prompt injection label、依赖缺失、输出炸弹、未授权域名/路径。
- 回滚：单 MCP/Python connector 熔断不拖垮 Session；catalog 保留最后 generation 但不可执行失联 Tool；删除可选 profile 不改变 DB schema。
- 评审：remote Skill 信任/签名、MCP roots/sampling、OAuth 多账户、Python runtime/lock 格式和 connector capability 粒度。
- DoD：冻结 canonical MCP/Skill suite 100%；Go-only profile 完整通过；Python reference connector contract/fault suite 通过；关闭后无 child/socket；动态 schema 不污染进行中 turn；新增 connector 与 canonical 完成率分开记账。

## P8 Agent、Subagent、System Context 与 Scheduler

### 目标、非目标与证据范围

- 目标：Agent resolution、prompt inbox、steer/queue、每 Session coordinator、子 Session、后台任务和 context epoch。
- 非目标：不把框架 checkpoint 当权威；不允许同 Session 两个 Runner 并行。
- 对照：Agent/Input/RunCoordinator/Execution/background/System Context 及 race tests。
- 前置：P3 project、P4 event、P6/7 catalog；核心状态机设计已冻结。

### 推荐目录与逐步任务

```text
internal/agent/  internal/session/{inbox,coordinator,context}/
internal/subagent/  internal/scheduler/
```

1. Red：deterministic model 覆盖 concurrent wake/join、steer cutoff、queue FIFO、cancel/restart、parent/child。
2. Session create 发布 canonical `SessionCreated` 并投影 Session；不得发布或映射 `SessionV1.Event.Created`。
3. Prompt admission 与 wake 分离：先事务持久化、幂等判定，再通知 coordinator。
4. coordinator 按 Session key 合并 wake；不同 key 受全局/Provider/tool semaphore 控制并行。
5. steer 批量提升 cutoff 内输入，queue 每轮只提升最早一个；sequence 来自 Event，不用 wall clock。
6. child Session 有 parent/depth/budget/cancel policy；后台完成以 durable event 回注父 Session。
7. System Context 用 epoch/baseline sequence；unavailable/replace/refresh 都显式事件化。
8. 对照实验（L3）：用 Eino ADK 的 ReAct agent 对照自研 steer/queue runner，产出"ReAct 隐式循环 vs 显式 inbox 状态机"行为差异说明。

```go
func (c *Coordinator) Wake(ctx context.Context, id SessionID) (Execution, error)
func (i *Inbox) Admit(ctx context.Context, cmd AdmitPrompt) (Admission, error)
```

### 测试、失败与 DoD

- 测试：10k scheduler seeds、wake storm、父子级联/隔离取消、预算耗尽、context unavailable/replay。
- 回滚：scheduler policy 版本化；已发布 canonical Event replay 不依赖新 wall-clock 决策。
- 评审：后台代理资源配额、父子取消默认值、最大深度、fairness。
- DoD：同 Session 串行、不同 Session 可并行；admitted input 不丢失/重复；重启后可继续 drain。

## P9 完整 Session Runner、Compaction 与 Snapshot

### 目标、非目标与证据范围

- 目标：使用唯一 `Session + Execution + Runner` 闭合 prompt→Provider→Tool→下一 turn→finish，并覆盖 retry/overflow/compaction/structured output/snapshot/revert。
- 非目标：不实现 `SessionPrompt`/`SessionProcessor`；不通过“重发最后请求”猜测恢复点；不在 Python/MCP/Provider adapter 保存 Session checkpoint。
- 对照：canonical V2 runner、compaction、retry、overflow、snapshot/revert 和 Session tests；V1 测试只提取经 `adopt` 评审的缺失行为向量。
- 前置：P4–P8 全部；采用核心、DB 和 `ProviderPort`/integration boundary 三份详细设计。

### 推荐目录与逐步任务

```text
internal/session/{runner,execution,recovery}/
internal/compaction/  internal/snapshot/  internal/revert/
internal/provider/profiles/{summary,title,structured}/
```

1. Red：无 Tool vertical slice 的 durable transcript；随后逐项加入 Tool、多 turn、steer/queue、interrupt/crash。
2. Runner 每轮重载 durable history/catalog/context snapshot，构造带 hash 的完整 Provider Turn；通过 `ProviderPort` 调用 fake 或 Go Tier-1 实现。
3. live delta 只发有界 hub；终态 block/step/tool/usage 在 Event transaction 持久化。
4. retry 依据 typed failure/attempt/是否已有副作用；overflow 先生成 compaction command，再用新 epoch 重试。
5. structured output 以 schema/hash 验证；失败不伪装 text success。
6. snapshot 记录文件基线/patch provenance；revert 发 durable command 并重建 canonical 投影。
7. 启动 recovery 扫描非终态 attempt/tool/input，按明确规则 resume、retry 或 fail，不重复副作用。
8. 对照实验（L3）：用 LangGraph 跑同一批 Session 输入，记录 checkpoint 与 durable event 的恢复差异，产出"event sourcing 让恢复/审计/投影天然一致"的说明。

```text
drain input → build request → stream frames → persist terminal blocks
           → settle tool? ─yes─► reload history ─┐
           └─ finish/compact/retry/stop         └─ next turn
```

### 测试、失败与 DoD

- 测试：上游 canonical V2 suite、已采纳行为向量、Go Provider transport failure、Go kill -9、mid-tool cancel、可选 Python MCP crash、overflow loop、snapshot conflict、replay hash。
- 回滚：新 Runner policy feature-flag + event version；已发布 canonical Event 始终可 replay；文件 revert 冲突 fail closed。
- 评审：live delta 丢失后的 UI 表现、重试计费、compaction fidelity，以及 V2 尚缺行为的 `adopt / redesign / reject` 决策。
- DoD：核心 Session differential suite 100%；任何终止后无 running tool/inbox stranded；同 prompt durable event log 等价。

## 10. P0–P9 联合验收

第一个可演示的核心候选必须在全新、没有 Python runtime 的临时目录完成：加载 canonical 配置→建立 Project/Session→admit prompt→fake Provider stream（真实 Provider 直连为可选项）→permission→Go built-in Tool→下一 turn→finish→停止进程→重启→replay 同一 transcript。随后再以可选 profile 接入 Python Wiki/Web MCP connector，证明扩展可增量启用且不改变核心 transcript。验收同时要求：

- SQLite integrity/replay 通过，Python/MCP 无 DB 访问路径。
- Go canonical fixture、request/event hash 可重复；启用 Python connector 时其 manifest/lock/schema/contract hash 另行可重复。
- 中途取消、Go kill -9、Python MCP connector crash 三种适用恢复路径都有 transcript 与资源泄漏证据；Python crash 不影响 Go-only 路径。
- 所有阶段 review 问题已有 ADR 或明确 owner/deadline；未解决项仍为 `pending/blocked`，不得标 `verified`。
