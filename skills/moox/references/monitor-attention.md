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

`doctor alerts` 基于 Monitor 概览，除当前告警与数据集外，还列出不健康的业务检查、组件和主机（`business_check`、`component`、`host`）。首页直接读取的策略实例与因子任务细节仍需另外查看，报告末尾的“注”会提醒。

## 流程

1. **取清单**（只读，经 `moox.toml` 里的运维网关；Agent 不读 `moox.toml`，由 CLI 自己读取）：

   ```bash
   ./bin/moox-cli doctor alerts --format text      # 给人看
   ./bin/moox-cli doctor alerts                    # JSON，给 Agent 逐项处理
   ```

   退出码：0 没有问题，1 只有警告（降级），2 有严重问题，3 无法判断。每一项带 `source`（alert、dataset、business_check、component、host）、
   `severity`（critical、warning、info）、原因、原始信息和一条常见原因提示。`info` 只提示（例如因子“积累中”），不算故障。
   只看某个空间用 `--space-id crypto`。
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
| 已停用的策略实例、已删除的视图或数据集还留着告警，或显示“数据生产端长时间未更新”（`producer stale`） | 指标与自动检查可能在下一次上报、评估或清理前仍保留；策略实例当前没有删除接口 | 已自动处理，等即可：新版 Storage（进程重启后生效）删除 View 后不再导出它的水位指标；Monitor 对指向已删除数据集的视图序列不显示（Storage 读取失败时按“还在”处理）；Monitor 清理定时器每 5 分钟清掉“持续不再检查”超过 1 小时且没有未恢复告警的自动检查，停用超过 7 天的也清掉，连同规则、状态和结果；规则存在但关闭了 K 线新鲜度监控的检查（页面显示“已关闭”）会被每轮评估补建，不清理，`monitor-db-check.sh` 的“失效但还没被清理”里出现它们是正常的。`monitor-db-check.sh` 的“失效但还没被清理”一节持续有内容超过 1 小时，再查 Monitor 日志里有没有 `monitor_stale_checks_pruned` / `monitor_orphan_rules_pruned`。**不要手改 `monitor.db`、不要手删检查或规则** |
| 1 小时等低频视图报 `producer stale`，且 Storage 刚重启过 | 视图只在每个周期提交输出时刷新水位指标，重启后要等下一次提交才重新出现指标 | Monitor 对周期不短于 5 分钟的视图已容忍“一个周期加 5 分钟”，一般不会再报；仍报且超过这个时间，才是真的没在提交，按“数据落后”查。**不要为了清除它重启 Storage**——重启会让所有低频视图再空窗一个周期 |
| 重启或发布期间，采集、采集计划、“采集任务清单控制面数据过期”、某组件探针失败 | 依赖的服务重启或发布中，暂时不可用；Monitor 评估时读不到 | 几分钟内自愈，等 2～3 个评估周期（约 1～3 分钟）再看；不要因此去重启别的组件 |
| 主机“失联”或“host-agent 停止上报”（`presence`） | 主机采集代理超过 3 分钟没上报；网关心跳也停止时判整机失联，心跳正常时判 HostAgent 停报。Monitor 的观测消费者断连或重连宽限内会冻结该判定 | 看当前 `alerts`；只在 `incidents` 里出现的是历史。持续存在时按原因检查主机、host-agent、网关心跳和 EventBus 连通 |
| “策略参数投递”（`factor_params` 检查）告警 | 策略导出的 `moox_strategy_param_delivery_stale` 为 1：实例在运行、任务有可变成员，但连续多期（默认 10）结果都没有把下一期参数送达因子侧 | 看对应策略实例的结果里 `c_params_error` 与 FactorMgr 是否可达、参数提交是否被拒绝；规则见 [`docs/模块/因子.md`](../../../docs/模块/因子.md) 第 11 节 |
| 新建的因子任务一直停在“就绪/试运行” | 任务启用后引擎自动 init、start、confirm；本地序列不足最长回看（可变成员按参数上限）时停在试运行 | 不是故障：看 `lifecycle status` 给出的原因，需要时导入历史（见初始数据导入） |
| 交易关注项（检查 ID 形如 `trade_attention:*`）：目标被拒绝（`target_rejected`）、组合账户自动暂停（`auto_paused`）、目标持续阻塞（`target_blocked`）、执行账户持续未就绪（`account_not_ready`）、目标连续过期（`target_expired`）、同一目标内反向下单（`target_reversal`）、接口限流（`rate_limited`）、成交额异常偏高（`turnover_high`）、下单结果未知偏多（`unknown_results`）、合约保证金率过低（`margin_ratio_low`）、密钥检测发现阻断项（`inspection_blocked`）、密钥检测连续失败（`inspection_failing`）、合约钱包可划转资金不足（`transfer_shortfall`）、钱包划转或归集出问题（`transfer_failing`）、估值降级（有持仓取不到价）（`valuation_degraded`）、清仓结束时有子单没收拾掉，需人工核对（`flatten_orders_unsettled`）、释放会话时没能撤单，只按本地记录释放（`release_orders_unverified`）、释放会话持续失败，目标单没撤下（`release_orders_stuck`）、标的已下架的订单需人工核对成交（`orders_instrument_gone`） | Trade 每 30 秒按库里的状态和近期记录重新计算并上报 `moox_trade_attention_active`。`margin_ratio_low` 表示实盘账户保证金率跌破阈值，风控已暂停组合并按比例减仓（同时会有 `auto_paused`），持续到用户恢复为止；OKX 账户、账户类型还没检测写入的账户不监控，只在日志里提示一次。`valuation_degraded` 表示受管资产取不到价（标的已下架、状态不可交易、交易所确认标的不存在，或暂时取价失败但有最后已知价），权益按最后已知价计、没有就按 0 计，不阻断权益采样和组合交易；detail 只带账户和资产。`transfer_failing` 的 detail 是固定文字，划转编号和金额只在 Trade 日志正文里。`flatten_orders_unsettled` 表示整组清仓、减仓或下架清仓结束时有子单没收拾掉（收尾满 30 分钟仍解析不出结果或一直读写不了子单，或交易所确认标的已不存在），动作已照常结束。`release_orders_unverified` 表示释放策略会话时凭证失效、标的已不存在，或交易账户已停用、处于异常状态而取不到会话，目标单没能撤下，只按本地记录释放了会话。`release_orders_stuck` 表示释放策略会话连续失败超过 10 分钟（多半是交易账户启用但会话一直建不起来），目标单没撤下、会话还没释放，策略仍在重试；失败期间每次重试都刷新，释放成功即消失。`orders_instrument_gone` 表示账户同步发现订单的标的已下架、交易所查不到：未结束的单已按过期结束并释放资金预留；累计成交领先已入账成交明细的单（含已结束的单）成交明细已经拉不到，缺口按已入账值收掉；持仓、余额以交易所同步为准。`flatten_orders_unsettled`、`release_orders_unverified`、`orders_instrument_gone` 三项按事件上报，约 5 分钟后自动消失，告警消失不代表已处理 | `moox-cli trade logical show --logical-account-id <ID>` 看暂停原因，`trade actions --logical-account-id <ID> --action-type REDUCE` 看减仓结果；Agent 不恢复组合：把读数和减仓结果告诉用户，由用户确认安全后在管理台手动恢复。`valuation_degraded`：用 `trade holdings` 看这个账户的资产，`trade holders` 看是否是下架标的的零头，把情况告诉用户，由用户决定是否在交易所处理这些资产；Agent 不代为卖出。`transfer_failing` 处理见 [`api-recipes.md`](api-recipes.md) 的“人工结束钱包划转记录”。`flatten_orders_unsettled`：detail 里有组合和动作号，用 `trade action --action-id <动作号>` 看结果里的 `manual_check_child_order_ids`，Trade 日志（`setup logs --host <Trade 所在主机> --component trade --lines 2000` 搜“交易关注项”）里也有单号；`release_orders_unverified`：在 Trade 日志里搜“交易关注项”看原因，用 `trade order list --logical-account-id <ID> --owner-type TARGET --only-open` 找这个组合还没结束的目标单。`orders_instrument_gone`：标的下架后交易所已查不到这些单，Trade 把未结束的按过期结束（原因写明“需人工核对”）、把成交缺口按已入账值收掉（详情写着“成交明细缺失”），持仓、余额以交易所同步为准；在 Trade 日志里搜“交易关注项”拿到单号，告诉用户到交易所核对这些单的实际成交。`release_orders_stuck`：在 Trade 日志里搜“交易关注项”看失败原因，用 `trade account show` 看执行账户的状态和就绪原因，把情况告诉用户（多半要先让执行账户的会话建起来）；Agent 不代为释放会话或撤单。`flatten_orders_unsettled`、`release_orders_unverified`、`orders_instrument_gone` 三项都把单号告诉用户，请用户到交易所核对这些单是否还挂着、成交了多少，Agent 不代为撤单或下单。其余各项按 detail 里的组合和原因查对应的目标、账户与订单（`trade logical target`、`trade account show`、`trade order list`） |
| 事件中心关注项（检查 ID 形如 `event_center_attention:*`）：事件通知失败（`execution_failed`）、发现标的即将下架（`delist_detected`）、系统动作处理失败（`action_failed`） | 事件中心每 30 秒按库里的状态重新计算并上报 `moox_callbackdispatcher_attention_active`。`execution_failed` 是通知地址的执行记录用完重试变成失败（4xx、重定向、出站防护拒绝直接失败）；`delist_detected` 只持续约 3 分钟，相当于一条通知（发现下架且有策略持有）；`action_failed` 有五种情况：系统动作记录失败（下架后核对时仍有持有），还没结束的记录里有下架步骤失败或只平了一部分（detail 写着“下架处理有步骤失败或只完成一部分”，记录结束后恢复），还没结束的记录调用 Trade 连续暂时失败超过 15 分钟（detail 写着“连续暂时失败超过 15 分钟”，多半是 Trade 或网关不可达，原因在执行记录的失败说明里，下一次推进没有出错就恢复），记录已取消但取消后的善后（解除禁止买入）没做成，下架后核对时留有零头（只报一次，约 3 分钟，零头不会自动处理） | `moox-cli event executions list --status EXECUTION_STATUS_FAILED` 看失败记录，`event executions get` 看每次尝试的状态码和原因；修好对方接口后 `event executions retry`，确认已处理用 `event executions ack`（告警随之消失）。下架相关的 `event executions cancel` 会同时解除禁止买入，先和用户确认。`action_failed` 的 detail 写着“下架处理有步骤失败或只完成一部分”时，用 `event executions get` 看各轮各组合的步骤（“失败”是用完重试或已到下架时间，“只平了一部分”是 Trade 报告交易所拒绝或长时间没成交，原因写在结果里），下一轮复查会换新的操作再平，核对仍有持有时记录才会失败，到时再人工处理。detail 写着“取消后的善后未完成”时，不是失败记录：用 `event executions list --status EXECUTION_STATUS_CANCELED` 找带 `cleanup_pending` 的记录，再 `event executions cancel --yes` 取消一次重做善后，成功后告警消失 |
| Collector 关注项（检查 ID 形如 `collector_attention:*`）：`delist_source_failed`、`delist_readonly_key_missing`、`event_outbox_stuck`、`eventbus_consumer_failed` | Collector 的下架检测每 10 分钟轮询币安现货下架计划、币安 U 本位合约信息、OKX 永续合约信息，上报 `moox_collector_attention_active`。某个来源连续失败 3 次是 `delist_source_failed`（多半是出口代理、域名白名单或交易所接口变动）；同一个 `delist_source_failed` 也在来源返回的条目被校验拒绝时出现（detail 写着“有下架条目被校验拒绝、没有发布（可能漏报）”：交易所、市场不对，或代码带冒号等，这些条目没有发布，等于漏报；代码大小写不同不算，会自动统一成大写）；读不到币安只读 Key 是 `delist_readonly_key_missing`（只算 warning，业务检查显示“需关注”，不挡 `setup verify`），币安现货来源本轮跳过，下一轮继续尝试；没配海外机时 Collector 的 `delist.auto_detect` 为 false，不轮询这三个来源，也就没有这两项 | 先看 Collector 日志里的“下架检测来源”；缺只读 Key 分三种情况，告警分不出是哪一种，逐一和用户确认：已有但被禁用的，请用户到管理台秘钥管理页启用；还没有的，请用户 `moox-cli config edit` 在 `[[exchange_keys]]` 里加一把币安正式环境、`readonly = true`、`name = "readonly"` 的 Key，再 `setup apply` 写入；在管理台删除过的，`setup apply` 不会重建、换名称也不行，目前补不回来（采集文档的已知限制）。补上后下一轮自动恢复；缺 Key 期间现货下架只能用 `moox-cli collector delist register` 或管理台「事件中心 › 下架处理」的“手工登记下架”手工登记（登记后到 T−24h 会自动卖出策略持有的实盘仓位；`--yes` 前向用户确认标的和下架时间；演练用 `--paper-only`）。detail 写着“被校验拒绝”时：在 Collector 日志里搜“登记下架通知”的 ERROR，里面有被拒条目的交易所、市场、代码和原因；漏掉的下架用 `collector delist register` 手工登记补上（同样先向用户确认标的和下架时间），再检查来源（接口返回是否变了），来源恢复正常后下一轮告警自动消失。`event_outbox_stuck` 是发件箱最老的事件超过 10 分钟没发布出去（EventBus 连不上或发布被拒），或有无法解码的事件被搁置成死信（detail 里写明条数）：先查 EventBus 是否可达、Collector 日志，死信需要人工看是哪条事件；`eventbus_consumer_failed` 是连上 EventBus 后 K 线合成的消费者连续 3 次创建失败，不影响下架事件发布，看日志里的失败原因（多为流或权限没配好） |
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
