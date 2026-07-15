# Cora Handoff

更新时间：2026-07-15

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

Git remote：

```text
origin  git@github.com:xiaomao361/cora.git
```

当前运行形态：

```text
每台应用服务器 1 个 Cora Agent
  -> 监听该节点多个显式日志文件
  -> http://public.internal:8088/v1/events:batch
  -> 公共服务器上的 Cora Server（172.22.245.1:8088）
  -> Cora Core decision
  -> SQLite Problem / Trend / Decision / Case
  -> public-web Nginx（172.22.244.253）反向代理
  -> https://cora.gbgoodness.com/mcp 供授权客户端拉取、调查和回写
```

SQLite 是当前 Server 事实源并启用 WAL。positions 是 Agent 的已确认字节位置事实源。
所有产品线经验必须显式按 `product_line` 隔离。

目标生产拓扑来自 Atlas：贯柏应用位于三个双节点部署组，共六台应用服务器：

- `gateway01/gateway02`
- `service01/service02`
- `sys01/sys02`

计划每台机器只部署一个 Agent。Server 进程不直接暴露公网；外部授权入口只经过 public-web
Nginx 和 `https://cora.gbgoodness.com`。

截至 2026-07-15，公共服务器上的 Cora Server 和监听 `gb-order/service01/service02` 的 Agent
已接入。远程只读 canary 已验证 health/readiness、线上四工具 MCP、attention 拉取和 Problem 详情；随后通过
MCP 写入首条未处理完成的真实 outcome，并重复导出稳定的 `gbjk-zhifu:1` case snapshot。尚未
完成 build identity、positions 推进和 72 小时稳定性验收，因此不能把当前连接成功等同于完整
生产 canary。

## 3. 当前代码地图

- `cmd/cora-server/main.go`：Cora Server 入口。
- `internal/serverconfig/`：Server 严格 YAML 加载、默认值和校验。
- `cmd/cora-agent/main.go`：单文件参数与 Promtail 风格 YAML Agent 入口。
- `cmd/cora-eval/main.go`：Cora Pack shadow evaluator。
- `cmd/cora-canary/main.go`：只读验证 Server health/readiness 与 MCP list/get 的生产探针。
- `cmd/cora-iterate/main.go`：T+1 只读规则迭代入口。
- `internal/cora/core.go`：Event、fingerprint、SQLite、聚合器和 HTTP API。
- `internal/cora/problem_lifecycle.go`：Problem 状态机、详情读取和不可变 case。
- `internal/cora/mcp.go`：官方 Go SDK 的 Streamable HTTP MCP 六工具入口（线上部署仍是四工具版本）。
- `internal/cora/iteration_snapshot.go`：按产品线、业务日和时区生成全 decision 只读快照。
- `internal/iteration/`：冻结 case、汇总、候选 gate、shadow evaluation 和不可变产物。
- `internal/cora/decision.go`：Cora Core、Pack 加载和规则判断。
- `internal/cora/eval.go`：历史 CSV 只读评估与脱敏报告。
- `internal/cora/experience/gbjk-zhifu.cora-v0.json`：贯柏直付经验包。
- `internal/agent/`：tail、parser、positions、batch/retry、多目标和健康检查。
- `internal/auth/token.go`：Server/Agent 共用的 token-file 读取与校验。
- `config/cora-agent-qikang.example.yml`：18 目标多日志示例。
- `config/cora-agent-canary.example.yml`：单节点 Supervisor 灰度配置。
- `config/cora-server.example.yml`：单二进制 Server 的生产配置示例。
- `deploy/supervisor/`：Server 与 Agent 的 Supervisor program 配置。
- `deploy/README.md`：Linux 构建、灰度、备份和回滚说明。
- `deploy/scripts/`：带 build identity 的发布构建、SQLite 一致性备份、positions 备份。
- `docs/VISION_ALIGNMENT.md`：原始愿景与当前实现的逐项对照、MCP/case/Core 防偏移边界。
- `docs/PRODUCTION_READINESS.md`：允许短暂不可用和不完整发现的 v0 生产契约与验收门槛。
- `config/cora-base-v0.json`：Core/Pack 版本清单。
- `schemas/`：decision、evaluation、case、iteration snapshot/run、候选与 closure JSON Schema。
- `reports/cora-shadow-eval/`：确定性评估证据。

## 4. 已实现的 Server/Core 能力

- `POST /v1/events:batch`：每批 1–500 个事件，body 最大 2 MiB。
- Java fingerprint：异常类型、logger 和前五个应用栈帧；无应用栈时使用归一化 message。
- 有上限的并发内存聚合窗口；默认 10 秒、10,000 活跃 fingerprint。
- SQLite schema migration，当前版本 v5；v3/v4 数据原位迁移并保留已有 Problem、trend 和 decision。
  旧代表样本里的历史节点分布不可可靠反推，因此节点表从升级后的新事件开始累计。
- Problem identity 为 `product_line + service + fingerprint`，同指纹跨服务不再误合并。
- Problem count、first/latest representative sample、trend point、节点事实和 Cora decision 同事务写入。
- `node_occurrences` 保存累计节点分布，`node_trend_points` 保存逐窗口节点趋势；节点优先读取
  `labels.node/deployment_group`，兼容现有 `labels.server/group`，缺失节点归入 `unknown`。
- 历史事件乱序安全：`first_seen/last_seen` 及代表样本按完整事件时间维护，不依赖到达顺序。
- `/v1/problems`、`/v1/attention`、`/v1/trends`、`/v1/node-occurrences`、
  `/v1/node-trends`、`/healthz`；需要定位单个 Problem 的查询显式要求 `service`。
- 除 `/healthz` 外的所有 Server 路径都受 bearer token 保护，`/mcp` 复用同一鉴权；
  health 仍只应位于私网监听。
- Server 默认监听收紧为 `127.0.0.1:8080`；当前生产配置显式监听 `0.0.0.0:8088`，由网络边界
  和 public-web Nginx 控制入口。生产以 `cora-server -config.file=./cora.yml` 启动，相对路径以
  Supervisor 的 `/home/gbjk/zhouwei/cora` 工作目录解析。原 CLI flags 只保留给本地调试和兼容。
- Core 无法判断或返回非法 decision 时 fail-open 为 `observe`，不会阻断事实落库。
- `gbjk-zhifu` Cora Pack 本地下一构建为 `cora-gbjk-v0.1.1`，共 131 条规则：27 attention、
  41 observe、63 ignore。新增规则只忽略业务方确认的“特殊人群补助未在投保信息中找到”正常判断
  及其 Redisson/Seata 包装；其他锁和事务异常仍保持 attention。
- 非贯柏产品线不能继承贯柏经验，默认 `observe`。
- Problem 支持 `new / acknowledged / resolved / recurring`；已解决项不再出现在当前关注列表，
  `acknowledged + handled=false` 继续留在 attention 直到处理完成；真正晚于处理时间的新事件会转为
  recurring，历史回放不会误重开。
- 同进程 `/mcp` 使用官方 Go SDK 的无状态 Streamable HTTP。线上当前提供
  `cora_list_attention`、`cora_get_problem`、`cora_record_outcome`、`cora_export_cases`；本地下一构建
  新增只读 `cora_iteration_snapshot` 和 `cora_retention_audit`。后者只做在线生命周期预检，
  不读取本地 closure artifact、不授权清理；清理前仍必须对一致性备份运行离线法证审计。所有工具显式要求产品线，
  详情/回写同时要求 service；导出以 case ID 高水位冻结快照并分页返回，供本地持久化和离线迭代。
- `cora_list_attention` 将代表样本共享 trace ID 的当前 Problem 归成只读 incident：保留排序最高的
  Problem 为代表，同时返回 incident key、关联 Problem、涉及服务和共享 trace；底层 Problem、计数、
  状态和 case 均不合并。limit 在归组后生效，SQL 只提取 trace ID，不加载完整样本。
- Problem 详情通过代表样本的共享 trace ID 返回 `related_problems`，供 Agent 识别同一故障链的
  包装异常；它不自动合并 Problem 或删除事实。MCP 读取面将 first/latest sample 的 breadcrumbs
  限制为首 2 条加末 6 条，每条 message 最多 768 bytes，并对历史样本中的 OSS/S3 签名 URL 凭据
  做只读脱敏；SQLite 和 case export 保留完整原始快照。
- 回写最小四字段加 actor，生成只增不改的产品线 case；case 保存当时 Problem 和 Core decision
  上下文快照。handled=true 转 resolved，否则转 acknowledged。
- `/healthz` 暴露 build identity、schema、聚合和 SQLite 写入事实；受 bearer 保护的 `/readyz`
  在 SQLite 不可达或最近写失败尚未恢复时返回 503。Server 可执行 quick_check 和一致性备份。

当前 decision 仍是 `attention / observe / ignore`。讨论中倾向未来把 `ignore` 改成语义更
清楚的 `suppress`，但本轮 identity/node 变更没有改变 decision schema、Pack 或历史报告语义。

当前 Core 接口可替换，但实现仍把 JSON Pack embed 进 Server 并只在进程启动时加载；规则
变化仍需重新构建二进制。原始目标不是泛化的在线自学习，而是规则快路径 + LLM 灰区判断 +
产品线 case 检索 + Agent 结果回写 + 人工审核的规则结晶；小模型只在 case/eval gate 达标后
进入。当前实现了第一层规则和 case 持久化/按 Problem 回读；case top-k 参与判定、LLM adapter、
结晶和热加载仍未实现。

## 5. 已实现的 Agent 能力

- 一个进程并发跟随多个显式文件目标。
- 支持生产 Logback pattern，重建 ERROR 与多行 Java stacktrace。
- 提取 timestamp、trace ID、thread、logger、method、line、message、exception type 和 source。
- 每目标独立 16 KiB breadcrumb ring：有 trace 取前 30 秒最多 20 条；无 trace 按 thread
  取前 5 秒最多 5 条；不等待 ERROR 后的普通日志，轮转 reopen 时保留 ring。
- 上传前统一脱敏 ERROR message、stacktrace、breadcrumbs 和 labels：覆盖 Authorization、
  token/password/cardNo 类 key、OSS/S3 签名 URL 查询凭据、手机号和 18 位身份证模式。
- thread/method/line/breadcrumbs 不参与 fingerprint，只随 first/latest representative sample 保存。
- 原子 `0600` positions；共享 store 并发安全。
- 只有 Server 2xx 后才提交 offset。
- 连接失败、429、5xx 有界指数退避；耗尽后退出交给 Supervisor 重启。
- YAML `clients[].bearer_token_file` 和 CLI `-auth-token-file` 均可读取 token，secret 不进入
  Supervisor command line；请求通过 `Authorization: Bearer` 发送。
- 低频运行日志覆盖进程启停、目标文件打开/轮转、批次投递/重试/失败、Server 接收和非空 flush；
  只记录计数、大小、状态与耗时，不记录事件正文、stacktrace、breadcrumbs、labels 或 token。
- rename/reopen 与 copy-truncate 轮转检测。
- 默认新文件从末尾开始；历史回放必须显式 `from-start`/`beginning`。
- 单事件默认最多 256 KiB；JSON batch 默认最多 1.5 MiB、100 events。
- YAML 模式提供 `/healthz` 和 `/readyz`。
- 两个端点包含逐目标 worker/readable、file size、committed offset、lag、parsed/parse failure、
  ERROR、truncated、sent、retry、delivery failure/drop 以及最近读/发/失败时间；当前投递失败时
  readiness 返回 503，最终失败仍退出交给 Supervisor。
- Agent/Server/Canary 二进制均可输出 version/commit/build time/Go version。
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
中英文敏感模式、签名 URL 查询凭据及 fingerprint 不受新增上下文字段影响。面向 Agent 的 MCP
详情进一步限制 breadcrumbs 数量和单条消息大小，并为既有历史样本提供只读脱敏，但不改变存储
或稳定导出内容。

### Supervisor 部署安全闭环

真实 HTTP 回归覆盖缺失/错误 token 返回 401、正确 token 正常访问、`/healthz` 无鉴权；Agent
重试闭环验证每次请求都携带 bearer token。token file 拒绝空值和空白字符。Linux amd64
Agent/Server 可构建为静态 ELF。Server 主机只需交付 `cora-server + cora.yml`，当前 Supervisor
固定 `/home/gbjk/zhouwei/cora` 工作目录；Agent 主机使用 `/home/gbjk/cora` 下的
`cora-agent + agent.yml`。私网监听、目录权限、
SQLite/positions 备份与二进制回滚已形成交付文档。真实 Server 与两个 Agent 已完成基础接入、
健康检查、MCP 拉取/回写和 case 导出；真实备份恢复、日志轮转、磁盘不足等故障演练及连续
72 小时稳定性验收仍未完成。

### 原始愿景对照

当前已兑现轻量采集、洪峰收敛、代表样本、产品线隔离、节点事实和上下文；Agent First 的
MCP、处理结果回写、不可变 case 和最小 Problem 状态机已实现。官方 MCP Go client 已真实跑通
“拉取 -> 详情 -> 四字段回写 -> resolved 隐藏 -> 后续事件 recurring”，并验证 bearer 保护和
历史事件不误重开。生产环境已产生首条 acknowledged case，并稳定导出非空 snapshot；该 case
揭示同一次业务失败会被 Redisson/事务/全局异常处理层记录成多个 Problem，因此详情已增加共享
trace 关联。LLM 灰区判断、case top-k 进入 Core、EWMA 突变和影响面扩大事件仍缺失。
完整产品 canary 还必须在真实服务上重复该闭环。详见 `docs/VISION_ALIGNMENT.md`。

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

生产边界已确认：不追求高可用或 100% 问题覆盖；允许短暂不可用和少量计数缺失，但不允许
采集/写入长期静默失效。价值验收是发现并闭环至少一个原本可能被忽略的真实问题。

按顺序执行：

1. **可恢复演练**：在目标主机真实验证 SQLite/positions 备份恢复、Server 不可达、Agent 重启、日志轮转
   和磁盘不足；接受少量计数缺失，不接受无信号永久停止。
2. **完成真实 canary**：现有 1 Server + 2 Agent 已完成基础连通和首条 outcome；继续连续运行
   至少 72 小时，补齐恢复/轮转/磁盘故障演练，并以发现和闭环至少一个真实问题作为价值验收。
3. **生产反馈同步**：本地 Hermes 通过远程 MCP 回写 outcome；本地迭代端通过
   `cora_export_cases` 冻结并分页拉取产品线 case snapshot，落成训练/评估输入。生产 Server
   不主动连接本地，也不持有 Git 或训练权限。
4. **Core v0 完整管道**：规则快路径之外，为灰区增加 LLM + case top-k 检索；处理结果即时
   改善检索，重复一致 case 只生成待人工审核的规则候选。

生产接入按门槛扩展：`gb-order/service01/service02` 已用于验证同服务跨节点聚合。下一步先部署并
验证 `cora_iteration_snapshot`，生成一份真实业务日迭代报告并人工审核高频 ignore；完成该闭环前
不继续沿调用链扩大服务范围。
attention 噪音、敏感上下文或 Agent readiness 未达标时，不继续扩大到全部服务。

高可用、零丢失、Postgres/MQ/Redis、多租户、Web UI 和精确 event dedup 均不是 v0 前置条件。

MCP/case、不静默失效、release identity 和本地备份恢复工具已经完成。当前本地代码相对
已部署版本新增业务日迭代快照和 T+1 规则迭代工作流；下一步是部署带版本的 Server，验证
六工具 MCP，再生成真实报告。部署需要单独授权。

## 9. Git 与交接状态

- standalone Git repo，branch `main`。
- `origin` 为 `git@github.com:xiaomao361/cora.git`；2026-07-14 首次接入时远端尚无 `main`。
- Cora Core、Agent、MCP/case、状态机、不静默失效信号、release identity、SQLite/positions
  备份恢复、稳定 case 导出、T+1 规则迭代、B0 retention audit 和 Supervisor production canary
  文档已进入当前 checkpoint；
  以 `git log -1`、`git status` 和 `git ls-remote origin refs/heads/main` 为准。
- 仓库当前能力与已部署生产二进制并不等价：线上仍是已记录的 dirty rc6 快照；当前提交后的
  下一构建才包含六工具 MCP、在线 retention 预检和 `cora-gbjk-v0.1.1`。部署需要单独授权。
- `docs/HANDOFF_2026-07-15_*.md` 是阶段完成时的历史快照，其中的 dirty status、部署版本和
  “下一窗口”描述只用于追溯，不作为当前 Git 真相；文档入口以 `docs/README.md` 为准。

新 Codex 项目启动提示词：

```text
我们继续开发 Cora。

先完整阅读：
1. /Users/zhouwei/Documents/ClaraCore/apps/cora/docs/README.md
2. /Users/zhouwei/Documents/ClaraCore/apps/cora/docs/HANDOFF.md
3. /Users/zhouwei/Documents/ClaraCore/apps/cora/README.md

真实 repo 是 /Users/zhouwei/Documents/ClaraCore/apps/cora。先确认 repo root、remote、status，
保护任何未提交改动。当前唯一接入路径是 Cora Agent，不要恢复 Java SDK/Logback Appender。

仓库代码已包含六工具 MCP、T+1 规则迭代和只读 retention 审计，但线上版本边界必须单独核验。
下一步先完成当前分支全量验证；如明确授权部署，再构建有版本标识的 release，备份线上状态，
按 `deploy/README.md` 替换 Server，并运行 `cora-canary`、六工具 MCP 验收和 72 小时观测。
保持 Supervisor 单进程边界，不先做 UI/Webhook/高可用，也不让在线结果自动改写生产规则。
```
