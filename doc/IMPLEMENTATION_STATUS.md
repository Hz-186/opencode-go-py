# 实施状态

> 长期目标：以 `dev@89130db6b0060a345548d870c51132ee71d6a828` 为冻结基线，完成 OpenCode canonical V2-only 的 Go Core 与可选 Python Integrations。本文记录可恢复的执行状态；主计划仍是范围与 Definition of Done 的唯一总入口。

## 当前约束

- Go 是 Session、Execution、Coordinator、Runner、Event、SQLite、Tool、Permission 和恢复逻辑的唯一权威拥有者。
- 首个里程碑必须是 Go-only + fake ProviderPort + SQLite/Event/Projector 的 durable vertical slice。
- Python、Protobuf 和 Provider RPC 不得成为 P0–P9 首个 vertical slice 的前置。
- 上游仓库只读；目标仓库中的 `.idea/`、`.DS_Store` 和用户已有无关改动不处理。

## 当前阶段 TODO

| ID | 任务 | 状态 | 证据/下一步 |
|---|---|---|---|
| P0-01 | 冻结 Git baseline manifest、hash 与生成命令 | verified | `p0-audit` 连续两次生成字节相同；6358 tracked、718 test/spec、40 package manifests、36 workspaces |
| P0-02 | machine-readable Feature Matrix mirror | verified | 主计划 65 行一一对应；64 canonical、1 replica-extension，状态枚举受检 |
| P0-03 | ADR 模板与首批架构 ADR | verified | ADR-0001 固化 Go Core/Tier-1 Provider；ADR-0002 固化保守 scope policy |
| P0-04 | 许可证台账 | verified | 冻结 `bun.lock` 共 3221 个 resolution、0 unknown source；283 个外部依赖由 393 条逐版本证据覆盖，269 个依赖全部 verified，14 个依赖保守 unresolved 且均有机器可审计原因 |
| P0-05 | 文档源码链接与冻结行号检查器 | verified | 全部 `doc/**/*.md` 链接通过 frozen-commit `git cat-file` 校验 |
| P0-06 | package/test/source/schema/API inventory 与 diff | verified | 冻结 tree 语义提取已覆盖 188 routes、89 events、37 root schema exports、744 public symbols；0 unresolved route；11 个 legacy 局部 endpoint ID 重名均带源位置留痕；semantic self-diff 为 0 |

P0 整体状态：`verified`。P0-01～P0-06 均满足阶段 DoD。

| ID | 任务 | 状态 | 证据/下一步 |
|---|---|---|---|
| P1-01 | branded ID、Location 与 Sessions cursor | verified | 冻结前缀、ascending/descending ID layout、无填充 base64url cursor 向量与 directory/project 互斥规则均有正负向测试 |
| P1-02 | canonical JSON 与 optional policy | verified | `p1-canonical-json-v1`：UTF-8、重复键、最大深度 100、稳定 key order、大整数、`-0`、missing/null、未知字段与非法浮点策略已冻结 |
| P1-03 | canonical LLM Domain/Codec | verified | Go Domain 拥有 Request/Message/Content/Usage/Event/Failure；5 类 Content（含 media）、16 类 LLMEvent、10 类 Failure 全覆盖；二进制 media 保留在 Domain/P5 lowering，不伪造 JSON wire |
| P1-04 | SessionMessage 与 Tool state | verified | 8 类消息、3 类 assistant content、4 类 tool state、Prompt attachment、Model.Ref、snapshot/token/error/time 全部 deterministic round-trip |
| P1-05 | Event envelope 与 Session Event | verified | `id/type/data/durable/location/metadata` envelope、32 类 Session Event、28 durable/4 live-only、v1/v2 definition registry 与 unknown raw 保真均通过 |
| P1-06 | Permission 与 Question | verified | V2 request/reply/ruleset、source/tool、Unicode、metadata 大整数及 optional/unknown 负向矩阵通过 |
| P1-07 | public golden、schema drift 与依赖边界 | verified | 80 项 frozen-source fixture 字段级深等价为 0；manifest/SHA-256 可重复；Domain dependency graph 不含 protobuf、gRPC、MCP 或 OpenAPI DTO |

P1 整体状态：`verified`。公共 fixture 位于 `schema/json/p1-canonical-fixtures.json{,.sha256}`，SHA-256 为 `1e8500b26f4b5ceb58cb9778c68b0e4d115c2b05ea5c614be2b24ce6d351f580`。Feature Matrix 中还依赖 P4/P5/P6 的跨阶段整行保持原状态，不提前冒充完成。

| ID | 任务 | 状态 | 证据/下一步 |
|---|---|---|---|
| P2-01 | root→project→session→turn 生命周期树 | verified | 父取消逐级传播；child 作为资源注册；资源严格逆序关闭；Close 并发幂等；关闭后注册只回滚当前资源 |
| P2-02 | 通用受管子进程 | verified | 显式 argv/env/绝对 cwd；stdout/stderr 与 combined output 有界且标记 truncation；typed start/exit/cancel/timeout/wait failure；TERM→进程组 KILL；Wait 与 lifetime 取消分离 |
| P2-03 | 有界 sink/backpressure | verified | 慢消费者真实阻塞生产者；Send/Receive 均支持 context；Close 唤醒阻塞双方并允许排空已接收 buffer；并发性质测试无丢失/重复 |
| P2-04 | signal escalation 状态机 | verified | 第一次 observation 唯一触发 graceful shutdown，第二次及以后进入 force operational state；64 路并发只有一个 shutdown winner |
| P2-05 | 结构化日志与脱敏 | verified | JSON log 统一 trace/session/turn/attempt；prompt/API key/Authorization/raw headers/token/password/cookie 及嵌套 map/slice/error 在 handler 写入前脱敏；secret fixture scan 为 0 |
| P2-06 | 压力、race 与泄漏门禁 | verified | 10,000 次 scope tree、2,000 资源 register/close race、1,000 次真实 child run、后代进程组清理、goroutine/FD 基线、慢消费者、signal escalation 全部通过 |

P2 整体状态：`verified`。P2 包联合 statement coverage 为 90.4%；`process/scope/shutdown/sink/telemetry` 分别为 88.4%/87.9%/100.0%/91.3%/94.4%。

| ID | 任务 | 状态 | 证据/下一步 |
|---|---|---|---|
| P3-01 | Config parse/validate/merge/resolve 与 provenance | verified | 冻结上游合同矩阵覆盖 remote→global→custom→project→directory→inline→organization→managed→managed preference 全部 9 层；JSONC、显式 env/file 替换、顶层字段/枚举、错误 stage/source/field、失败保留最后有效 generation 与 64 路并发 reload 均通过 |
| P3-02 | Auth secret reference 与短期 lease | verified | Store 只公开 `SecretRef`/metadata；lease clone、expiry、replacement/delete 内存清零、context 取消、并发 lease、全部 renderer/JSON 脱敏均通过，statement coverage 100.0% |
| P3-03 | Path 与 Project identity | verified | Darwin/Linux/Windows portable matrix（含 UNC）、显式大小写模式、symlink loop、中文/空格路径、git/non-git/empty repo、root commit/normalized remote/linked worktree 稳定 ID 均通过；可选 Git probe 仅忽略 typed `ExitFailure`，cancel/timeout/start/wait failure 保持 `errors.Is`/`errors.As` |
| P3-04 | Workspace instance cache 与 generation fencing | verified | 并发 load 共享 in-flight boot；取消 waiter 不取消共享 boot；取消 dispose 不再 orphan in-flight entry；失败重试、reload fencing、旧 snapshot 不变、逆序 scope close 与深 clone 均通过 |
| P3-05 | Worktree create/list/remove/reset | verified | 真实 Git lifecycle、slug/branch、dirty/force、managed-root containment、primary/unowned/symlink/path escape 防护、case mode、失败补偿、取消后独立有界验证/清理、并发 create/remove 与重复 fault 的 goroutine/FD/runner residue 均通过 |
| P3-06 | Go-only integration boundary | verified | Config/Workspace 仅消费显式 Go 输入并产出 immutable-by-copy snapshot；不会扫描 integration runtime 环境，仓库当前无 Python、Provider RPC 或 integration snapshot 默认产物 |

P3 整体状态：`verified`。P3 六包联合 statement coverage 为 88.2%；`config/auth/pathx/project/workspace/worktree` 分别为 94.7%/100.0%/87.5%/82.1%/93.2%/79.2%。当前实施入口转入 P4 Event Store、SQLite 与 Projector 基础；长期 P0–P17 goal 保持 active。

| ID | 任务 | 状态 | 证据/下一步 |
|---|---|---|---|
| P4-01 | Durable Event transaction core | verified | `internal/event` 已实现 projector→local commit→sequence/event 原子 Store closure→commit 后通知；同 aggregate 100 路并发序列无洞；exact `type.version` definition registry 冻结 aggregate field；observer panic/数据变异隔离且 typed 上报；Event 事务各读写点与最终 commit fault 均保持零半状态 |
| P4-02 | Replay、owner fencing 与 subscription | verified | exact replay 幂等且不重复 projector/通知；`ReplayAll` 对 aggregate/sequence/definition 整批预检后支持后续 chunk；claim/transfer fencing 与 remove/restart 已覆盖；typed/wildcard 有界订阅、history/live handoff、gap/corrupt/cancel/wake cleanup 均受检 |
| P4-03 | Versioned migration catalog | verified | embedded `000001_event_store.sql` 只含 canonical `schema_migration/event_sequence/event` 与索引；catalog ID/order/UTF-8/LF/SHA-256 稳定；apply orchestration 校验 checksum/downgrade，原子失败只保留精确前缀并可恢复续跑 |
| P4-04 | SQLite connection/writer/migration apply | verified | 真实 `modernc.org/sqlite` 已接入标准库 `database/sql`；独占 writer 显式执行 `BEGIN IMMEDIATE`，并发 exact migration 不重放/不重复计数，caller cancel 后用独立有界 context rollback；连接 policy 设置并回读验证 FK/busy timeout/temp memory/WAL/NORMAL&#124;FULL；真实 projector rollback、busy、disk-full、read-only、WAL kill -9 fault 均通过 |
| P4-05 | Integrity、import/rebuild 与容量门禁 | verified | 真实 quick/FK/sequence/payload integrity check、canonical replay import、shadow projector rebuild/rollback、online backup/manifest、10k property 与 10M Event count/min/max + history p95 均通过；replay rebuild projection snapshot 与 live projection 字节级一致 |

P4 整体状态：`verified`。P4-01～P4-05 已满足阶段 DoD；真实 SQLite/Event 套件通过定向 `count=10`、race `count=3` 与 vet，联合 statement coverage 为 77.5%（`internal/event` 86.3%、`internal/store/sqlite` 73.0%）。Feature Matrix 中依赖后续 P6/P8/P10 的跨阶段行仍保持原状态，不提前冒充完成。

| ID | 任务 | 状态 | 证据/下一步 |
|---|---|---|---|
| P5-01 | fake ProviderPort 与脚本化 cassette | verified | `internal/provider` 已冻结 `ProviderTurnRequest`、有界 `LLMEventSink` 与 `ProviderPort`；fake cassette 按 provider/model/route 精确匹配，事件经 canonical codec round-trip 深拷贝，支持取消、sink/backpressure failure、failure-only turn 与安全调用记录；focused `count=10`、race `count=3` 与 vet 通过 |
| P5-02 | model/route/capability catalog 与 raw request preview | verified | `Catalog` 精确解析三种 canonical API type（OpenAI Responses、Anthropic Messages、OpenAI-compatible），未知 route/API、重复 route 和缺失 capability typed fail-fast；`RequestPreview` 仅输出消息/内容形状、工具数及 header/option 名称，canonical JSON deterministic 且 fixture secret 不泄漏；focused `count=10`、race `count=3` 与 vet 通过 |
| P5-03 | text/reasoning/tool-input block stream state | verified | `StreamValidator` 覆盖 16 类 canonical LLMEvent 的 terminal、step、text/reasoning start→delta→end、tool input→call→result/error 状态；duplicate ID、错序、terminal tail、未知 event、Usage/metadata 不变量均 typed 拒绝；focused `count=10`、race `count=3` 与 vet 通过 |
| P5-04 | bounded transport retry 与 diagnostic | verified | `RunWithRetry` 仅重试白名单 status（408/409/425/429/500/502/503/504）且 `StreamStarted=false` 的 `AttemptError`；Retry-After/指数 backoff 有上限且可取消，每次重试报告无 secret diagnostic；focused `count=10`、race `count=3` 与 vet 通过 |
| P5-05 | 三种 Tier-1 native adapter | verified | Go 已实现 OpenAI Responses、Anthropic Messages、OpenAI-compatible 的 deterministic request projection、HTTP/SSE framing 与 canonical `LLMEvent` normalize；system/message/tool/tool-choice/generation/JSON 与 tool response format/provider/HTTP options 均受检，`ResponseFormatTool` 投影为真实 native tool/schema 并强制 named choice；text/reasoning/tool fragments、usage/cache/provider metadata、malformed/disconnect/terminal tail/401/429/overflow/sink/cancel、exact route/API/capability、短期 credential lease、secret redaction 与禁用 redirect/fallback 均有 focused 证据 |

P5 整体状态：`verified`。三种 adapter 复用 canonical `ProviderPort`、`Catalog`、`StreamValidator` 与 `LLMEvent`，仅执行单次 native HTTP attempt；retry 仍由 `RunWithRetry` 独占。P5 未引入 Python Provider、Protobuf/gRPC、Tool settlement 或 Runner retry/compaction，依赖 P6/P8/P9/P10 的 Feature Matrix 行保持原状态。

## 最近验证

- P5-05 最终门禁：`CGO_ENABLED=0 go test ./internal/provider/... -count=10`、`go test -race ./internal/provider/... -count=3`、`go vet ./internal/provider/...`、`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`test -z "$(gofmt -l cmd internal)"` 与 `git diff --check` 全部通过；三种 adapter 的 401/credential/HTTP option 与 in-flight cancel 合同由同一跨 adapter suite 验证。
- P4 driver-independent 门禁：`internal/event` 的 publish/replay/replayAll/claim/remove/subscription/handoff/fault suite，以及 `internal/store/sqlite` migration catalog/apply/`BEGIN IMMEDIATE`/rollback/PRAGMA policy 均通过 `count=10`、race `count=3`、vet；脚本化 SQL 只证明调用合同，不替代真实 SQLite fault 证据。随后全仓普通/race/vet/format/diff checks 全部通过。
- P4 最终门禁：真实 `modernc.org/sqlite` 的 `internal/event` + `internal/store/sqlite` 通过 `go test -count=10`、`go test -race -count=3`、`go vet`；10,000-seed sequence property 通过；`BenchmarkRealStoreHistoryTenMillionEvents` 在 10,000,000 条真实事件上验证 `count=10,000,000/min=0/max=9,999,999`，20 次 history page 的 p95 为 `0.1660 ms`（门槛 20 ms）；WAL kill-9、disk-full、busy、read-only、migration/projector rollback、canonical import、online backup 和 replay rebuild/live projection byte-exact snapshot 均通过。
- P5-01 定向门禁：`internal/provider` fake cassette contract 通过 `count=10`；覆盖 exact model/route mismatch 不消费 cassette、canonical LLMEvent stream、codec 深拷贝、attempt 记录、deadline cancellation 与 sink error wrapping；Provider 包不依赖 SQLite、Python、Protobuf 或 gRPC。
- P3 回归修复：worktree 的 case-insensitive 模式现在分离比较 key 与保留实际大小写的 Git operation path；`TestCaseModeControlsCaseOnlyRemoval` 与 canceled optional Git probe 各通过 `count=10`，随后全仓普通/race/vet/format/diff 门禁重新通过。
- P3 最终门禁：`go test ./internal/project -count=10`、P3 六包联合 coverage/race/vet、`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`gofmt -l cmd internal` 与 `git diff --check` 全部通过。
- Project optional Git probe 回归同时覆盖 remote 与 root-commit：普通 Git 非零退出保持 fallback；deadline/cancel 与 start/wait 等运行期失败不再降级成 global project。
- 通用 process 的 timeout/cancel 测试先等待 child readiness，并以有界 deadline 验证 TERM→KILL，消除了 shell trap 尚未安装造成的调度型 flake；focused `count=10`、race `count=3`、package `count=3` 均通过。
- P2 定向门禁：`go test ./internal/runtime/... ./internal/telemetry -count=10`、对应 `-race` 与 `go vet` 全部通过；coverage profile 位于 `/tmp/opencode-go-py-p2-coverage.out`。
- P2 fault suite 已验证 cooperative TERM、忽略 TERM 后 KILL、进程组后代消失、取消后有界输出、并发 Close、关闭 deadline、注册回滚与 secret scan；通用 process package 不含 generation/supervisor 业务状态。
- P1 最终门禁：`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`gofmt -l cmd internal`、`git diff --check` 与 `p1-fixtures -verify` 全部通过。
- 全仓 `go test ./... -count=10` 连续通过；JSON/SessionMessage/SessionEvent 三个 fuzz target 短跑合计约 44 万次输入无失败。
- P1 联合 suite 使用 `-coverpkg=./internal/domain/...,./internal/codec,./internal/p1fixture` 的 statement coverage 为 85.1%；coverage profile 位于 `/tmp/opencode-go-py-p1-coverage.out`。
- 80 项 public golden 在 source JSON 与 canonical JSON 之间执行 `domain.JSONValue` 字段级深等价；生成器连续输出一致，当前 SHA-256 为 `1e8500b26f4b5ceb58cb9778c68b0e4d115c2b05ea5c614be2b24ce6d351f580`。
- 带真实冻结上游的 `go test ./... -count=1`、`go test -race -count=1 ./...` 与 `go vet ./...`：最近一轮通过；`internal/baseline` 覆盖率为 79.3%。
- `license-evidence` 从冻结 Git tree 解析 JSONC `bun.lock`，用 8 路有界并发读取官方 `registry.npmjs.org` 精确版本元数据；只有 registry name/version/`dist.integrity` 与 lock 完全一致才标记 verified。串行与并发生成的证据字节/hash 相同。
- 393 条证据中 378 条 registry 记录 verified；其余为 10 条未安装/无 lock resolution、2 条 vendor file、1 条 Git、1 条 URL snapshot 与 1 条 registry 未声明 license。它们保留 `unresolved_reason`，不猜测许可证。
- `p0-audit` 单命令生成 baseline、Feature Matrix、bun lock inventory、license evidence/ledger、source-link、semantic inventory 与 semantic self-diff；8 个 artifact 的 bundle 作为最后写入的完整性 commit marker。
- `semantic-inventory` 与 `semantic-diff` 可独立生成原子 JSON/SHA-256；语义 diff 区分 added、removed、changed 与 breaking，重复自然键不会静默覆盖。
- 真实冻结仓库生成：`tracked_files=6358`、`test_files=718`、`version=1.18.11`、generated tree/artifact 均为 33。
- 当前 P0 bundle 与 SHA-256 证据位于 `testdata/baseline/p0-bundle.json{,.sha256}`；避免在被审计文档中嵌入 bundle 自引用 hash。
- 冻结 manifest SHA-256 为 `992c76516d2a30453701a8260683acea5d6d49607fbbf19e86f8f3bb3a891d8f`；license evidence SHA-256 为 `9c7d14c4d2b884169484f45bce37bef0c0dd5a5325a899ec8533dc39f7ab13d5`。
