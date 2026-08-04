# P10–P17 产品外壳、生态与发布实施手册

> 状态：V2-only 规划草案；所有阶段均为 `pending`。本文以 P0–P9 canonical 核心链路已通过联合验收为前提，只规定实施与验收，不代表任何功能已经完成。

## 0. 实施原则与依赖图

```text
P9 ─► P10 ─┬─► P11 ─► P12
           ├─► P13
           ├─► P14
           └─► P15（可后置）
P11/P13/P14 ─► P16（可选，本机默认跳过）─► P17
P0 shared baseline/differential fixtures ─────────► P17
```

**本机优先说明：** 本手册按“单机运行、不对外分发”编排：P16 发布阶段可选，默认跳过，不计入完成门槛；P15 企业集成可后置；P17 的跨平台与发布 rehearsal 置后。核心目标是让 Go core 在本机被 CLI/TUI/SDK 完整使用。

所有客户端只通过 P10 canonical 公开协议与 Go 核心交互；不得让 TUI、Python Integration、MCP server 或 Extension Worker 绕过 API/Tool settlement 直接写 SQLite。每阶段区分 upstream canonical fixture 与复刻版新增 connector contract，并把平台差异写成显式 waiver，不得以“看起来一样”代替测试。legacy route/SDK/DB、V1 Plugin/Permission/Agent Loop 明确不进入本手册。

## P10 HTTP、OpenAPI、SSE、WebSocket 与 SDK

### 目标、非目标与证据范围

- 目标：实现 canonical `/api` route、middleware、OpenAPI、SSE/WS、错误 envelope 与生成 SDK。
- 非目标：HTTP DTO 不成为 Domain；SSE 不承担 durable delivery；客户端无 DB 权限。
- 对照：`packages/server`、`packages/client`、`packages/sdk-next`、canonical protocol groups 和 route tests；排除 legacy `packages/sdk` 表面。
- 前置：P9 Session API；P1 HTTP codec；P4 replay/subscription。

### 推荐目录与逐步任务

```text
internal/server/{route,middleware,sse,ws}/  api/openapi/
sdk/{go,python}/  test/contract/
```

1. Red：从上游 canonical protocol 导出 route/method/query/header/status/body/error/OpenAPI fixtures；legacy route 应进入拒绝/不存在测试。
2. handler 只 decode→command/query→encode；Domain error 映射保持字段、状态码和 null/missing。
3. SSE 先 replay `after` cursor 再切 live；每连接有界队列、heartbeat、慢客户端策略。
4. WS/PTY proxy 明确 origin/auth/header stripping、frame/message limit 和关闭码。
5. OpenAPI clean generation；直接生成新 Go/Python client SDK，不以旧 JS SDK 作为验收入口；Python client SDK 是 API consumer，不等同于 Python Integration Runtime。
6. mDNS、CORS、compression、proxy/UI fallback 分别测试，不混入核心 router。

```go
func (h *SessionHandler) Prompt(w http.ResponseWriter, r *http.Request) {
	cmd, err := h.codec.DecodePrompt(r)
	result, err := h.sessions.Prompt(r.Context(), cmd)
	h.codec.WriteResult(w, result, err)
}
```

### 测试、失败与 DoD

- 测试：HTTP exercise DSL、OpenAPI breaking diff、slow SSE、cursor gap、WS origin、SDK error/cancel。
- 回滚：新增 route 可 feature gate；不得静默回落到错误版本；OpenAPI breaking 需版本 ADR。
- 评审：SSE overflow 行为、canonical route 版本策略、mDNS 默认、安全 header。
- DoD：冻结 canonical contract 100%；Go/Python client SDK 可完成 Session；Go-only server 不依赖 Python runtime；legacy route 不存在；事件不丢序、内存有界。

## P11 CLI、run、import/export 与维护命令

### 目标、非目标与证据范围

- 目标：复刻 command tree、flags/help、TTY/non-TTY renderer、exit code、signal 和维护入口。
- 非目标：CLI 不包含另一套 Session loop；维护命令不绕过 DB lock/migration。
- 对照：`packages/opencode` CLI commands、run/auth/mcp/agent/model/db/upgrade tests。
- 前置：P10 client/SDK；P3 config；P4 db maintenance。

### 推荐目录与逐步任务

```text
cmd/opencode/  internal/cli/{command,render,completion}/
internal/importer/  internal/exporter/  internal/maintenance/
```

1. Red：捕获上游 help、flag precedence、stdout/stderr、exit code 和 TTY transcript golden。
2. 建 command manifest，所有子命令共享 config/server discovery/error renderer。
3. `run` 通过 SDK 调 Session；JSON mode 每行可解析，human mode 保持 live block 顺序。
4. canonical import 先 parse/validate/plan，再单事务或 checkpoint batch；export 有 schema/version/hash；不直接读取 legacy SQLite。
5. completion 由 command manifest 生成；shell 只支持明确目标，不写用户配置。
6. signal 首次 graceful cancel，二次强退；终端恢复必须注册在 root scope。

### 测试、失败与 DoD

- 测试：Unicode、中文/空格路径、pipe、broken pipe、无 stdin、SIGINT/SIGTERM、Ghostty/macOS 26。
- 回滚：import 失败不覆盖原 DB；upgrade 交 P16；命令输出 breaking 由 golden 阻断。
- 评审：自动启动 server、JSON streaming 格式、completion 安装范围。
- DoD：command/flag/help/error snapshot 100%；TTY/non-TTY 无控制字符污染；退出码等价。

## P12 TUI

### 目标、非目标与证据范围

- 目标：实现 layout、theme、navigation、canonical session stream、permission/question、subagent 和 attach。
- 非目标：TUI 不持有权威 Session；不因 renderer 丢帧改变 durable 状态。
- 对照：`packages/tui`、相关 TUI event、snapshot/E2E 和 terminal adapter。
- 前置：P10 SDK/SSE、P11 terminal lifecycle；TUI engine spike ADR。

### 推荐目录与逐步任务

```text
internal/tui/{model,update,view,theme,keymap}/
test/tui/{driver,golden}/
```

1. Red：定义核心旅程的 semantic screen tree 和 ANSI frame golden；先测 model/update，再测渲染。
2. 比较 Bubble Tea/Lip Gloss 等候选对 IME、宽字符、鼠标、alternate screen、性能的支持。
3. Client event→TUI message 使用有界 coalescing；durable terminal 不得被 delta 合并丢失。
4. 实现 session switch、scrollback、dialog、permission/question、child/background 状态。
5. 终端 resize/suspend/resume/disconnect 归 scope；panic 也恢复 cursor/mode。
6. accessibility：无颜色模式、可配置 keymap、screen-reader 兼容输出 profile。

```text
API Event → Adapter → Msg → Update(Model) → Commands
                              └────────────► View(Frame)
```

### 测试、失败与 DoD

- 测试：80/120/200 列、emoji/CJK/combining、resize storm、鼠标、SSH、Ghostty、Windows Terminal。
- 回滚：renderer engine 隔离在 Port；若 spike 不达标可替换，不改 Domain/API。
- 评审：像素等价还是语义等价门槛、scrollback 上限、远程 attach 延迟。
- DoD：核心旅程 interaction/frame golden 全过；无 goroutine/terminal state 泄漏；真机矩阵签字。

## P13 ACP、LSP、PTY 与 Formatter

### 目标、非目标与证据范围

- 目标：实现编辑器协议、语言服务生命周期、跨平台终端会话和 formatter catalog。
- 非目标：LSP/PTY 子进程不脱离 Project scope；formatter 不在未知配置下静默改文件。
- 对照：ACP、LSP、PTY、Formatter packages、server proxy 和平台 tests。
- 前置：P3 Project/Config、P6 Permission、P10 WS、P11 CLI lifecycle。

### 推荐目录与逐步任务

```text
internal/acp/  internal/lsp/  internal/pty/{unix,windows}/
internal/formatter/  test/fakes/lsp/
```

1. Red：ACP initialize/session/cancel；fake LSP initialize/diagnostic/shutdown；PTY replay/reconnect fixtures。
2. ACP stdio framing、capability 和错误与上游 contract 对齐，所有 request 绑定 context。
3. LSP 按 project/language lazy start；document version fencing；push/pull diagnostic 归一化。
4. PTY 定义 platform Port：Unix pty 与 Windows ConPTY；输出 ring buffer 带 cursor/epoch。
5. WS attach 从 cursor replay，再切 live；慢客户端不阻塞 PTY reader，超限给 gap signal。
6. Formatter detection/config/command 经 permission；写入使用 snapshot/atomic replace 并回传 diff。

### 测试、失败与 DoD

- 测试：坏 framing、server hang/crash、stale diagnostics、1GiB PTY output、断线重连、formatter timeout。
- 回滚：子进程 crash 可重启；文件格式化失败保留原文；cursor gap 要求客户端 resync。
- 评审：PTY buffer 持久化、LSP 多 root、formatter 自动运行时机。
- DoD：冻结 suite 100%；三平台真机；进程/socket/terminal 无泄漏；高吞吐不 OOM。

## P14 V2 扩展、自定义 Tool 与 CodeMode

### 目标、非目标与证据范围

- 目标：基于 V2 语义设计 extension hook/order/mutation、自定义 Tool、reload/dispose 和 CodeMode，同时隔离不可信代码。
- 非目标：不执行或适配 TypeScript/Bun V1 ABI；Extension 不直写 SQLite。
- 对照：`packages/plugin/src/v2`、`codemode`、core V2 plugin hooks/custom tool tests；V1 Plugin 仅作考古。
- 前置：P6 Registry/Permission、P7 MCP/integration manifest、P10 API、P2 process scope；仅 MCP 无法表达的 hook 才需要 V2 Extension RPC ADR。

### 推荐目录与逐步任务

```text
internal/extension/{registry,hook}/  internal/codemode/
python/connectors/  python/extensions/  extension-sdk/{mcp,go,python}/
```

1. Red：选定 canonical V2 lifecycle corpus，记录加载、hook 顺序、参数/返回 mutation、错误与 dispose fixture。
2. 先将 custom Tool/resource/prompt 映射到标准 MCP + integration manifest；manifest/capability/generation/deadline 进入统一 Go host。
3. 只有 canonical V2 hook mutation/lifecycle 无法由 MCP 表达时才定义最小语言无关 Extension RPC；Go host 或可选 Python Extension Worker 实现，禁止 dynamic import/BunShell/V1 Hooks façade。
4. reload 先建新 generation 并探活，再原子切换；旧 in-flight 完成后 dispose。
5. 自定义 Tool 进入统一 Registry/Permission；schema/input/output 都做大小和类型验证。
6. CodeMode 使用最小 OS capability、临时 workspace、网络/文件策略和输出限额。

### 测试、失败与 DoD

- 测试：恶意 extension、无限循环、OOM/crash、版本错、stale tool、hook timeout/mutation、sandbox escape。
- 回滚：host crash 不破坏 Go 状态；插件逐个熔断；reload 失败保留旧 generation。
- 评审：哪些 hook 必须自定义 RPC、Python connector/extension capability 粒度、sandbox 强度、hook mutation 冲突与签名分发。
- DoD：选定 V2 extension contract 100%；host 隔离安全审计通过；静态检查证明无 V1 Plugin/Bun/JS runtime 依赖。

## P15 Share、GitHub、GitLab 与企业集成

> 本机优先注：可后置，本机场景可跳过；需要对外集成时再启动。

### 目标、非目标与证据范围

- 目标：Share 生命周期、GitHub/GitLab workflow、账户/组织控制面和外部幂等。
- 非目标：普通 CI 不写真实组织；外部 API 不能成为本地 Session 唯一历史。
- 对照：Share、GitHub action/PR/comment、GitLab、account/control plane integrations/tests。
- 前置：P3 Auth、P9 Session、P10 API、P16 前置 secret/release policy。
- **本机优先说明：** 本机单机场景基本不需要，默认最后做，可与 P16 一起跳过；仅在需要对外集成时启动。

### 推荐目录与逐步任务

```text
internal/share/  internal/integration/{github,gitlab}/
internal/account/  test/fakes/forge/
```

1. Red：mock forge contract，覆盖 pagination、rate limit、webhook replay、token refresh、partial failure。
2. 外部 command 建 idempotency key/outbox；本地事务记录 intent，异步执行并记录 result。
3. Share create/revoke/read 权限与上游 URL/error 对齐；敏感内容有明确过滤/确认。
4. GitHub PR/comment/action 关联 repository/ref/session；重复 webhook 不重复创建 Session/评论。
5. GitLab 特殊 Provider/identity 仍经 canonical adapter，不建立第二个 Agent loop。
6. enterprise policy 在 command 前决策，审计日志不存 secret/prompt 原文。

### 测试、失败与 DoD

- 测试：429、5xx、超时、重复 webhook、权限撤销、org policy change、补偿、secret scan。
- 回滚：外部副作用使用 revoke/update/compensation；无法回滚必须呈现人工处理步骤。
- 评审：Share 默认可见性、数据驻留、outbox 重试上限、企业审计范围。
- DoD：contract/idempotency/revoke 通过；sandbox 组织 E2E；普通 CI 零真实外部写入。

## P16 安装、升级、签名、公证与发布

> 本机优先注：可选阶段，本机默认跳过，不进入完成门槛；仅在需要对外分发时再启动。

### 目标、非目标与证据范围

- 目标：可复现构建可独立运行的 Go core artifact，并为可选 Python integration profile 完成独立安装、升级、回滚、签名、公证和 SBOM。
- 非目标：core 不在线下载或隐式要求 Python；integration runtime 不在线下载未锁定包；不要求用户用 `sudo` 修复权限。
- 对照：安装脚本、upgrade、release workflow 和平台 package tests。
- 前置：P11 CLI、P14 integration/extension profile 范围决策；release threat model。
- **本机优先说明：** 默认只在本机运行，本阶段**不进入完成门槛**，标记低优先；仅在需要对外分发时再启动（恢复估算 Core 6 / Python profile 3 人月）。本机只需保证 `go build` 产出单二进制可离线运行。

### 推荐目录与逐步任务

```text
build/  packaging/{brew,scoop,choco}/  release/
python/lock/  integration-manifest/  test/install-vm/
```

1. Red：clean VM install/upgrade/downgrade/offline/proxy/corrupt artifact/权限错误矩阵。
2. 固定 Go toolchain；core artifact hash 可重复且无 Python 文件。Python runtime/wheels 按可选 profile 单独锁定。
3. core manifest 与 integration manifest 分离；后者列 entrypoint、所有文件/hash/capability/最低 OS/依赖锁，启用前验证与主程序兼容。
4. channel metadata 签名，下载到 staging，验签/health 后原子切换；保留一个可启动旧版本。
5. macOS hardened runtime/sign/notarize/staple；Windows signing/SmartScreen；Linux package signatures。
6. 生成 SBOM、license notice、provenance；发布 rehearsal 使用与正式相同流水线。

```text
download → verify metadata/signature/hash → stage → smoke → atomic switch
                                                     └fail→keep current
```

### 测试、失败与 DoD

- 测试：Gatekeeper、SmartScreen、代理/TLS、磁盘满、断电、跨 schema downgrade、PATH/中文用户名。
- 回滚：DB migration 兼容性先检查；不可逆 migration 阻止自动 downgrade；binary 可原子回切。
- 评审：Python profile 使用受支持解释器、随附 CPython 还是 standalone；connector 拆包粒度、签名密钥/HSM、支持 OS 版本、auto-update consent。
- DoD：三平台 Go-only release rehearsal；签名/公证自动验证；无 Python 离线启动；升级失败可恢复。选择发布的 Python profile 另通过安装/升级/卸载/签名测试，卸载不迁移或破坏 DB。

## P17 V2 全量验证、性能、安全与上游追平

### 目标、非目标与证据范围

- 目标：运行 canonical V2 fixture/differential/E2E/fuzz/fault/performance/security，升级冻结基线并清零未批准差异。
- 非目标：不是“最后补测试”；不把 waiver 当永久完成；不因平均性能相近忽略 p95/p99。
- 对照：P0 inventory 中全部 `canonical-v2/shared` 功能、测试、协议、平台和当前上游 drift；排除 `v1-archaeology`。
- 前置：P0–P16 阶段 DoD；候选 release artifact。

### 推荐目录与逐步任务

```text
test/differential/{runner,fixture,cassette}/
test/performance/  test/fault/  security/  baseline/upgrades/
```

1. 将同一 canonical fixture 驱动 upstream-v2 与目标版，规范化仅限批准的 nondeterminism；diff 保留原始证据。
2. 全量 Provider cassette（本机路线轻量）、Tool/Permission、Event/SQLite、HTTP/SSE/WS、CLI/TUI；三平台运行置后。
3. fuzz/property 覆盖 codec、JSON、event state、HTTP parser、Tool path、MCP；可选 Provider/Extension RPC 仅在实际发布时纳入。
4. fault 覆盖 Go crash、kill -9、disk full、clock jump、network partition、slow consumer，以及启用 profile 下的 Python connector/worker crash。
5. 同硬件测 warm start、first frame、publish p95、fanout、Session memory、PTY throughput；阈值默认 ±10%。
6. threat model、SAST/DAST/dependency/SBOM、OAuth CSRF、WS Origin、command/path injection、sandbox escape。
7. 用 P0 baseline diff 评估新上游；先过滤 `v1-only`，再更新 canonical inventory/fixtures 并按影响升级模块。
8. 每个差异必须是 fix、`blocked` 或有 owner/expiry/evidence 的 waiver；过期自动阻断 release。

### 测试、失败与 DoD

- 测试本身：故意注入差异，证明 harness 不会被 normalize 掩盖；重复运行测 flaky rate。
- 回滚：基线升级使用独立分支/manifest；失败保留上一 canonical release，不混合两套 Schema。
- 评审：性能阈值、waiver 审批者、上游 canonical 升级节奏和平台差异。
- DoD：满足主计划第 13 章；公开 contract/durable 语义差异为 0；性能/安全门槛全过；waiver 可审计且未过期。

## 10. P10–P17 联合验收

**本机优先验收：** 在本机从没有 Python 的干净目录安装/构建 Go core，经生成的 Go/Python client SDK、CLI、TUI 分别完成同一套 Session/Tool/Permission/PTY 旅程；随后断网、杀进程、重启并验证 Event/SQLite transcript。再对声明启用的 Python integration profile 做增量安装与 connector 旅程。跨平台 clean VM、发布与签名矩阵置后，仅在正式对外分发时执行。还必须证明：

- 所有 UI/客户端只是 API consumer；无绕过 Go writer 的路径。
- legacy route/SDK/DB 与 TypeScript/Bun V1 Plugin ABI 不存在于发布物，且不会被错误宣传为支持。
- core 与 Python integration artifact、manifest/descriptor、SBOM、签名和 canonical baseline Commit 分别可追溯；core 不含隐藏 Python 依赖。
- differential harness 在候选 artifact 而非开发目录运行；三平台失败不能用单平台通过替代。
- P0–P17 的 Feature 状态、actual 人月、上游 drift、waiver 和证据已回填主计划。
