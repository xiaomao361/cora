# Cora 下一阶段 Handoff：规则迭代工作流与生产事实保留闭环

更新时间：2026-07-15

## 1. 新 Session 的目标

下一阶段只围绕两件互相依赖的事情展开：

1. 把“根据昨日 ERROR 更新规则和迭代模型”标准化为可重复、可审计、可回滚的工作流。
2. 把以下生命周期实现为安全闭环：

```text
发现 → 处理 → case → 形成规则 → 验证规则
  → 压缩/删除生产事实 → 复发时重新打开
```

这里的依赖关系必须保持明确：**规则迭代工作流先产生已导出、已验证、已部署的闭环凭据，
retention 才能据此判断生产事实是否允许压缩或删除。** 第一阶段禁止直接实现自动删除。

## 2. 当前真相快照

### 2.1 仓库边界

- 独立仓库：`/Users/zhouwei/Documents/ClaraCore/apps/cora`
- branch：`main`
- remote：`git@github.com:xiaomao361/cora.git`
- 本地 HEAD：`3214ff1`，`origin/main`：`1739512`
- 生产 Server：`v0.1.0-rc4`，commit
  `3214ff1d1a5e86432785640fadf194f55800a4b5`

新 Session 必须先重新执行 `git status --short`、`git log -3 --oneline --decorate` 和远端
HEAD 核验，不要假设本段状态仍然最新。

### 2.2 不能丢失的本地未提交工作

当前工作树包含两组已实现并通过测试、但尚未提交/部署的改动：

- 低频运行日志：进程启停、目标文件打开/轮转、批次投递/重试、Server 接收和非空 flush；
  不记录事件正文或凭据。
- Attention incident 读取归组：共享代表 trace ID 的 Problem 在 `cora_list_attention` 中归为
  一个只读 incident，底层 Problem、计数、状态和 case 不合并。

相关修改分布在 README、HANDOFF/生产文档、`cmd/cora-server`、`internal/agent` 和
`internal/cora`。已验证：

```text
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

以上检查当时全部通过。新 Session 仍须重跑，不得直接视为当前通过。

用户自己的未跟踪文件：

```text
config/cora-agent-gb-order-service02.yml
```

必须保留，不要覆盖、删除或误提交。

### 2.3 生产运行事实

截至 2026-07-15 约 08:10，`gb-order/service01` 与 `service02` 均已接入：

- Server `dropped_events=0`、`flush_failures=0`、`write_failures=0`。
- 累计约 3,293 个 flushed events。
- `service01`：约 11 个 Problem、1,561 次 occurrence。
- `service02`：约 16 个 Problem、2,099 次 occurrence。
- Redisson 与 Seata 高频 Problem 均正确聚合到 S1/S2 两节点。

当前观察到三个高频 `ignore`：

- 第三方订单回调失败：约 645 次，规则 `ig_07`。
- 用户支付密码错误：约 127 次，规则 `ig_05`。
- 商品信息同步延迟：约 62 次，规则 `ig_06`。

这暴露出后续规则迭代需要覆盖的一个问题：稳定 `ignore` 在频率异常升高时是否应临时升级为
`observe`。不要直接把三个规则全部改成 attention；先通过工作流对真实证据、频率基线和业务
含义进行复核。

### 2.4 当前数据边界

- 生产 Cora SQLite 定位为**热工作集**，不是永久历史仓库。
- 本地导出的 case、规则版本和冻结评估证据应成为长期事实源。
- Cora 当前不保存每条 ERROR；同类 ERROR 已折叠为 Problem，只保留累计/窗口事实与
  first/latest representative sample。
- 仍会持续增长的主要是 `trend_points`、`node_trend_points`、完整 case snapshot、样本和 WAL。
- `problem_cases.problem_id` 当前外键指向 `problems.id`，所以已有 case 的 Problem 不能直接
  硬删除；retention 设计必须明确 tombstone、外键迁移或导出后删除策略。
- SQLite `DELETE` 不会自动缩小文件，只释放页面供复用；物理回收需要受控 checkpoint 与
  VACUUM/incremental vacuum，不能在生产高峰直接执行。

## 3. 工作流 A：昨日 ERROR → 规则/模型迭代

### 3.1 定位

这是一个默认 T+1 运行的本地工作流，不是生产 Server 自动训练，也不是自动把候选规则上线。
Codex 负责边界、证据、验收和最终判断；Atlas 用于代码/依赖/发布事实查询，Cora MCP 用于生产
Problem、case 和 outcome 事实。每次查询必须显式指定 `product_line`，不同产品线不得混合。

当前“模型迭代”首先指 Cora Pack 的规则与经验版本迭代。LLM 灰区判断、case top-k 和统计模型
只能在 case 量与冻结评估集达到门槛后进入，不能用“模型迭代”名义绕过人工审核和 shadow eval。

### 3.2 标准输入

- 明确的业务日期与 `product_line`。
- `cora_list_attention` 当前 incident/Problem 视图。
- `cora_get_problem` 的样本、趋势、节点、related problems 和历史 case。
- `cora_export_cases` 冻结的 snapshot high-water、page hash 和完整分页结果。
- Atlas 中对应服务的代码、调用链、发布与依赖事实。
- 人工/Agent outcome：是否真问题、是否处理、根因、动作、责任边界。

禁止把 bearer token、原始敏感日志或未脱敏签名 URL 写入工作流产物。

### 3.3 标准步骤

1. **冻结输入**
   - 生成 `iteration_run_id`。
   - 记录业务日期、产品线、Cora build、Pack version、输入时间范围。
   - 通过 `cora_export_cases` 冻结 snapshot，校验所有 page hash。
2. **收敛待处理项**
   - 以 incident 为入口，不按包装层 Problem 重复调查。
   - 区分 attention、observe、ignore-frequency-escalation 和已处理复发。
3. **补足证据**
   - Cora 提供运行样本、节点和趋势。
   - Atlas 同步查询代码位置、上下游边界、历史 release；不能只凭异常文案猜根因。
4. **记录 outcome/case**
   - 必填：`is_real_problem`、`handled`、`root_cause`、`action`、`actor`。
   - “需要运营或业务开发处理”也是合法 action；不要求当前用户亲自修业务。
5. **生成规则候选**
   - 单个未经复核的 case 不直接生成规则。
   - 重复一致 case 或清晰稳定业务语义才进入 candidate。
   - 候选必须声明匹配字段、预期 decision、误伤风险和来源 case IDs。
6. **冻结评估**
   - 使用固定 snapshot 做 shadow eval。
   - 对比 baseline 与 candidate：attention/observe/ignore 迁移、已知真问题召回、噪音变化。
   - 高频 ignore 必须单独报告其窗口频率和升级结果。
7. **人工审核与发布**
   - 审核 candidate patch 和评估报告。
   - 更新 Pack version、manifest、文档和 release identity。
   - 构建新二进制；禁止生产自动激活未经审核的规则。
8. **上线后观察与闭环**
   - 比较发布前后 2h/24h/72h 的 Problem、节点和 decision 变化。
   - 生成 `closure_receipt`，记录规则已验证/回滚、case snapshot 和发布版本。

### 3.4 每次运行的标准产物

建议形成一个不可变目录：

```text
out/iterations/<product_line>/<business_date>/<iteration_run_id>/
  run.json
  case-snapshot.jsonl
  case-snapshot-manifest.json
  attention-incidents.json
  triage-results.jsonl
  rule-candidates.json
  candidate-pack.patch
  shadow-eval.json
  shadow-eval.md
  closure-receipt.json
```

`out/` 仍应保持本地、Git ignored；Git 只提交工作流代码、schema、经过审核的 Pack、精简报告和
文档，不提交生产 SQLite 或原始导出数据。

### 3.5 工作流验收标准

- 同一输入 snapshot 重跑得到相同候选与评估结果。
- 所有 case、规则和发布之间可双向追溯。
- Atlas 与 Cora 的证据边界清楚，结论能指出来源。
- 不自动激活规则，不跨产品线借用经验。
- 工作流失败时不会改生产状态，部分导出不能被标记为成功。
- 产出 `closure_receipt` 后，retention 才能把对应事实列为清理候选。

## 4. 工作流 B：生产事实压缩与删除闭环

### 4.1 生命周期

```text
new / recurring
  → investigated
  → case_recorded
  → rule_candidate
  → rule_validated
  → rule_deployed
  → retention_eligible
  → compacted
  → purged_from_production

任何阶段再次出现有效新事件
  → reopened / recurring
```

现有 Problem 状态仍是 `new / acknowledged / resolved / recurring`。上面的阶段首先应作为
retention/provenance 元数据设计，不能未经迁移直接替换现有状态机。

### 4.2 永远不能直接删除的情况

- Problem 仍是 `new`、`acknowledged` 或 `recurring`。
- `handled=false`。
- case snapshot 未完整导出或 hash 未校验。
- 没有经过审核并部署的规则版本。
- shadow eval 未通过或上线观察期未结束。
- 最近仍有 occurrence，或正在调查复发。
- 数据库备份/quick_check 不成功。

### 4.3 建议的分层保留策略

具体天数必须先由真实 retention audit 估算，不要直接硬编码。可用以下值作为讨论起点：

- outcome 后立即：`resolved` 从 attention 隐藏，但事实不删除。
- 7–14 天：规则候选、评估和上线观察窗口，保留代表样本。
- 30 天：已验证规则覆盖的 resolved Problem 可压缩 first/latest 样本，清理细粒度旧趋势；保留
  fingerprint、计数、时间、状态、rule/case provenance 和必要日级聚合。
- 90 天：若没有复发且 closure receipt 完整，可从生产热库 purge；本地长期保留精简 case、规则、
  评估与 closure receipt。

### 4.4 分阶段实现

#### B0：只读 retention audit（第一优先级）

先实现 dry-run，不做任何 DELETE：

- 每张表的行数、页面/字节估算、时间范围。
- `page_count`、`freelist_count`、WAL 大小和 schema version。
- 按原因列出 eligible/ineligible Problem、case、trend 数量。
- 估算逻辑可释放空间与物理可回收空间，明确两者不同。
- 输出机器可读 JSON 和人工可读 Markdown。

验收必须使用生产数据库的一致性备份或只读连接，不能在生产主库上做试删。

#### B1：补齐 provenance 与删除凭据

至少需要表达：

- case export snapshot ID、through case ID、page hash、exported/verified time。
- rule ID、Pack version、来源 case IDs、eval run ID、deployed build。
- closure receipt 状态、观察窗口和 retention eligibility。
- compacted/purged 时间与执行 run ID。

先写 schema/ADR，再决定这些字段落在 SQLite 新表、local manifest，还是两边各保存一份引用。

#### B2：安全压缩

- 先备份并 `quick_check`。
- 单事务、小批次、有最大行数限制。
- 先删除/聚合过期 trend 与 node trend。
- 再将 eligible resolved Problem 的大样本压缩成最小、脱敏 tombstone。
- 每次执行输出 before/after receipt；失败必须可重跑且不能产生半闭环状态。

#### B3：生产 purge 与空间回收

- 解决 `problem_cases → problems` 外键策略后才能删除 Problem。
- purge 与 VACUUM 分开：DELETE 负责页面复用，受控维护窗口才做物理缩容。
- 明确 WAL checkpoint、并发写入、磁盘余量和回滚流程。
- 复发 fingerprint 必须创建/恢复为 active Problem，并关联本地历史 case/rule provenance。

### 4.5 Retention 验收标准

- 默认 dry-run；执行删除必须显式开启。
- active/unhandled/未导出/未验证事实零误删。
- 备份恢复后数据、case export 和 rule provenance 可对账。
- 清理后 `quick_check`、health/readiness 和 MCP list/get/export 正常。
- 同 fingerprint 新事件能够重新打开，并保留“曾由哪条规则处理”的可追溯关系。
- 生产 SQLite 增长趋于平台化/可复用，而不只是一次性 VACUUM 后变小。

## 5. 两条工作流的共同协议

建议用一个版本化的 `closure-receipt` schema 连接 A 与 B：

```json
{
  "schema_version": "cora.closure-receipt.v1",
  "product_line": "gbjk-zhifu",
  "service": "gb-order",
  "fingerprint": "...",
  "case_snapshot_id": "...",
  "case_ids": [1, 2],
  "rule_id": "...",
  "pack_version": "...",
  "eval_run_id": "...",
  "deployed_build": "...",
  "validated_at": "...",
  "observation_ends_at": "...",
  "retention_eligible": false
}
```

`retention_eligible` 不能由“存在规则”单独决定；只有 case export、评估、部署和观察全部完成后
才能变为 true。所有字段都必须可从真实 artifact 或运行事实验证，不能只写人工备注。

## 6. 推荐实施顺序

1. 重新核验当前 dirty worktree，保护日志和 Attention incident 归组改动。
2. 先完成一份短 ADR/SDD：生产热库、本地长期事实源、closure receipt、外键与复发语义。
3. 定义 `iteration-run`、`rule-candidate`、`closure-receipt` JSON schema。
4. 用“昨日 ERROR”跑通一次**只读**规则迭代：冻结 export、incident triage、Atlas 对账、候选与
   shadow report；暂不自动改 Pack。
5. 实现 retention audit dry-run，并在生产一致性备份上验证估算。
6. 人工确认规则候选与 audit 报告后，再实现 rule promotion 和 B2 compact。
7. 至少经过一次 72h 上线观察、备份恢复演练和复发测试后，才进入 B3 purge。

不要先做 Web UI、MQ/Redis、高可用、全量服务接入或自动训练；它们都不是当前闭环的阻塞项。

## 7. 新 Session 的第一轮完成标准

第一轮不要同时实现全部工作流。建议只完成：

1. ADR/SDD 与三个 schema；
2. 一个可重跑的昨日 ERROR 只读 iteration run；
3. 一个只读 retention audit；
4. 基于真实生产 snapshot/备份的报告；
5. 完整测试，但不删除生产数据、不自动发布规则。

只有这五项都完成，下一轮才讨论 candidate Pack patch 和 compact mutation。

## 8. 新 Session 启动提示词

```text
我们继续 Cora，进入“规则迭代工作流 + 生产事实 retention”阶段。

先完整阅读：
1. /Users/zhouwei/Documents/ClaraCore/apps/cora/docs/HANDOFF_2026-07-15_RULE_ITERATION_AND_RETENTION.md
2. /Users/zhouwei/Documents/ClaraCore/apps/cora/docs/HANDOFF.md
3. /Users/zhouwei/Documents/ClaraCore/apps/cora/docs/CORA_V0.md
4. /Users/zhouwei/Documents/ClaraCore/apps/cora/docs/PRODUCTION_READINESS.md

真实仓库是 /Users/zhouwei/Documents/ClaraCore/apps/cora。先核验 repo root、remote、branch、status
和线上 Cora build。当前 dirty worktree 中已有运行日志与 Attention incident 归组改动，必须保护；
config/cora-agent-gb-order-service02.yml 是用户未跟踪配置，不要覆盖、删除或误提交。

本 Session 第一轮只做：
- 写清生产 SQLite 是热工作集、本地 case/规则库是长期事实源的 ADR/SDD；
- 定义 iteration-run、rule-candidate、closure-receipt schema；
- 用昨日真实 ERROR 跑一次只读规则迭代；
- 实现并运行只读 retention audit。

不要删除生产数据，不要自动激活规则，不要扩大到更多服务。Codex 负责证据边界和最终验收；
Cora MCP 查生产 Problem/case，Atlas 同步查代码/发布/依赖证据。先给出当前真相快照和最小闭环，
然后直接实现并验证。
```

## 9. 2026-07-15 目标 A 实现进展

目标 A 的只读代码闭环已实现：

- `cmd/cora-iterate` 通过 Cora HTTP/MCP 读取 build/schema、业务日迭代快照、当前 attention
  incident、Problem detail 和稳定 case export；不注册或调用任何生产写工具。
- 新增只读 `cora_iteration_snapshot`，在单个 SQLite 只读事务中按显式业务日期和 timezone
  汇总包括 `ignore` 在内的全部 decision，并使用前 N 个完整业务日计算频率 baseline。
- 每页 case export 都重新计算 Server 同口径 SHA-256，snapshot high-water 或 cursor 漂移会失败。
- Atlas 证据通过显式产品线/服务/fingerprint 的 `cora.code-evidence.v1` JSONL 输入；缺失时在
  triage 中保持 `not_collected`，不能伪造代码或 release 结论。
- 只有两条以上一致、handled case，加上 verified Atlas evidence，才生成 `proposed` candidate；
  高频 ignore 只进入复核清单，不能绕过 gate。
- candidate 仅在内存中 shadow evaluate；输出 problem/occurrence transition、已知真问题 recall、
  noise escalation 和 RFC 6902 风格待审核 patch，不修改 Pack。
- 原始 `iteration-snapshot.json` 与 case snapshot 一并固化并由 `run.json` 记录 SHA-256；所有文件
  先写临时目录，完整成功后才原子发布到 `out/iterations/...`；`out/` 已加入 Git ignore。

自动化验证已覆盖：相同冻结输入字节级重跑一致、分页 hash 失败不发布、真实 httptest Cora
Server + MCP 端到端只读运行、产品线边界、schema 自检和 Pack manifest/hash 对账。

当前 Codex 可通过已配置 MCP 连接 `https://cora.gbgoodness.com/mcp`，但线上版本尚未部署
`cora_iteration_snapshot`，本地 CLI 也仍需独立可读的 bearer token file。因此当前尚未形成
2026-07-14 的真实生产报告。部署并验证新 Server 后，使用生产域名运行：

```sh
go run ./cmd/cora-iterate \
  -server-url https://cora.gbgoodness.com \
  -auth-token-file /secure/path/auth.token \
  -product-line gbjk-zhifu \
  -business-date 2026-07-14 \
  -timezone Asia/Shanghai \
  -code-evidence /secure/path/atlas-evidence.jsonl
```

运行和候选审核说明见 `docs/RULE_ITERATION_WORKFLOW.md`。目标 B retention 尚未开始；目标 A
剩余生产闭环是构建/部署新 Server、验证第五个 MCP 工具，再生成并审核真实业务日报告。
