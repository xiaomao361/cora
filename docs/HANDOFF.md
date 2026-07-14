# Cora Handoff

更新时间：2026-07-13

## 1. 当前产品定义

Cora 是给资源有限团队使用的轻量错误观测系统。它不要求业务服务增加依赖，直接读取
现有 Logback 文件，把 ERROR 洪峰收敛成少量 Problem，并用可解释的产品线经验判断哪些
值得关注。

名称已经统一为 Cora，不再维护两套产品概念：

- **Cora**：整个产品和仓库。
- **Cora Agent**：每台应用服务器部署一个，监听该机器上的多个 Java 服务日志。
- **Cora Server**：中心接收、聚合、存储和查询服务。
- **Cora Core**：Server 内的纯决策内核。
- **Cora Pack**：按产品线隔离、可版本化的规则/模型包。

Java SDK 和 Logback Appender 已从仓库移除。当前唯一生产接入路径是 Cora Agent；未来
只有在业务开发能够配合发布时，才重新设计应用内 SDK，不继承旧代码兼容负担。

## 2. 代码、运行和数据边界

独立 Git 仓库：

```text
/Users/zhouwei/Documents/ClaraCore/apps/cora
```

后续 Git 命令必须在该目录执行，不要在 ClaraCore 根目录操作。

当前运行形态：

```text
每台应用服务器 1 个 Cora Agent
  -> 监听该节点多个显式日志文件
  -> 内网 POST /v1/events:batch
  -> 公共 ECS 上的 Cora Server
  -> Cora Core decision
  -> SQLite Problem / Trend / Decision
```

SQLite 是当前 Server 事实源并启用 WAL。positions 是 Agent 的已确认字节位置事实源。
所有产品线经验必须显式按 `product_line` 隔离。

目标生产拓扑来自 Atlas：贯柏应用位于三个双节点部署组，共六台应用服务器：

- `gateway01/gateway02`
- `service01/service02`
- `sys01/sys02`

计划每台机器只部署一个 Agent；公共服务器上的 Server 只通过内网地址接收，不暴露公网。

## 3. 当前代码地图

- `cmd/cora-server/main.go`：Cora Server 入口。
- `cmd/cora-agent/main.go`：单文件参数与 Promtail 风格 YAML Agent 入口。
- `cmd/cora-eval/main.go`：Cora Pack shadow evaluator。
- `internal/cora/core.go`：Event、fingerprint、SQLite、聚合器和 HTTP API。
- `internal/cora/decision.go`：Cora Core、Pack 加载和规则判断。
- `internal/cora/eval.go`：历史 CSV 只读评估与脱敏报告。
- `internal/cora/experience/gbjk-zhifu.cora-v0.json`：贯柏直付经验包。
- `internal/agent/`：tail、parser、positions、batch/retry、多目标和健康检查。
- `internal/auth/token.go`：Server/Agent 共用的 token-file 读取与校验。
- `config/cora-agent-qikang.example.yml`：18 目标多日志示例。
- `config/cora-agent-canary.example.yml`：单节点 Supervisor 灰度配置。
- `deploy/supervisor/`：Server 与 Agent 的 Supervisor program 配置。
- `deploy/README.md`：Linux 构建、灰度、备份和回滚说明。
- `docs/VISION_ALIGNMENT.md`：原始愿景与当前实现的逐项对照、MCP/case/Core 防偏移边界。
- `config/cora-base-v0.json`：Core/Pack 版本清单。
- `schemas/`：decision、evaluation row 和 feedback JSON Schema。
- `reports/cora-shadow-eval/`：确定性评估证据。

## 4. 已实现的 Server/Core 能力

- `POST /v1/events:batch`：每批 1–500 个事件，body 最大 2 MiB。
- Java fingerprint：异常类型、logger 和前五个应用栈帧；无应用栈时使用归一化 message。
- 有上限的并发内存聚合窗口；默认 10 秒、10,000 活跃 fingerprint。
- SQLite schema migration，当前版本 v4；v3 数据原位迁移并保留已有 Problem、trend 和 decision。
  旧代表样本里的历史节点分布不可可靠反推，因此节点表从升级后的新事件开始累计。
- Problem identity 为 `product_line + service + fingerprint`，同指纹跨服务不再误合并。
- Problem count、first/latest representative sample、trend point、节点事实和 Cora decision 同事务写入。
- `node_occurrences` 保存累计节点分布，`node_trend_points` 保存逐窗口节点趋势；节点优先读取
  `labels.node/deployment_group`，兼容现有 `labels.server/group`，缺失节点归入 `unknown`。
- 历史事件乱序安全：`first_seen/last_seen` 及代表样本按完整事件时间维护，不依赖到达顺序。
- `/v1/problems`、`/v1/attention`、`/v1/trends`、`/v1/node-occurrences`、
  `/v1/node-trends`、`/healthz`；需要定位单个 Problem 的查询显式要求 `service`。
- 除 `/healthz` 外的所有 Server 路径都受 bearer token 保护，因此未来 `/mcp` 默认继承鉴权；
  health 仍只应位于私网监听。
- Server 默认监听收紧为 `127.0.0.1:8080`，无 token 时必须显式使用仅供本地开发的
  `-allow-unauthenticated`；生产 Supervisor 配置要求显式私网 IP 和 token file。
- Core 无法判断或返回非法 decision 时 fail-open 为 `observe`，不会阻断事实落库。
- `gbjk-zhifu` Cora Pack 共 130 条规则：27 attention、41 observe、62 ignore。
- 非贯柏产品线不能继承贯柏经验，默认 `observe`。

当前 decision 仍是 `attention / observe / ignore`。讨论中倾向未来把 `ignore` 改成语义更
清楚的 `suppress`，但本轮 identity/node 变更没有改变 decision schema、Pack 或历史报告语义。

当前 Core 接口可替换，但实现仍把 JSON Pack embed 进 Server 并只在进程启动时加载；规则
变化仍需重新构建二进制。原始目标不是泛化的在线自学习，而是规则快路径 + LLM 灰区判断 +
产品线 case 检索 + Agent 结果回写 + 人工审核的规则结晶；小模型只在 case/eval gate 达标后
进入。当前只实现第一层规则，case 持久化/检索、LLM adapter、结晶和热加载均未实现。

## 5. 已实现的 Agent 能力

- 一个进程并发跟随多个显式文件目标。
- 支持生产 Logback pattern，重建 ERROR 与多行 Java stacktrace。
- 提取 timestamp、trace ID、thread、logger、method、line、message、exception type 和 source。
- 每目标独立 16 KiB breadcrumb ring：有 trace 取前 30 秒最多 20 条；无 trace 按 thread
  取前 5 秒最多 5 条；不等待 ERROR 后的普通日志，轮转 reopen 时保留 ring。
- 上传前统一脱敏 ERROR message、stacktrace、breadcrumbs 和 labels：覆盖 Authorization、
  token/password/cardNo 类 key、手机号和 18 位身份证模式。
- thread/method/line/breadcrumbs 不参与 fingerprint，只随 first/latest representative sample 保存。
- 原子 `0600` positions；共享 store 并发安全。
- 只有 Server 2xx 后才提交 offset。
- 连接失败、429、5xx 有界指数退避；耗尽后退出交给 Supervisor 重启。
- YAML `clients[].bearer_token_file` 和 CLI `-auth-token-file` 均可读取 token，secret 不进入
  Supervisor command line；请求通过 `Authorization: Bearer` 发送。
- rename/reopen 与 copy-truncate 轮转检测。
- 默认新文件从末尾开始；历史回放必须显式 `from-start`/`beginning`。
- 单事件默认最多 256 KiB；JSON batch 默认最多 1.5 MiB、100 events。
- YAML 模式提供 `/healthz` 和 `/readyz`。
- `${ENV_VAR}` 配置展开；严格拒绝 Loki endpoint、重复路径和 glob。

交付语义是 at-least-once：Server 已接收但响应丢失时可能重复。当前没有 event ID 去重。

## 6. 真实验证证据

### 多行错误闭环

本地真实启动 Agent -> Server -> Core：多行 Java 异常被正确解析；相同异常不同 trace ID
归并到同一 fingerprint；INFO 被过滤；Agent 重启后从已确认 offset 继续，没有重发已确认
内容。

### 多目标闭环

一个 YAML 同时跟随两个文件，Agent health/ready 均正常，共享 positions 保存两个独立
offset，Server 形成两个不同 service 的 Problem。企康事件只得到未训练产品线 observe，
没有错误继承贯柏规则。

### 多服务 identity 与双节点事实闭环

相同产品线、相同 fingerprint 的 `gb-order` 和 `gb-payment` 被保存为两个独立 Problem；
同属 `gb-order` 的 `service01/service02` 仍聚合为一个 Problem，同时累计为两个节点分布和
两个逐窗口 node trend。HTTP 节点查询可按 `product_line + service + fingerprint` 精确读取。
回归同时覆盖现有配置的 `server/group` 标签兼容和 v3 -> v4 数据迁移。

### Breadcrumbs 与脱敏闭环

真实 Agent -> HTTP 接收端回归验证：ERROR 只附加同 trace 的前置 INFO，不混入同 thread 的
其他 trace；thread/method/line 正确上传；ERROR message、stacktrace、breadcrumb 和敏感 label
在网络请求前均已脱敏。单元回归覆盖 trace/thread 时间窗与条数上限、16 KiB byte bound、
中英文敏感模式及 fingerprint 不受新增上下文字段影响。

### Supervisor 部署安全闭环

真实 HTTP 回归覆盖缺失/错误 token 返回 401、正确 token 正常访问、`/healthz` 无鉴权；Agent
重试闭环验证每次请求都携带 bearer token。token file 拒绝空值和空白字符。Linux amd64
Agent/Server 可构建为静态 ELF。Supervisor program、单节点 canary YAML、私网监听、目录权限、
SQLite/positions 备份与 symlink rollback 已形成交付文档；尚未连接真实服务器执行灰度。

### 原始愿景对照

当前已兑现轻量采集、洪峰收敛、代表样本、产品线隔离、节点事实和上下文；Agent First 的
主界面 MCP、处理结果回写、case base、LLM 灰区判断、Problem 状态变化事件仍缺失。Server
当前没有 MCP。技术 canary 可验证采集与存储，但完整产品 canary 必须包含“Agent MCP 拉取 ->
调查 -> 四字段结果回写 -> case 入库”的闭环。详见 `docs/VISION_ALIGNMENT.md`。

### Loki 历史导入与压力测试

真实混合日志压力测试结果：

- 文件 100,211,698 bytes、169,057 行。
- Agent 解析并提交 16,434 个 ERROR events。
- 最终 offset 与文件大小一致，lag 为 0。
- 观测最大 lag 约 5.23 MiB，最大追赶速度约 2.69 MiB/s。
- Agent 观测 RSS 峰值约 19.3 MiB；Server 约 18.0 MiB。
- dropped events = 0，flush failures = 0，pending fingerprints = 0。
- 混合日志形成 40 个 Problem；所有 Problem 均满足 `first_seen <= last_seen`。

采样从文件约 75 MiB 时才启动，因此 CPU/RSS 是后半程观测峰值，不是完整 100 MiB 导入
的绝对峰值。这一轮日志混合多个服务且统一标成 `gb-order`，只能作为压力/稳定性证据，
不能用于 Cora 准确率结论。

### Core shadow evaluation

现有 1,404 行旧训练 CSV 的确定性评估：31 attention、559 observe、814 ignore，decisive
coverage 60.2%。但 98.6% 行重复近似 fingerprint、没有完整时间、1,403/1,404 行缺异常栈，
因此只能作为规则基线，不能声称统计模型质量。历史权重没有加载。

## 7. 已确认的下一版架构决策

### 六台机器部署

- 六台应用服务器各一个 Cora Agent。
- 每个 Agent 监听本机 3 个或更多 Java 服务。
- 每个目标必须明确 `product_line/service/node/deployment_group/environment/source`。
- 双节点相同服务聚合成同一 Problem，同时保留节点级出现次数。
- Cora Server 部署在公共 ECS，但入口仅走内网。
- 生产进程统一由 Supervisor 管理，不使用 systemd。

### 上下文策略

不上传完整 trace 的所有日志，也不重新实现 Loki/APM。下一版采用有界 breadcrumbs：

- ERROR 本身和完整 stacktrace 始终上传。
- 有 trace ID：同一 trace 最近 30 秒、最多 20 条前置日志。
- 无 trace ID：同一 thread 最近 5 秒、最多 5 条，作为弱 fallback。
- breadcrumbs 默认最大 16 KiB；每个目标环形缓冲严格有界。
- 不等待 ERROR 后的普通日志；异常堆栈仍读到下一个 Logback header。
- breadcrumbs 不参与 fingerprint，只保存在 first/latest representative sample。
- Agent 上传前需要按 key/模式脱敏 Authorization、token、password、手机号、身份证、
  cardNo 等敏感信息。
- 跨服务关联由 Server 按 trace ID 完成；Agent 不跨文件拼装调用链。

### 边缘过滤策略

第一阶段所有 ERROR 都上传，不把 Core `ignore` 等同于 Agent drop。已知噪音仍可能通过频率
突增暴露第三方故障。运行一段时间后，只对稳定且明确标记 `edge_safe` 的规则做本地聚合：
发送代表样本加 occurrence count/window，而不是静默硬丢弃。

## 8. 下一轮必须先解决的问题

按顺序执行：

1. **MCP + case 最小闭环**：Server 同进程提供 Streamable HTTP MCP；先实现按产品线拉关注
   问题、读取 Problem 上下文、四字段结果回写，并把结果保存为不可变 case。
2. **真实 canary**：在拿到 Server 私网 IP、首台 Agent 主机、实际日志路径和 Supervisor
   include 目录后，部署 1 Server + 1 Agent。技术 canary 可先验证采集；产品 canary 需同时
   验证 Agent 通过 MCP 拉取和回写。
3. **Core v0 完整管道**：规则快路径之外，为灰区增加 LLM + case top-k 检索；处理结果即时
   改善检索，重复一致 case 只生成待人工审核的规则候选。
4. **运行指标**：Agent 暴露每目标 lag、sent、retry、failure、parse/drop 和 rule suppression。
5. **重复与长故障**：设计 event ID/幂等；Agent 当前不读取 `.gz` 历史，Server 长时间不可用
   时必须保证活动/未压缩日志保留期覆盖恢复窗口。

部署代码已具备技术 canary 条件，但按 Agent First 原始目标，下一本地闭环应先补 MCP + case，
而不是 UI、告警渠道或单纯的 Pack 热加载。

## 9. Git 与交接状态

- standalone Git repo，branch `main`。
- 没有 remote。
- 现有首个本地提交：`474a29c`（旧名称时期的 bootstrap commit）。
- Cora Core、evaluation、Agent、乱序修复、整体重命名、Java 集成移除、service-scoped
  identity、节点事实、breadcrumbs、脱敏、bearer 鉴权、Supervisor 部署和愿景对照已作为
  当前本地 checkpoint 整体提交；以 `git log -1` 为准。
- 当前没有 remote，因此未 push。

新 Codex 项目启动提示词：

```text
我们继续开发 Cora。

先完整阅读：
1. /Users/zhouwei/Documents/ClaraCore/apps/cora/docs/HANDOFF.md
2. /Users/zhouwei/Documents/ClaraCore/apps/cora/README.md
3. /Users/zhouwei/Documents/ClaraCore/apps/cora/docs/CORA_AGENT_V0.md
4. /Users/zhouwei/Documents/ClaraCore/apps/cora/docs/CORA_V0.md

真实 repo 是 /Users/zhouwei/Documents/ClaraCore/apps/cora。先确认 repo root、remote、status，
保留全部未提交改动。当前唯一接入路径是 Cora Agent，不要恢复 Java SDK/Logback Appender。

下一轮先实现 MCP + case 最小闭环：Server 同进程 Streamable HTTP MCP，提供按产品线拉取
关注问题、读取 Problem 上下文、四字段处理结果回写；回写保存为不可变产品线 case。保持
bearer 鉴权，不另拆服务，不先做 UI/Webhook，也不让在线结果自动改写生产规则。
```
