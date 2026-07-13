# Clarion 项目启动 Handoff

更新时间：2026-07-13

## 1. 项目是什么

Clarion 是面向中小团队的 Agent First 错误关注框架。它不替代 Sentry、APM
或基础设施监控，而是把持续涌入的 Error 收敛为少量候选 Problem，让 Agent
回答一个问题：

> 现在，有没有值得我关注的新问题？

完整产品讨论和已经收敛的架构决策见：

`/Users/zhouwei/Documents/ClaraCore/seeds/lightweight-error-attention-platform.md`

核心命名：

- **Clarion**：确定性 Framework，负责接收、指纹、聚合、状态与出口。
- **Cora**：判断 Core，负责关注度、语义归类、建议和产品线经验。
- Clarion 先以纯规则模式运行；Cora 可以后插、独立升级和回滚。

## 2. 真实边界

### 代码边界

独立嵌套仓库：

`/Users/zhouwei/Documents/ClaraCore/apps/clarion`

ClaraCore 根目录是共享 workspace，不是 Git 仓库。后续所有 Git 操作必须在
`apps/clarion` 内执行。

### 运行时边界

当前是一个 Go 单进程：HTTP ingest、指纹计算、SQLite 和查询 API 都在同一
二进制中。没有 MQ、Redis 或其他常驻依赖。

### 数据与事实边界

- SQLite 是当前运行事实源，启用 WAL。
- Error 是输入事实；Problem 是确定性指纹归并后的候选问题。
- 所有查询和未来经验必须显式按 `product_line` 隔离。
- Framework 宁拆勿合；跨指纹语义合并留给 Cora。
- 当前数据库文件是本地运行产物，已在 `.gitignore` 中排除。

## 3. 当前实现

已经完成第一个可执行闭环：

```text
批量 HTTP 上报
→ Java 异常指纹计算
→ 按 product_line + fingerprint upsert
→ 重复事件只增加 count
→ 保留 first/latest 两个代表样本
→ HTTP 查询候选 Problem
```

当前接口：

- `GET /healthz`
- `POST /v1/events:batch`，每批 1–500 个事件，body 上限 2 MiB
- `GET /v1/problems?product_line=<line>`
- `GET /v1/trends?product_line=<line>&fingerprint=<fingerprint>`

关键文件：

- `cmd/clarion/main.go`：进程入口。
- `internal/clarion/clarion.go`：领域类型、指纹、SQLite 和 HTTP handler。
- `internal/clarion/clarion_test.go`：重复归并及产品线隔离测试。
- `README.md`：运行和 curl 示例。

## 4. 已验证事实

在 2026-07-13 已执行：

```sh
go test -v ./...
go vet ./...
git diff --check
```

全部通过。

同时启动真实 HTTP 服务并上报两条 OOM：请求 ID 与源码行号不同，但异常类型、
logger 和应用方法栈一致。查询结果为一个 Problem，`count = 2`，且首个和最新
代表样本分别保留。

## 5. 尚未实现

- EWMA 基线和频率突变事件。
- 问题状态机与影响面扩大事件。
- Cora v0 规则判定及 Framework/Core contract。
- MCP 拉取与处理结果回写。
- case base、Webhook、Web UI 和发布部署。

当前代码已用内存窗口兑现“错误洪峰只转化为低频落库”的第一版承诺；尚未用
压测证明具体吞吐、内存上限和故障恢复表现。

## 6. 当前 Git 状态

- 已在 `apps/clarion` 执行 `git init`，默认分支为 `main`。
- 没有配置 remote。
- 没有 commit；当前项目文件全部是 untracked。
- `go.mod` 暂用 module path `github.com/claracore/clarion`。创建远端时需要确认
  实际 owner；如不是 `claracore`，应在首次提交前同步修改 module path 和 import。

创建远端后建议先做：

```sh
cd /Users/zhouwei/Documents/ClaraCore/apps/clarion
git remote add origin <REMOTE_URL>
git status --short
go test ./...
```

只有用户明确要求时才 commit/push。

## 7. 2026-07-13 续开发结果

已完成此前建议的“内存窗口聚合 + 趋势点”闭环：

- ingest 进入有容量上限的并发安全内存聚合器。
- 默认每 10 秒 flush，同一产品线和指纹每个窗口只做一次 Problem upsert。
- flush 在一个 SQLite 事务中同时写 Problem 与趋势点。
- 满载时保留已有活跃指纹并丢弃新指纹，`/healthz` 暴露累计
  `dropped_events`。
- SIGINT/SIGTERM 停止 HTTP 接收后执行最多五秒的最终 flush。
- `/v1/trends` 可按产品线和指纹读取趋势点。

本轮已通过 `go test ./...`、`go test -race ./...` 和 `go vet ./...`。

随后已完成 schema migration/version 闭环：

- 使用 SQLite `PRAGMA user_version`，当前 schema 版本为 2。
- v1 创建 Problem 表；v2 创建趋势表和索引。
- 上一轮产生的无版本数据库可原地升级且保留数据。
- 重复启动不会重复迁移；高于程序支持版本的数据库会拒绝打开。
- 每个版本在事务中执行，失败时 DDL 与版本号一起回滚。
- 测试覆盖新建、旧库升级、幂等重开、拒绝新版本和失败回滚。

随后已完成聚合器性能基线：

- 高重复 ingest 约 110–113 万 events/s；10,000 活跃指纹约 105 万 events/s。
- 10,000 活跃指纹保留约 11.92 MiB Go heap。
- 真实 HTTP 进程 RSS 快照：基线 18.0 MiB、窗口装满 36.4 MiB、flush 后
  47.1 MiB。
- 10,000 指纹 flush 为 255–280 ms；瞬时分配约 52.5 MiB。
- 10,000 events / 100 fingerprints 确认只产生 100 个 Problem 更新和 100 个
  趋势点。
- `/healthz` 已暴露 pending、dropped、flush 次数/失败、flushed events 与最近
  flush 时延。
- 详细复现命令与判断见 `docs/PERFORMANCE_BASELINE.md`。

## 8. Logback Appender 闭环

已实现独立 Maven artifact `io.clarion:clarion-logback:0.1.0-SNAPSHOT`：

- 只采集 ERROR 及以上日志；业务线程只做事件快照和有界队列 `offer`。
- 队列满时丢旧留新；网络失败或非 2xx 响应不重试，统一累计 dropped。
- 后台 daemon 线程批量 POST，单批上限与服务端一致为 500，并设置连接、请求和
  停止排空超时。
- 暴露 sent、dropped、failed batch 和 queued 四个进程内计数 getter，JMX/指标
  桥接留给后续接入层。
- `integrations/logback/example` 是最小 Java 示例；INFO 留在本地，带异常的 ERROR
  自动上报。

真实端到端验证已启动 Go Clarion、运行 Java 示例并查询 API：Appender 输出
`sent=1 dropped=0 failed_batches=0`，Go 端形成一个
`java.lang.IllegalStateException` Problem，`flushed_events=1`。

## 9. 下一条最小闭环

建议下一轮定义并实现 Cora v0 的 Framework/Core contract 与纯规则 adapter，先
覆盖“新指纹、高频突增、影响面扩大”中的一个可验证判断，不同时做 MCP 或 Web UI。

Logback Appender 尚未发布到远端 Maven 仓库，也未在真实业务服务中接入；当前只以
本地 artifact、单元测试和最小 Java 进程完成验证。

## 10. 当前主要风险

- 内存窗口在进程崩溃或最终 flush 超时后会丢失；这是当前以低写放大换取吞吐的
  明确容错边界。
- 满载时 HTTP 仍返回整批 accepted；精确的逐批 dropped 回执尚未定义，调用方需
  通过 `/healthz` 观察累计值。
- 指纹算法目前只做了最小 Java 栈过滤，尚未支持 cause chain、配置应用包前缀
  或差异样本槽。
- API 没有鉴权；只能用于本机或可信内网实验。
- RSS 仅做了离散采样而非连续峰值采集；换部署硬件后仍需重测。
- Appender 的计数目前只在 Java 进程内暴露 getter，尚未接入 Micrometer/JMX；若
  宿主不采集，队列丢弃只能通过应用侧诊断观察。

## 11. 新 Session 启动提示词

```text
我们继续开发 Clarion。

先阅读：
1. /Users/zhouwei/Documents/ClaraCore/AGENTS.md
2. /Users/zhouwei/Documents/ClaraCore/seeds/lightweight-error-attention-platform.md
3. /Users/zhouwei/Documents/ClaraCore/apps/clarion/docs/HANDOFF.md
4. /Users/zhouwei/Documents/ClaraCore/apps/clarion/README.md

真实仓库是 /Users/zhouwei/Documents/ClaraCore/apps/clarion，不是 ClaraCore 根目录。
先检查 repo root、remote、status 和现有测试，保留当前未提交改动。

Logback Appender 已完成。下一轮先定义 Cora v0 的 Framework/Core contract，并
实现一个最小纯规则 adapter；不要同时扩展到 MCP 或 Web UI。
```
