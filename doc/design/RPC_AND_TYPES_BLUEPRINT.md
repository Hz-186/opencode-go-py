# Python Integration Runtime、Provider RPC 与跨语言边界详细设计

> 状态：V2-only 设计草案，供评审。本文冻结于上游 `dev@89130db6b0060a345548d870c51132ee71d6a828`。为保持既有链接，文件名暂保留 `RPC_AND_TYPES_BLUEPRINT.md`；RPC 已不是默认运行时或首个 vertical slice 的前置。

## 1. 结论与边界

**ADR-0001 修订（2026-08-04）：** Provider RPC 保持为 P14+ 可选长尾能力，不是 P5、P9 或首个 vertical slice 的前置。P5 由 Go 实现 OpenAI Responses、Anthropic Messages 和 OpenAI-compatible 三种 Tier-1 adapters；只有真实长尾需求和独立 ADR 才能启用本文件的 Provider Worker 设计。

目标运行时是可独立工作的 Go canonical Core。Go 持有 Session、Execution、Runner、Tier-1 Provider、Tool Registry、Permission、Event、SQLite 和资源生命周期的全部权威状态；没有 Python runtime 时，CLI/TUI/embedded Server、Provider、Tool 和恢复链路必须完整工作。

Python 的正式定位是**可选 Integration Runtime**：

1. 通过标准 MCP Tool/Resource/Prompt 实现 Wiki、Web Fetch、知识库、文档/数据处理和第三方 SaaS connector。
2. 在明确启用的 profile 中提供非 canonical 长尾 Provider 或 Python-only SDK adapter。
3. 承载 LangGraph、PydanticAI、OpenAI Agents SDK 等对照实验，但实验不得成为生产 fallback。
4. 由 Go 按配置和 capability 懒启动、监督、取消和回收；用户不手工启动第二个后端。

Python 不得读取 SQLite、持有 Session repository、决定 Permission、结算 Tool、保存 checkpoint、推进下一 Provider turn，或重建 `SessionPrompt`/`SessionProcessor` 式第二循环。Python crash 只能形成一个可解释的 integration/tool/provider boundary failure，不能改变 durable truth。

## 2. 与 OpenCode canonical V2 的对齐

- 上游 V2 Runner 每个 turn 调用一次 `llm.stream(request)`，在本地持久化/结算 Tool 后重载历史；目标版保留该边界，只将调用抽象为 Go `ProviderPort`：[llm.ts](/Users/zhanghongze/PycharmProjects/opencode/packages/core/src/session/runner/llm.ts#L60)。
- 冻结基线的 canonical model resolver 只支持 OpenAI Responses、Anthropic Messages 和 OpenAI-compatible 三种 API type；这使首版 Go Tier-1 范围可控：[model.ts](/Users/zhanghongze/PycharmProjects/opencode/packages/core/src/session/runner/model.ts#L131)。
- 上游 LLM schema 能只暴露 Tool definition，由 Runner 外部执行 Tool；因此 Python Provider 永远不能在内部执行本地 Tool：[tool.ts](/Users/zhanghongze/PycharmProjects/opencode/packages/llm/src/tool.ts#L36)。
- 上游 MCP 已有 stdio、Streamable HTTP/SSE、OAuth、tool/resource/prompt、dynamic catalog 和 scope close；Python 生产工具优先作为 MCP server 接入，不新增平行 Tool RPC：[index.ts](/Users/zhanghongze/PycharmProjects/opencode/packages/opencode/src/mcp/index.ts#L164)。
- 上游 Tool Registry 将 definitions materialize 后由 core-owned `settle` 执行；Python connector 只是 executor，必须经过同一 registry snapshot、permission 和 durable terminal：[registry.ts](/Users/zhanghongze/PycharmProjects/opencode/packages/core/src/tool/registry.ts#L23)。
- Wiki、文档解析、数据分析和额外 SaaS connector 是复刻版扩展，不是上游已有能力。它们使用 canonical 扩展边界，但 fixture、状态和完成率与 upstream differential 分开记录。
- V1 BunShell/Hooks、JS compatibility sidecar、legacy Session/Permission/DB 不因 Python 被重新引入。

## 3. 能力档位与启动模型

| Profile | 进程 | 内容 | 是否为 canonical release 前置 |
|---|---|---|---|
| `core` | 单 Go 进程 | Session/Runner、Go Tier-1 Provider、built-in Tool、MCP host、SQLite、CLI/TUI/Server | 是 |
| `python-tools` | Go + 按需 Python MCP 子进程 | Wiki/Web、知识库、文档/数据、SaaS connector | 否，按 connector contract 验收 |
| `python-providers` | Go + P14+ 可选 Python Provider Worker | 经 ADR 批准的长尾/LiteLLM adapter | 否，不替代 canonical 核心或 Tier-1 Go adapters |
| `experiments` | 隔离实验进程 | LangGraph/PydanticAI/Eino/Agents SDK 对照 | 永远不进生产完成率 |

默认安装和启动只启用 `core`。Go 解析配置时只登记 integration manifest，不启动进程；首次 catalog discovery 或 call 才启动对应 Python MCP server。空闲回收、project scope 关闭、reload、shutdown 和 crash cleanup 均由 Go 管理。

一个 Python integration 的缺失、版本不兼容或启动失败只能使该 capability unavailable。Go core、其他 connector、已有 Session 和数据库必须继续可用。

## 4. Python MCP Tool/Resource/Prompt 边界

### 4.1 为什么 Tool 优先 MCP

OpenCode 已经把外部工具抽象为 MCP catalog，并负责 transport、OAuth、内容转换和生命周期。Python MCP SDK 又能直接暴露 Tool/Resource/Prompt，因此复用 MCP 同时满足“利用 Python 库”和“不偏离上游架构”，无需为 Wiki、PDF 或 SaaS 各设计一套 gRPC。

调用链：

```text
Provider emits ToolCall
  → Go durable ToolCalled
  → Go validates frozen registry generation + input schema
  → Go Permission(action/resource/capability)
  → Go MCP client calls managed Python server
  → Python library/API produces bounded MCP content
  → Go validates output/media/provenance
  → Go durable ToolCompleted|ToolFailed|ToolCancelled
  → Runner reloads history and starts next Provider turn
```

### 4.2 Integration manifest

每个生产 Python integration 必须声明：

```yaml
name: knowledge.wikipedia
version: 1.0.0
protocol: mcp
transport: stdio
entrypoint: [python, -m, opencode_integrations.wikipedia]
capabilities:
  tools: [wiki.search, wiki.fetch]
network:
  allow_domains: [wikipedia.org, wikimedia.org]
filesystem:
  mode: none
credentials: []
limits:
  timeout_ms: 15000
  max_output_bytes: 2097152
  max_memory_mb: 256
lock_hash: sha256:...
```

实际 manifest 格式在 P7 ADR 冻结。entrypoint 不得由模型提供；模型只能传入 Tool JSON Schema 允许的业务参数。

### 4.3 Reference connector

P7 首个 reference connector 使用 Wiki/Web knowledge 场景，至少提供 search/fetch 等价能力。返回值必须包含 `source_url`、标题、revision/更新时间（来源提供时）、retrieved time、正文和 provenance/trust label。远端内容始终视为不可信输入，不能因为由 Python library 获取就升级为 system instruction。

Reference connector 的意义是验证：

- Python 库确实减少连接器代码，而不接管 Tool lifecycle。
- MCP crash/cancel/timeout 能被 Go settle。
- connector 可安装、禁用、升级和删除，DB 无需 migration。
- Go-only profile 不因其存在而增加启动或发布前置。

## 5. Go ProviderPort 与 Tier-1 实现

Provider 是 canonical Runner 的 Port，而不是跨进程协议：

```go
type ProviderPort interface {
	RunTurn(ctx context.Context, request ProviderTurnRequest, sink LLMEventSink) error
}
```

P5 优先冻结 `ProviderPort` 与 fake 底座，并在 Go 中实现 OpenAI Responses、Anthropic Messages 和 OpenAI-compatible adapters。它们负责 request projection、HTTP/SSE framing、canonical `LLMEvent`、usage/cache metadata、transport failure 分类和 redaction；本文件第 6 节的 RPC 不进入 P5 门槛。

Go adapter、RPC adapter 与 fake adapter 使用同一 Port。Session/Runner 不导入具体 SDK 类型，也不知道实现是进程内还是 RPC。

## 6. Python Provider Worker（P14+ 可选长尾路径）

只有出现以下至少一项真实需求，并由新 ADR 证明收益高于打包、版本、取消和故障成本时，才允许实现该 Worker：

- 选定 Provider 只有 Python SDK 能可靠支持。
- LiteLLM 等统一层能显著降低明确产品范围内的 connector 数量。
- Provider 依赖必须进程隔离，且收益经 spike/ADR 证明高于打包和故障成本。
- 需要独立升级 Provider adapter，但不改变 Go core。

一次 RPC 仍只对应一次完整 Provider Turn：

```protobuf
service ProviderWorker {
  rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
  rpc Health(HealthRequest) returns (HealthResponse);
  rpc RunProviderTurn(ProviderTurnRequest) returns (stream ProviderTurnFrame);
}
```

`ProviderTurnRequest` 是自足不可变快照；Worker 不回调 Go 获取历史，不执行本地 Tool，不读 DB。`ProviderTurnFrame` 带 turn ID、连续 frame sequence、LLMEvent/diagnostic 和唯一 terminal。Go 验证 block/tool 状态机、terminal 唯一性、usage 和大小限制。

SDK/LiteLLM 的完整请求自动 retry/fallback 默认关闭；如底层无法关闭，必须逐 attempt 上报并由 ADR 证明不会隐藏重复计费/Tool Call。Python 不能因为自身方便而扩大 canonical route 支持声明。

Protobuf 只在该 capability 获批后冻结，字段只追加、删除 reserved、major 不兼容 fail-fast。它不进入 P1 Domain、DB/Event JSON 或 HTTP API 的单一来源。

## 7. 类型和 Schema 所有权

| 语义 | 权威表示 | MCP/Python Tool | Provider RPC | 持久化 |
|---|---|---|---|---|
| Session/Message | Go Domain + canonical JSON | 不可见，仅收到 Tool args | request snapshot 中必要 message | Event/projection |
| Tool definition | Go Domain + JSON Schema | MCP catalog schema | Provider request schema snapshot | generation/hash/event |
| Tool call/result | Go Tool state machine | MCP call/content | LLMEvent ToolCall，不执行 | durable terminal |
| LLMEvent | Go tagged union | 不适用 | Protobuf mapper | terminal 映射为 Event |
| Permission | Go policy/domain | 只收到批准后的最小调用 | 不可见 | pending/saved/event |
| Credential | Go `SecretRef/Lease` | 单 connector 短期 lease | 单 turn 短期 lease | secret store，不入 Event |
| Connector output | Go validated content/media | MCP JSON/content | 不适用 | Tool result/output store |

MCP 使用标准 JSON-RPC/JSON Schema；Python model 只用于边界校验。任何 Pydantic/SDK/MCP/generated 类型都不能进入 Go Domain、SQLite schema 或 durable Event definition。

## 8. 生命周期、取消、背压与恢复

### 8.1 MCP/Python Tool

- Go context cancel → MCP request cancel/transport close → grace deadline → process kill escalation。
- 输出进入有界 reader；达到单 item/总字节/时间限制后取消并 settle typed error，不允许无限缓存。
- ToolCalled 已 durable 后的 crash 必须产生一个 durable terminal failure；manifest 只有在显式声明幂等且 policy 允许时才能 retry。
- dynamic catalog 更新产生新 registry generation，进行中 call 继续使用冻结 snapshot。
- Go 重启扫描 pending/running Tool 并按 canonical recovery rule 失败/修复，不向 Python 查询 checkpoint。

### 8.2 可选 Provider Worker

- Go context cancel → gRPC stream cancel → Python task cancel → native HTTP body close。
- Go bounded sink 暂停读取时依赖 HTTP/2 flow control；超过 stall deadline 取消 turn。
- 首 frame/durable output 前 crash 可按 Runner policy retry；已有 durable partial output 后默认不透明重试。
- Worker restart 使用新 generation/turn fence；旧 frame 不得接到新 turn。

## 9. 安全边界

- Python 默认无 SQLite、Session repository、宿主工作区、全量环境变量和任意网络权限。
- Go 根据 manifest 与 Permission 交集签发 capability；schema 声明不是授权。
- credential 使用短期、最小 scope lease；不得出现在 argv、普通环境、日志、cassette、Event 或异常文本。
- 本地 tool 优先 stdio，不监听端口；网络 transport 仅绑定私有 UDS/authenticated loopback，校验 nonce/token/version/frame limit。
- 大媒体使用 Go 管理的只读/只写临时 lease；不得让模型传任意宿主路径。
- connector 独立 dependency lock、hash、SBOM 和 license notice；禁止从当前目录或在线未锁定源隐式加载。
- raw web/wiki/SaaS 内容带 provenance/trust label，继续遵守 prompt injection 防护。

## 10. 打包与版本

`core` 与 `python-integrations` 分开发布和验证：

- Core manifest 只含 Go artifact/必要资源，可离线启动和完成 canonical 核心旅程。
- Integration manifest 声明兼容 core range、Python ABI/runtime、lock hash、capabilities、文件 hash 和最低 OS。
- 新 profile 先安装到 staging、校验/探活后切换；失败保留旧 profile，不能回滚 Go/DB。
- 禁用/卸载 profile 不修改 DB schema；历史 Tool result 保留普通 durable 数据。
- macOS/Windows/Linux 分别验证签名、公证、动态库、冷启动、代理和路径。

是否使用受支持系统 Python、随附 CPython、PyInstaller/Nuitka 或其他 standalone 形式由 P16 spike 决定。该决策不得阻塞 Go core release。

## 11. 验证矩阵

| 类别 | 必测 | 通过标准 |
|---|---|---|
| Go-only | 无 Python 安装、PATH 中无 python | canonical core 全旅程通过 |
| MCP contract | initialize/catalog/call/cancel/dynamic changed | 与 Go registry/permission/settlement 一致 |
| Reference connector | Wiki search/fetch、Unicode、来源、限额 | schema/provenance 稳定，无未授权访问 |
| Process | missing dependency、hang、crash、stderr bomb | core 存活、call typed failure、无 orphan |
| Security | domain/path/env/credential/output escape | fail closed，secret scan 为 0 |
| Install | profile install/upgrade/disable/remove | core/DB 不变、manifest/hash 可追溯 |
| Optional Provider | request/event cassette、cancel、frame gap、crash | 不替代 Tier-1，LLMEvent/metadata contract 通过 |
| Experiment | checkpoint/tool loop 对照 | 只产学习报告，不进入生产依赖 |

## 12. 实施顺序

1. P1 冻结 Go Domain/canonical JSON，不生成 Python/Protobuf 运行时。
2. P2 建 Go scope/process primitives，不建立 Provider Worker。
3. P5 冻结 `ProviderPort`、fake/cassette，并实现三种 Tier-1 Go adapters；不生成 Provider RPC。
4. P6 冻结 Tool executor/permission/settlement 边界。
5. P7 完成 canonical MCP host，再实现 Python Wiki/Web reference connector 与 manifest/lazy supervisor。
6. P9 用 `ProviderPort`（fake/Go Tier-1）闭合唯一 Runner；Python connector 只作为 Tool crash/cancel 插件测试。
7. P14 扩展 Python connector catalog；只有真实长尾 Provider 需求才评审 Provider RPC，只有 MCP 无法表达的 canonical hook 才评审 Extension RPC。
8. P16 仅在需要对外分发时分别发布 Go core 和选定 Python profiles（本机优先口径下可跳过）。
9. Provider RPC 若获批，必须复用 P5 已冻结的 `ProviderPort` contract，且不得降低 Tier-1 Go adapters 的完成门槛。

## 13. 评审时必须决定的问题

1. 首批生产 Python connector 除 Wiki/Web 外是否包含 PDF/Office/数据分析；每项必须独立估算和许可审计。
2. Integration manifest 的 capability 粒度、域名通配、临时文件 lease 和资源预算默认值。
3. 生产 profile 使用一个共享 Python runtime 还是每 connector 隔离环境；默认建议按依赖族隔离，避免一个巨型环境。
4. P16 Python 分发形式与自动安装 consent；默认 core 不自动下载。
5. 哪些 V2 extension hook 无法由 MCP 表达；没有证据时不建立自定义 Extension RPC。
6. 哪些真实长尾 Provider 需求足以证明 RPC/Python Worker 的收益；没有证据时不建立该路径。

## 14. 本设计完成门槛

- 没有 Python 环境时，Go core、fake/Tier-1 Go Provider、canonical MCP、Session、Tool、Permission、Event 和 SQLite 全部完成其阶段 DoD；若 P14+ 启用 Python Worker，则额外通过完整 contract。
- Python reference connector 经标准 MCP 接入，ToolCalled→Permission→call→terminal settlement 全链路只有 Go拥有状态。
- Python crash、取消、输出炸弹、依赖缺失、未授权 URL/路径和 credential 泄漏均有自动化测试及明确失败语义。
- connector 可按需启动、禁用和删除；无 orphan process/socket，DB 不需要 migration。
- canonical differential 与新增 connector contract 分开报告，不能用 Python 新功能掩盖上游行为差异。
- 仓库构建、测试、启动、P5、P9 和首个 vertical slice 不依赖 Protobuf/grpcio/CPython；可选 Provider RPC 只能作为 P14+ 独立 profile 增量加入。
