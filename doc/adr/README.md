# Architecture Decision Records

ADR 用于记录会影响 canonical 行为、状态所有权、协议边界、依赖方向或阶段门槛的决策。已接受的 ADR 不直接删除；后续决策用新的 ADR 标记其 `superseded`。

## 状态

- `proposed`：等待证据或评审。
- `accepted`：当前实现必须遵守。
- `superseded`：已由后续 ADR 替代。
- `rejected`：保留被拒方案及理由。

## 模板

```markdown
# ADR-NNNN：标题

- 状态：proposed
- 日期：YYYY-MM-DD
- 决策者：项目维护者
- 影响阶段：P?

## 背景与证据

说明需要决策的问题，并区分冻结上游事实、复刻版设计和未确认项。

## 决策

列出唯一、可测试的决策及禁止项。

## 后果

记录收益、成本、迁移和回滚影响。

## 验证

列出自动测试、差分证据和完成门槛。
```
