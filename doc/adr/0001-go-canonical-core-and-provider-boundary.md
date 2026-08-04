# ADR-0001：Go canonical Core 与 Provider 边界

- 状态：accepted
- 日期：2026-08-04
- 决策者：项目维护者
- 影响阶段：P0、P1、P2、P5、P7、P9、P14

## 背景与证据

冻结上游 `dev@89130db6b0060a345548d870c51132ee71d6a828` 的 canonical V2 Runner 每个 Provider turn 只调用一次 `llm.stream(request)`，Tool Call 在 Go Core 对应的权威边界内先 durable、再执行、再 settlement，随后从 durable history 继续。复刻目标要求只有一套不带版本后缀的 `Session + Execution + Coordinator + Runner`，且默认产品在没有 Python 的环境中仍能构建、启动并完成核心旅程。

旧规划的部分 2026-08-04 修改曾把“Python Worker 完整 turn RPC”提升为 P5 重点路径。这会让 Protobuf、grpcio、Python runtime 和第二进程进入 canonical 核心关键路径，与默认单 Go binary、Tier-1 Provider 由 Go 实现以及首个 Go-only vertical slice 的执行授权冲突。

## 决策

1. Go 是 Session、Execution、Coordinator、Runner、Event Store、SQLite、Tool Registry、Permission、Question、Tool settlement、Compaction、Retry/Overflow 和恢复逻辑的唯一权威拥有者。
2. 首个可合并里程碑使用进程内 deterministic fake `ProviderPort`，完成真实 SQLite/Event/Projector、Prompt admission、无工具单 turn、interrupt、restart 和 replay/recovery；Python、Protobuf、gRPC 均不是前置。
3. P5 首先用 Go 实现 OpenAI Responses、Anthropic Messages 和 OpenAI-compatible 三个 Tier-1 adapter。Adapter 只负责 request projection、HTTP/SSE framing、native chunk 到 canonical `LLMEvent`、usage/cache/provider metadata、transport failure 分类和 redaction。
4. Runner 负责完整 turn retry、compaction、Tool settlement、continuation 和下一 Provider turn。任何 Provider SDK 的隐式 retry/fallback 必须关闭或被显式观测，不能隐藏重复计费或 Tool Call。
5. Python 的生产定位是 P7 起可选的 MCP Integration Runtime。Python MCP Tool 只能作为 Go Registry/Permission/durable settlement 链路中的 execution leaf。
6. Provider RPC 只能在 P14 或更晚、出现已验证的真实长尾 Provider 需求，并由新 ADR 证明收益高于打包、版本、取消和故障成本后引入。它不得成为 P1、P2、P5、P9 或首个 vertical slice 的门槛。
7. 禁止 Python 读取 SQLite、持有 Session repository、保存 checkpoint、决定 Permission、执行 Tool settlement或推进下一轮 Agent Loop。

## 后果

- 默认构建、普通 CI 和核心恢复链路不依赖 Python 或跨进程协议。
- P5 的 Go adapter 工作量高于依赖 Python SDK 的方案，但 Provider request、chunk、usage、cache、错误和取消语义能够在 canonical 边界内被直接验证。
- Python Wiki/Web/文档/SaaS connector 仍可在 P7 通过标准 MCP 懒启动、独立禁用和删除，不要求数据库迁移。
- 若未来批准长尾 Provider RPC，它必须实现同一个 `ProviderPort` contract，不能改变 Runner、Event 或数据库模型。

## 验证

- `go test ./...` 在 PATH 中没有 Python 时通过。
- fake Provider 驱动的首个 durable vertical slice 覆盖 interrupt、进程重启和 replay。
- 三个 Tier-1 Go adapter 分别通过 request/stream/error/cancel/metadata contract tests。
- 静态依赖检查阻止 Protobuf/grpcio/Python Provider Worker进入 P1、P2、P5、P9 的必需依赖图。
- 启用 Python MCP profile 时，`ToolCalled → Permission → MCP call → Tool terminal settlement` 的权威事件只由 Go 写入。
