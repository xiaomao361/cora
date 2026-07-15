# Cora production readiness contract

## 产品承诺

Cora 不是零丢失的事件总线，也不是承诺发现全部故障的监控平台。它的生产承诺只有一个：

> 在当前错误中，尽可能找出可能值得关注的问题，让 Agent 或工程师有机会处理其中至少一个，
> 从而让系统比之前更健壮一点。

这是 best-effort 的关注发现产品。衡量价值的核心不是错误采集完整率或告警覆盖率，而是：

- 是否发现了原本可能被忽略的真实问题；
- 是否减少了错误洪峰和已知噪音带来的干扰；
- 处理结果是否让下一次判断更贴近该产品线；
- 是否在不增加明显运维负担的前提下持续产生上述价值。

## 可接受与不可接受

可接受：

- 单 Cora Server + SQLite，不做高可用集群；
- Server 短暂不可用，由 Supervisor 和人工恢复；
- 进程崩溃时丢失一个内存聚合窗口内的部分计数；
- at-least-once 导致少量重复计数；
- 某些问题漏报、误报或判断不准；
- 第一版 Core 主要依赖规则和 Agent 判断，逐步积累 case。

不可接受：

- Agent 已停止采集、严重积压或持续发送失败，但系统没有可见信号；
- Server 对公网开放或敏感日志未经脱敏进入代表样本；
- 产品线经验、case 或查询结果默认跨线混用；
- 已处理问题持续反复打扰 Agent，无法 ack/resolved/recurring；
- MCP 只能读取却不能回写处理结果，导致经验闭环中断；
- SQLite 无可验证备份，升级失败后无法恢复；
- 为追求“生产级”引入 MQ、Redis、集群、复杂权限或完整 APM，偏离轻量目标。

## v0 生产边界

第一版只服务单公司内网环境：

- 六台以内应用服务器，每台一个 Cora Agent；
- 一个私网 Cora Server；
- Supervisor 管理进程；
- SQLite WAL 作为事实源；
- bearer token + 网络白名单；
- 允许人工恢复，不承诺自动故障转移。

初始内部恢复目标建议为 30 分钟内恢复 Server；这是操作目标，不是外部 SLA，可在真实 canary
后调整。活动日志必须保留到足以覆盖该恢复窗口；若做不到，Agent 必须明确暴露丢失风险。

## 上生产前的 P0

### 1. Agent First 产品闭环

当前本地实现已完成，并由官方 MCP Go client 端到端回归；仍需在真实 canary 中验收。

- Server 同进程提供受 bearer 保护的 Streamable HTTP MCP；
- `cora_list_attention` 返回“现在可能值得关注”的问题，而不是全部历史 Problem；其中
  `acknowledged + handled=false` 继续可见，直到后续 outcome 将其处理完成；共享代表 trace 的
  Problem 只在读取面归为一个 incident，原始事实保持独立；
- `cora_get_problem` 返回代表样本、趋势、节点和已有 case；
- `cora_record_outcome` 回写真问题、是否处理、根因、动作，并生成不可变产品线 case；
- Problem 至少支持 `new / acknowledged / resolved / recurring`，避免重复打扰。

### 2. 不静默失效

当前本地实现已完成：Agent/Server 均暴露运行事实并有失败/恢复回归；真实阈值仍需 canary 校准。

- Agent 每目标暴露 readable、lag、sent、retry、failure、parse/drop；
- Server 暴露 dropped、flush failure、SQLite 写失败、最后成功写入时间；
- health/readiness 能区分“进程活着”和“仍在有效工作”；
- 二进制暴露版本、commit 和 schema version，现场状态可追溯。

### 3. 可恢复而非高可用

仓库已提供 SQLite 一致性备份、quick_check、positions 备份和恢复步骤；本地恢复演练纳入发布验证，
生产主机上的停启、权限和磁盘故障仍必须在 canary 前实际执行一次。

- 实际执行一次 SQLite 停机备份和恢复；
- 实际执行一次 positions 备份、Agent 重启和续传；
- 演练 Server 不可达、Agent 重启、日志 rename/copy-truncate、磁盘空间不足；
- 明确活动/未压缩日志的最短保留时间；
- 任一失败可以丢少量计数，但不能无信号地永久停止发现新问题。

### 4. 真实 canary

基础 canary 已执行：真实 Server、两个 Agent、health/readiness、MCP 拉取/回写和首条不可变
case 已验证。完整 canary 尚未完成；当前下一道门是补齐故障演练与连续观测，而不是继续扩展
产品功能。

- 保持当前 1 Server + 2 Agent 的受控范围，不继续扩大服务面；
- 默认从文件末尾开始，不回放历史洪峰；
- 连续运行至少 72 小时；
- 重复验证 MCP 拉取、调查、结果回写和 case 再读取；
- 完成 SQLite/positions 恢复、Server 不可达、Agent 重启、日志轮转和磁盘不足演练；
- 至少验证一次“发现并处理一个真实问题”，这是价值验收，不要求覆盖全部问题。

## 明确后置

以下不是 v0 上生产的前置条件：

- Server 高可用或自动故障转移；
- 事件绝对不丢、精确计费级 count、全局 event dedup；
- Postgres、MQ、Redis、分布式存储；
- 多租户、RBAC、审计平台；
- Web UI、通知渠道和完整告警生态；
- 小模型训练或在线自动修改规则。

event ID/幂等、LLM 灰区判断、case top-k 检索和规则结晶仍然重要，但应根据真实 canary 的
重复噪音和判断质量逐步加入，不应阻塞第一版 best-effort 价值验证。
