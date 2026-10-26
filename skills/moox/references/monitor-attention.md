# 首页提示“N 项需要处理”的排查（Agent 用）

用户说首页提示“有问题需要处理”、监控告警、数据“长时间未更新”、某个组件或主机异常时，按这里的流程查：先取清单，再逐项判断是真问题、残留还是瞬时，
最后处理并复核。全程只读，处理动作按下面各条的说明做，破坏性运维（重置 View、清数据）另行向用户确认，见 [`cli-operations.md`](cli-operations.md)。

## 首页“需要处理”由什么组成

首页把四类问题合在一起计数：

| 来源 | 内容 | 怎么看 |
| --- | --- | --- |
| 监控告警 | Monitor 当前未恢复的告警（`alerts`） | `moox-cli doctor alerts` |
| 数据 | 告警之外仍不健康的数据集（落后、停止上报） | `moox-cli doctor alerts` |
| 策略 | 策略实例降级、目标过期等 | 控制台策略页，或 moox-cli 的策略命令 |
| 因子 | 因子任务计算降级、落后 | 控制台因子页，或 `moox-cli doctor diagnose` |

`doctor alerts` 只覆盖前两类；策略和因子要另外看，报告末尾的“注”会提醒。

## 流程

1. **取清单**（只读，经 `moox.toml` 里的运维网关；Agent 不读 `moox.toml`，由 CLI 自己读取）：

   ```bash
   ./bin/moox-cli doctor alerts --format text      # 给人看
   ./bin/moox-cli doctor alerts                    # JSON，给 Agent 逐项处理
   ```

   退出码：0 没有问题，1 只有警告（降级），2 有严重问题，3 无法判断。每一项带 `source`（alert、dataset、business_check、component、host）、
   `severity`（critical、warning、info）、原因、原始信息和一条常见原因提示。`info` 只提示（例如因子“积累中”），不算故障。
   只看某个空间用 `--space crypto`。
   同一类 doctor 还有 `doctor bootstrap`（新部署各模块的安装与配置是否正常）和 `doctor diagnose`（按 Monitor 的事实诊断各模块），
   清单里查不出原因时再用它们。

2. **判断每一项是真问题、残留还是瞬时**，对照下面的“常见原因与处理”。先看起始时间：几分钟内的多半是重启或发布造成的瞬时现象。

3. **需要更深一层时**，用只读的数据库检查（经 SSH 公钥，主机配置同 `native-release.sh`，见 [`native-release.md`](native-release.md) 的“一次性配置”）：

   ```bash
   skills/moox/scripts/monitor-db-check.sh            # 默认控制机
   ```

   它列出：未恢复的告警状态和各自最近一次检查结果、检查概况、失效但还没被清理的自动检查、没有检查的孤儿默认规则、
   静默超过 2 分钟的视图序列、清理定时器的日志。契约测试是 `skills/moox/scripts/test-monitor-db-check.sh`。

4. **处理后复核**：重新跑 `doctor alerts`，预期退出码 0。残留项要说明等多久会自愈（见下），不要用重启组件去“催”。

## 常见原因与处理

先看 `alerts`（当前告警）；`incidents`（最近 48 小时的告警经过）里没有恢复记录的条目不一定还在告警，要看它的 `active` 字段，
不要把历史当成现状。

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 已删除的策略实例、视图、数据集还留着告警，或显示“数据生产端长时间未更新”（`producer stale`） | Prometheus 的指标序列删对象后不会自己消失；Monitor 里对应的自动检查、告警规则也一直留着 | 已自动处理，等即可：新版 Storage（进程重启后生效）删除 View 后不再导出它的水位指标；Monitor 对指向已删除数据集的视图序列不显示（Storage 读取失败时按“还在”处理）；Monitor 清理定时器每 5 分钟清掉“持续不再检查”超过 1 小时且没有未恢复告警的自动检查，停用超过 7 天的也清掉，连同规则、状态和结果；规则存在但关闭了 K 线新鲜度监控的检查（页面显示“已关闭”）会被每轮评估补建，不清理，`monitor-db-check.sh` 的“失效但还没被清理”里出现它们是正常的。`monitor-db-check.sh` 的“失效但还没被清理”一节持续有内容超过 1 小时，再查 Monitor 日志里有没有 `monitor_stale_checks_pruned` / `monitor_orphan_rules_pruned`。**不要手改 `monitor.db`、不要手删检查或规则** |
| 1 小时等低频视图报 `producer stale`，且 Storage 刚重启过 | 视图只在每个周期提交输出时刷新水位指标，重启后要等下一次提交才重新出现指标 | Monitor 对周期不短于 5 分钟的视图已容忍“一个周期加 5 分钟”，一般不会再报；仍报且超过这个时间，才是真的没在提交，按“数据落后”查。**不要为了清除它重启 Storage**——重启会让所有低频视图再空窗一个周期 |
| 重启或发布期间，采集、采集计划、“采集任务清单控制面数据过期”、某组件探针失败 | 依赖的服务重启或发布中，暂时不可用；Monitor 评估时读不到 | 几分钟内自愈，等 2～3 个评估周期（约 1～3 分钟）再看；不要因此去重启别的组件 |
| 主机“失联”（`presence`） | 主机采集代理 3 分钟没上报：主机离线、网络断、host-agent 停了；或刚重启 Monitor、EventBus | 看当前 `alerts`；只在 `incidents` 里出现的是历史。持续存在才登录主机看 host-agent 进程和 EventBus 连通 |
| “策略参数投递”（`factor_params` 检查）告警 | 策略导出的 `moox_strategy_param_delivery_stale` 为 1：实例在运行、任务有可变成员，但连续多期（默认 10）结果都没有把下一期参数送达因子侧 | 看对应策略实例的结果里 `c_params_error` 与 FactorMgr 是否可达、参数提交是否被拒绝；规则见 [`docs/模块/因子.md`](../../../docs/模块/因子.md) 第 11 节 |
| 新建的因子任务一直停在“就绪/试运行” | 任务启用后引擎自动 init、start、confirm；本地序列不足最长回看（可变成员按参数上限）时停在试运行 | 不是故障：看 `lifecycle status` 给出的原因，需要时导入历史（见初始数据导入） |
| 数据落后（`late_confirmed`、`success stale`、`run stale`） | 生产链路真的慢了：Collector、Storage Primary、View、Factor 之一 | 按 [`view-catchup.md`](view-catchup.md) 分别量 Primary、View、Factor 三段，再决定补算或修复 |
| 组件“从未收到监控上报”“监控上报已中断” | 进程没起、连不上 EventBus，或凭据不对 | `native-release.sh status --host <别名>` 看进程与就绪；EventBus 凭据问题见 [`eventbus-credentials.md`](eventbus-credentials.md) |

## 判定口径（给判断“是不是误报”用）

- Monitor 对一个指标序列 2 分钟没收到就算“停止上报”；周期不短于 5 分钟的视图输出水位序列例外，容忍“一个周期加 5 分钟”（1 小时视图 65 分钟）。
- 数据新鲜度看“应收的最新一根 K 线晚于允许迟到仍没到”，不看“最近有没有上报”；“积累中”（`warming`）显示降级但不是故障，不告警。
- 概览里的主机地址等环境信息不要写进回复或文档；`doctor alerts` 的输出本身不含主机地址。

## 不要做

- 不要手改 `monitor.db`，不要手删检查或告警规则（清理定时器负责）。
- 不要把重启当修复手段：重启 Monitor 不会清告警，重启 Storage 会让低频视图空窗。要重启先说明影响并取得用户同意，按 [`native-release.md`](native-release.md) 的流程。
- 不要打印凭据、令牌或主机地址；不要读取 `moox.toml`。
- 清单里的 critical 先于 warning 处理；拿不准是真故障还是瞬时的，先等一两个评估周期再复查，不要连续动手。
