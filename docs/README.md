# Cora 文档地图

本页定义 Cora 文档的阅读顺序和事实边界。代码、运行状态和历史快照可能处于不同版本；不要把
历史 handoff 中的 dirty worktree、构建号或“下一窗口”描述当成当前 Git 或生产真相。

## 从这里开始

1. [`CORA_OVERVIEW.md`](CORA_OVERVIEW.md)：产品目标、架构、核心边界和当前能力概览。
2. [`HANDOFF.md`](HANDOFF.md)：当前代码地图、生产拓扑、已验证事实、剩余风险和下一闭环。
3. [`../README.md`](../README.md)：本地运行、验证命令和所有主要入口。

涉及 Git 状态时，以 `git status`、`git log -1` 和远端引用为准；涉及线上状态时，以部署主机的
build identity、health/readiness、MCP tools list 和数据库检查为准。

## 当前规范与操作手册

- [`CORA_AGENT_V0.md`](CORA_AGENT_V0.md)：Agent 配置、解析、脱敏、投递和运行信号。
- [`CORA_V0.md`](CORA_V0.md)：Core 决策契约、经验包与评估限制。
- [`PRODUCTION_READINESS.md`](PRODUCTION_READINESS.md)：v0 生产承诺、不可接受边界和 canary 门槛。
- [`RULE_ITERATION_WORKFLOW.md`](RULE_ITERATION_WORKFLOW.md)：只读 T+1 case/规则迭代工作流。
- [`RETENTION_AUDIT.md`](RETENTION_AUDIT.md)：在线预检与离线 B0 retention 法证审计。
- [`../deploy/README.md`](../deploy/README.md)：构建、Supervisor、备份、回滚和当前生产拓扑。
- [`PERFORMANCE_BASELINE.md`](PERFORMANCE_BASELINE.md)：可复现的聚合性能基线。

## 设计与决策依据

- [`VISION_ALIGNMENT.md`](VISION_ALIGNMENT.md)：原始愿景与当前实现的防偏移对照。
- [`ADR_001_PRODUCTION_FACT_LIFECYCLE.md`](ADR_001_PRODUCTION_FACT_LIFECYCLE.md)：生产热事实、
  不可变迭代证据、closure receipt 与未来 retention mutation 的责任边界。

## 历史阶段快照

- [`HANDOFF_2026-07-15_RULE_ITERATION_AND_RETENTION.md`](HANDOFF_2026-07-15_RULE_ITERATION_AND_RETENTION.md)：
  目标 A 启动与完成时的规则迭代/生命周期设计快照。
- [`HANDOFF_2026-07-15_TARGET_B_RETENTION.md`](HANDOFF_2026-07-15_TARGET_B_RETENTION.md)：
  B0 离线审计、生产备份证据和在线 retention 预检的阶段快照。

历史快照保留当时的 commit、dirty build、生产版本和验收数据，便于追溯；它们不持续更新。
若历史快照与 `HANDOFF.md` 或实时检查冲突，优先使用实时检查，其次使用 `HANDOFF.md`。
