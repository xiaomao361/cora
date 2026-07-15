# Cora 目标 B Handoff：生产事实 Retention 安全闭环

更新时间：2026-07-15（Asia/Shanghai）

## 1. 新窗口的唯一目标

目标 A“生产事实 → Core 规则/经验迭代”已经通过真实生产验收。新窗口进入目标 B：

```text
case / rule / evaluation / deployment / observation
  → closure receipt
  → retention eligibility audit
  → 后续才允许 compact
  → 更后续才讨论 purge / VACUUM
```

下一窗口第一轮只实现 **B0：只读 retention audit**。不要直接实现删除、压缩、SQLite
migration、checkpoint 或 VACUUM；不要因为存在 `resolved` Problem 或已有规则就判定可清理。

## 2. 仓库与 Git 当前真相

真实独立仓库：

```text
/Users/zhouwei/Documents/ClaraCore/apps/cora
```

- branch：`main`
- remote：`git@github.com:xiaomao361/cora.git`
- local HEAD：`3214ff1`（`fix: keep unhandled problems visible`）
- `origin/main`：`1739512`
- 当前生产二进制不是由一个干净 Git commit 构建，而是由 dirty worktree 的源码快照构建。

新窗口开始必须重新执行：

```sh
pwd
git rev-parse --show-toplevel
git remote -v
git status --short
git log -3 --oneline --decorate
git ls-remote origin refs/heads/main
```

当前 dirty worktree 同时包含此前已经验证但尚未提交的运行日志、Attention incident 归组、
Problem 生命周期测试，以及目标 A 的迭代工作流。不要 reset、checkout、clean 或覆盖这些改动。

必须特别保护以下未跟踪文件：

```text
config/cora-agent-gb-order-service02.yml
docs/CORA_OVERVIEW.md
```

第一个是用户生产配置，禁止误提交。第二个在本次 Handoff 前已经存在且归属未确认，先保留，
不要在目标 B 中顺手修改。`out/` 和 `dist/` 是本地产物边界，不是 Git 源码边界。

## 3. 当前生产拓扑

```text
授权客户端 / Codex
  → https://cora.gbgoodness.com
  → Nginx：public-web（172.22.244.253）
  → Cora Server：public（172.22.245.1:8088）

业务服务器上的 Cora Agent
  → http://public.internal:8088/v1/events:batch
  → Cora Server
```

Server：

- 用户：`gbjk`
- 目录：`/home/gbjk/zhouwei/cora`
- 命令：`/home/gbjk/zhouwei/cora/cora-server -config.file=./cora.yml`
- SQLite：`/home/gbjk/zhouwei/cora/cora.db`
- token：`/home/gbjk/zhouwei/cora/auth.token`
- Supervisor program：`cora-server`

Agent：

- 用户：`gbjk`
- 目录：`/home/gbjk/cora`
- 命令：`/home/gbjk/cora/cora-agent -config.file=/home/gbjk/cora/agent.yml`
- positions：`/home/gbjk/cora/positions.json`
- 上传入口：`http://public.internal:8088/v1/events:batch`

截至本 Handoff 重新核验，公网 health 返回：

```text
version:        v0.1.0-rc6-dirty
source digest:  7d69633fd551a0ee6bdecbd167f9fe35412e1627a8729ef02ee5a2b376ba278e
build time:     2026-07-15T02:31:58Z
SQLite schema:  5
status:         ok
write_failures: 0
```

这个 64 位 digest 是构建时对当前 Go 源码、module 文件和内嵌 Pack 的快照摘要，不是 Git
commit。目标 B 的报告必须同时记录 build version 和完整 digest，不能把 `3214ff1` 当作 rc6
源码身份。

## 4. 目标 A 已完成的真实闭环

生产 MCP 当前有五个工具：

```text
cora_list_attention
cora_get_problem
cora_record_outcome
cora_export_cases
cora_iteration_snapshot
```

目标 A 已完成：

- 显式按 `product_line + business_date + timezone` 读取全部 decision，包括 `ignore`。
- 在单个 SQLite 只读事务中汇总当日 occurrence、历史基线、节点和 Case ID。
- 冻结并逐页校验 case export high-water 与 SHA-256。
- 将原始业务日输入保存为 `iteration-snapshot.json`。
- 生成 triage、规则候选、shadow evaluation 和 `run.json` 哈希清单。
- 候选只能来自至少两条一致 handled Case 加 verified Atlas evidence；不会自动改 Pack。
- 空 `case_ids` / `node_counts` 已修复为稳定 JSON array，rc6 线上验收通过。

真实生产运行目录：

```text
out/iterations/gbjk-zhifu/2026-07-14/live-rc6-20260714-024308/
```

真实结果：

- 17 个 Problem，3,058 次 occurrence。
- `attention`：6 个 / 2,208 次。
- `ignore`：3 个 / 785 次。
- `observe`：8 个 / 65 次。
- 高频 ignore：`ig_07=609`、`ig_05=120`、`ig_06=56`。
- 前七日没有 Cora 历史窗口，因此 baseline 为 0；这是数据边界，不是增长率结论。
- case snapshot：`gbjk-zhifu:2`，through case ID 2，共 2 条 Case。
- 规则候选为 0：缺少历史基线、verified Atlas evidence 和足量一致 Case，安全 gate 正常生效。
- 8 个 artifact SHA-256、`cora.iteration-run.v1` 和
  `cora.iteration-snapshot.v1` Schema 全部通过。

关键产物：

```text
run.json
iteration-snapshot.json
case-snapshot.jsonl
case-snapshot-manifest.json
attention-incidents.json
triage-results.jsonl
rule-candidates.json
shadow-eval.json
shadow-eval.md
```

目标 B 可以读取这些本地不可变产物，但不能把“目标 A 跑完”直接等同于任何 Problem 已满足
retention 条件。本次没有候选规则、规则部署和观察窗口证据，因此当前应预期
`retention_eligible = 0`。

## 5. 目标 B 已确认的设计边界

先完整阅读：

1. `docs/ADR_001_PRODUCTION_FACT_LIFECYCLE.md`
2. `docs/HANDOFF_2026-07-15_RULE_ITERATION_AND_RETENTION.md`
3. `schemas/cora-closure-receipt.v1.schema.json`
4. `docs/PRODUCTION_READINESS.md`
5. `internal/cora/core.go` 中 schema v1–v5 migration

已经接受的原则：

1. 生产 SQLite 是热工作集；本地冻结 case、评估、规则和 closure receipt 是长期审计事实。
2. `resolved`、命中规则或存在人工备注都不足以授权清理。
3. `retention_eligible=true` 必须同时满足：
   - case snapshot 完整导出且 hash 验证；
   - 规则经过审核；
   - 冻结评估通过；
   - 对应 build 已部署；
   - 上线观察窗口结束并通过。
4. `problem_cases.problem_id → problems.id` 当前有外键；不能直接硬删 Problem。
5. B2 默认先形成保留稳定 identity 的 tombstone；B3 才可能讨论硬删除。
6. 复发必须复用 `product_line + service + fingerprint` identity，重新进入 `recurring`，并保留
   前一轮 closure/rule provenance。
7. 逻辑释放空间与 SQLite 文件物理缩小是两件事。DELETE 不等于文件缩小，VACUUM 不是 B0。

## 6. 分阶段边界

### B0：只读 retention audit——下一窗口只做这一阶段

目标：解释当前生产事实“为什么可保留/不可保留、占用多少、将来理论上能释放多少”，不写数据库。

建议实现：

```text
cmd/cora-retention-audit
internal/retention/
schemas/cora-retention-audit.v1.schema.json
docs/RETENTION_AUDIT.md
```

默认输入应是一份经过一致性备份和 `quick_check` 的 SQLite 文件，而不是在线主库。CLI 使用
SQLite `mode=ro` 打开，不能调用会运行 PRAGMA 写设置或 migration 的 `cora.OpenStore`。

推荐显式参数：

```text
-db <consistent-backup.db>
-product-line gbjk-zhifu
-iteration-root out/iterations
-closure-root out/closure-receipts
-output-root out/retention-audits
-run-id <stable-id>
```

推荐输出：

```text
out/retention-audits/<product_line>/<audit_run_id>/
  audit.json
  audit.md
  problem-decisions.jsonl
  run.json                 # 输入 identity 与 artifact hashes
```

报告至少包含：

- DB 文件 SHA-256、文件大小、schema version、capture time。
- `PRAGMA page_count/page_size/freelist_count`；明确 freelist 可复用页面和物理文件大小不同。
- `problems / trend_points / node_occurrences / node_trend_points / cora_decisions /
  problem_cases` 行数和可用时间范围。
- 按 Problem state、decision、handled、Case 存在性统计。
- 每个 Problem 的 eligibility 与全部阻塞原因，而不是只返回一个 bool。
- 闭环证据缺失、hash 不匹配、规则未部署、观察未通过、近期 occurrence、active/unhandled 等原因。
- 逻辑可释放量估算及估算方法；不能把估算写成精确物理回收值。
- JSON 和 Markdown 必须由同一结构化结果渲染，保证口径一致。

B0 的保守默认：closure receipt 缺失或任何 gate 无法验证时，一律 ineligible。当前真实数据应
大概率得到 `eligible=0`，但报告必须由查询和 artifact 验证推导，不能硬编码。

### B1：provenance index——本轮不要实现

在 B0 用真实备份证明查询键后，再决定 schema v6 是否增加最小 closure receipt digest/index。
生产库只保存必要引用和 digest，不复制本地完整 artifact。

### B2：compact/tombstone——本轮禁止

必须先有备份、quick_check、receipt、批次上限、单事务、幂等执行凭据和复发测试，才允许清理
旧 trend 或压缩 representative sample。Problem identity row 默认保留。

### B3：purge/VACUUM——本轮禁止

只有在外键迁移、恢复演练和 recurrence provenance 都通过后才讨论。DELETE、checkpoint 和
VACUUM 必须拆成不同维护动作。

## 7. B0 的第一轮完成标准

第一轮只有同时满足以下条件才算完成：

1. audit CLI 对 SQLite 使用真正只读连接，不触发 migration/WAL/checkpoint。
2. 产品线是强制边界；跨产品线数据不会进入明细或统计。
3. 固定测试 fixture 能覆盖 active、unhandled、resolved-without-receipt、receipt-invalid、
   receipt-eligible 等原因组合。
4. 相同 DB 与 artifact 输入产生字节稳定的 JSON/Markdown/JSONL。
5. closure receipt 及其引用的 artifact hash 均实际验证。
6. audit 前后数据库文件 SHA-256、mtime、size 不变。
7. 输出通过 JSON Schema，所有 artifact hash 可重新计算。
8. 在生产一致性备份上跑出真实报告；主库没有任何写入。
9. `go test ./...`、`go test -race ./...`、`go vet ./...`、`git diff --check` 全部通过。

如果暂时拿不到生产备份，先完成 fixture 闭环，但必须明确标记“未完成真实生产 B0 验收”，不能
只用当前 MCP snapshot 代替页面、WAL 和表级审计。

## 8. 生产备份边界

生产备份必须由用户授权并在 Server 主机执行。现有脚本契约：

```sh
deploy/scripts/backup-server.sh SERVER_BINARY DATABASE BACKUP_ROOT
```

如果脚本已经安全交付到 Server，当前生产等价参数：

```sh
deploy/scripts/backup-server.sh \
  /home/gbjk/zhouwei/cora/cora-server \
  /home/gbjk/zhouwei/cora/cora.db \
  /home/gbjk/zhouwei/cora/backups
```

没有部署脚本时，可使用二进制的同一备份能力，但 destination 必须是不存在的新文件：

```sh
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
/home/gbjk/zhouwei/cora/cora-server \
  -db /home/gbjk/zhouwei/cora/cora.db \
  -backup-db "/home/gbjk/zhouwei/cora/backups/cora-$timestamp.db"
/home/gbjk/zhouwei/cora/cora-server \
  -db "/home/gbjk/zhouwei/cora/backups/cora-$timestamp.db" \
  -check-db
```

脚本通过 Server 的一致性 SQLite backup 能力生成备份并执行 `check-db`。不要直接复制正在 WAL
模式写入的单个 `cora.db` 文件，也不要把 `auth.token` 同步到本地。备份文件可能包含代表样本和
敏感上下文，传输、权限和本地存放必须按生产数据处理，不能加入 Git 或普通聊天附件。

## 9. Plan 与子 Agent 建议

- 建议新窗口使用显式小计划，因为目标 B 跨 B0–B3，但计划中只允许 B0 处于 `in_progress`。
- 第一轮不建议多个子 Agent 同时改代码：当前 worktree 很脏，B0 会共同触及 CLI、SQLite 查询、
  Schema 和测试 fixture，文件边界容易重叠。
- 如果需要辅助，只委派只读、结构化任务，例如审阅 eligibility reason taxonomy 或独立检查
  JSON Schema；主 Agent 负责边界、实现、生产数据安全和最终验证。

## 10. 新窗口启动提示词

```text
我们继续 Cora，开始目标 B，但第一轮只实现 B0：只读 retention audit。

先完整阅读：
1. /Users/zhouwei/Documents/ClaraCore/apps/cora/docs/HANDOFF_2026-07-15_TARGET_B_RETENTION.md
2. /Users/zhouwei/Documents/ClaraCore/apps/cora/docs/ADR_001_PRODUCTION_FACT_LIFECYCLE.md
3. /Users/zhouwei/Documents/ClaraCore/apps/cora/schemas/cora-closure-receipt.v1.schema.json
4. /Users/zhouwei/Documents/ClaraCore/apps/cora/docs/PRODUCTION_READINESS.md

真实仓库是 /Users/zhouwei/Documents/ClaraCore/apps/cora。先核验 repo root、remote、branch、
dirty status 和线上 health。保护所有既有改动；不要覆盖或提交
config/cora-agent-gb-order-service02.yml，也不要擅自处理 docs/CORA_OVERVIEW.md。

目标 A 已经由 v0.1.0-rc6-dirty 在生产跑通。第一轮只实现一个对一致性 SQLite 备份运行的
只读 cora-retention-audit：输出严格 JSON、Markdown、逐 Problem 原因和 artifact hash，验证
closure receipt gate，证明审计前后 DB 文件未变化，并用测试 fixture 与真实生产备份验收。

禁止 DELETE、UPDATE、migration、checkpoint、VACUUM、自动规则发布和任何生产写入。若拿不到
生产备份，先完成 fixture 闭环并明确留下真实验收 blocker。方向明确后直接实现最小闭环，不要
扩展到 B1/B2/B3。
```

## 11. 收口时必须报告

新窗口结束时只报告实际真相：

- 改了哪些文件和契约。
- audit 使用了 fixture 还是真实一致性备份。
- DB 是否被证明字节、mtime、size 不变。
- eligible/ineligible 数量及主要原因。
- Schema、hash、test/race/vet/diff 的实际结果。
- 未验证内容和剩余风险。
- 是否仍停留在 B0；没有明确授权不得声称可以清理生产数据。

## 12. 本轮 B0 实现状态（2026-07-15）

已经实现 fixture 级 B0 闭环：

- `cmd/cora-retention-audit` 使用 SQLite `mode=ro&immutable=1` 打开指定一致性备份，不调用
  `cora.OpenStore`，也不触碰已有 WAL/SHM sidecar；
- `internal/retention` 对产品线内表事实、Problem 状态、decision、handled Case、closure receipt
  和引用 artifact hash 做保守审计；
- 输出 `audit.json`、`audit.md`、`problem-decisions.jsonl` 与 `run.json`，且相同输入字节稳定；
- 新增 `cora.retention-audit.v1` Schema 与 `docs/RETENTION_AUDIT.md`；
- fixture 覆盖 active、unhandled、resolved-without-receipt、receipt-invalid 和
  receipt-eligible，并验证审计前后 DB SHA-256、mtime、size 不变且不创建 SQLite sidecar；
- fixture 的 `audit.json` 已通过 Draft 2020-12 Schema 实例校验，输出与输入 artifact hash
  均可重新计算。

真实生产 B0 已通过 `/Users/zhouwei/Downloads/backups/cora-20260715T031923Z.db` 验收。最终
报告目录为：

```text
out/retention-audits/gbjk-zhifu/gbjk-zhifu-20260715T031923Z-b0-final/
```

- 数据库 SHA-256：`3c1942982ab5103236288402cdaedc64963c0a4860f4ecac22a5861eacd9e3c1`；
- build：`v0.1.0-rc6-dirty`；source digest：
  `7d69633fd551a0ee6bdecbd167f9fe35412e1627a8729ef02ee5a2b376ba278e`；
- schema v5，`quick_check=ok`，660 pages，page size 4096，freelist 0，WAL 0 bytes；
- `gbjk-zhifu` 有 20 个 Problem、4,561 次 occurrence，eligible 0、ineligible 20；
- 19 个 `new`、1 个 `acknowledged`，全部没有 handled Case，且没有 closure receipt；
- 3,814 个 trend row、4,058 个 node trend row 当前都不允许清理，逻辑可释放估算为 0；
- 第一次真实读取发现普通 `mode=ro` 会触碰已有 `.db-shm` mtime；随后改为
  `mode=ro&immutable=1`，补回归测试并重跑，最终 `.db/.db-shm/.db-wal` 的 SHA-256、size、
  mtime 全部不变；
- 最终 JSON 通过 Draft 2020-12 Schema，所有输出 hash 可重算，相同输入重跑四个文件逐字节一致。

B0 已完成，但结果明确不授权 compact 或 purge。下一步仍需先形成真实 closure receipt；是否进入
B1 provenance index 必须由新的明确目标决定，不能因为审计工具通过就直接进入 B2。

## 13. B0.1 在线 MCP 预检（2026-07-15）

为避免每次日常检查都手工复制数据库并运行 CLI，本地下一构建新增
`cora_retention_audit`：

- 在 Cora Server 当前 Store 的只读事务中执行，不更新 Problem、Case 或 SQLite schema；
- 显式要求 `product_line`，按 Problem ID 稳定分页；
- 返回全产品线汇总、逐 Problem 状态/decision/Case 计数和阻断原因；
- 只有 `resolved + handled Case` 会标记为 `forensic_audit_candidate=true`；
- 因 Server 当前没有 closure receipt provenance index，所有在线结果都保留
  `closure_receipt_verification_requires_offline_audit`，不会给出
  `retention_eligible=true`；
- 真正 compact/purge 前仍必须使用 `cora-retention-audit` 独立命令对一致性备份验证 receipt、
  artifact hash、quick_check 和数据库不变性。

该变化只需要下一次替换 `cora-server`；Agent 不需要更新，业务 JAR 完全不在此流程范围内。
