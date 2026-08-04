# ADR-0002：保守的 baseline scope 分类

- 状态：accepted
- 日期：2026-08-04
- 决策者：项目维护者
- 影响阶段：P0、P17

## 背景与证据

P0 manifest 需要把冻结上游文件标为 `canonical-v2 / v1-archaeology / shared`，供后续 inventory 和 drift diff 使用。上游仓库仍处于 V1/V2 迁移期：少量目录具有明确 canonical V2 定位，主计划附录也明确列出了若干只能用于 V1 行为考古的文件；其余组合层常同时包含 canonical、legacy、产品外壳或共享设施。

若仅凭目录名把混合包全部标为 canonical，会把规划判断伪装成上游事实并抬高差分完成率。若把所有混合包标为 V1，又会丢失仍需复刻的 Config、MCP、CLI、TUI 等行为证据。

## 决策

1. scope policy 版本为 `ADR-0002/v1`，写入每份 baseline manifest。
2. 明确的 canonical 包前缀标为 `canonical-v2`：`packages/core`、`schema`、`protocol`、`llm`、`server`、`client`、`sdk-next`、`codemode` 和 `plugin/src/v2`。
3. 主计划“V1 行为考古”明确列出的文件，以及 legacy Permission、legacy SDK 前缀标为 `v1-archaeology`。
4. 其余文件保守标为 `shared`。`shared` 只表示“需要在具体 Feature inventory 中继续细分”，不表示已纳入 canonical scope、已实现或已验证。
5. Feature Matrix 的 `canonical / replica-extension` 分类仍是产品完成率的权威口径；file scope 只是考古和 drift routing hint，不能单独改变 Feature 状态。
6. 分类规则变化必须提升 policy 版本、更新本 ADR 或由后续 ADR 取代，并让 baseline diff 报告规则变化。

## 后果

- P0 可以生成覆盖全部 tracked file 的稳定分类，而不对混合目录过度断言。
- P17 仍需在每个 Feature 的源码/测试 inventory 中把 `shared` 细化，不能用当前 file count 直接计算 canonical 百分比。
- 同一 blob 因 policy 升级发生分类变化时会被 manifest diff 识别为 modified。

## 验证

- fixture 覆盖 canonical generated schema、明确 V1 Processor 和 shared 文件。
- manifest 固定记录 `scope_policy=ADR-0002/v1`。
- self-diff 必须为空；policy 或分类变化必须产生可见 diff。
