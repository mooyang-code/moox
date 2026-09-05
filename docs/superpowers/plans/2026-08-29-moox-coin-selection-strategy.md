# MooX 策略执行框架实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将当前截面选币实现改为简洁的规则 DSL，支持定时与事件触发，只向 Trade 输出完整目标持仓权重。

**Architecture:** 固定 Go 流水线处理标的池、前筛、评分、选择、信号、权重、后筛与合成，Expr 只执行受约束表达式。定义仅保存 `dsl_yaml`，编译态留在内存；每个运行周期首次成功的 Result 保存规则后态，并与 outbox 原子提交。Strategy 与 Trade 共同使用 `run_id` 隔离运行身份，在该身份内以 `bar_end_time` 判断目标先后。

**Tech Stack:** Go 1.25、tRPC-Go、Protobuf、SQLite/GORM、NATS JetStream、Expr、现有 Storage/Factor 输入绑定、Vue 3/Pinia/Arco Design、Vitest/Playwright。

## 状态与范围

修订日期：2026-09-05。依据 [策略执行框架设计](../../策略执行框架设计.md)，本文替换原文件中的旧实施步骤，
保留原路径，避免出现两份并行施工基线。**以下任务均未执行；本轮只修改文档。**

原计划中的已完成勾选、测试、制品和生产部署记录仅适用于旧版实现，保留在 Git 历史中，
不能证明本计划完成。源码检查确认当前仍是 `moox.strategy/v2` Manifest，而不是下面的新 DSL。

实施边界：

- Strategy 只计算目标权重；Trade 负责权益、数量换算、账户选择、风控、订单与失败恢复。
- 保留 JetStream 异步链路；Trade 通知供监控展示，不反向推进 Strategy 的规则状态。
- 单进程、SQLite、每 Runner 串行求值，不增加通用 DAG、分布式锁、Saga 或版本平台。
- 不重做已有 Collector 标的规范化、Factor catalog 与因子公式；不增加 FactorInstance。
- 不全仓机械替换 Spec。确实表示定义的对象用 Def，Binding、Request、Params、Config 保持其含义。
- 本计划不授权生产部署、清库、修改账户或实际下单；这些操作需另行确认。
- 协议不提供旧 DSL 兼容分支；新旧权重协议不能同时消费。切换前停用相关 Runner、
  隔离旧 outbox/消息及旧消费进程，再协调升级；不在启动时自动删除用户数据库。
- 不把当前 Trade 首次换算回执当作 Strategy 的业务要求。本轮复用 Trade 的现有数量执行链，
  保留消息一致性与执行幂等保护，不扩展一套新的交易换算引擎。

## 一、固定合同

### 1.1 最小对象

| 对象 | 必需内容 | 删除或改名 |
| --- | --- | --- |
| StrategyDef | strategy_id、dsl_yaml、created_at、updated_at | 不持久化另一份 name、kind、compiled_json 或定义 hash |
| StrategyRunner | runner_id、strategy_id、space_id、input_bindings、可空 logical_account_id、enabled、run_id | 删除 command_sequence、last_result_id；目标从 Result 查询 |
| StrategyResult | result_id、runner_id、run_id、strategy_id、bar_end_time、dsl_yaml 副本、前结果 ID、输入出处、rule_states、解释与目标 | 不再以 input_hash 判断同周期修订，不再区分 hold/rebalance |
| RuleState | 按规则名保存信号持有项或批次 | 替代 TheoryState/DecisionState；无状态规则不保存 |
| PositionTargets | target_id、runner_id、run_id、strategy_id、logical_account_id、bar_end_time、effective_at、valid_until、targets | 不包含 command_sequence；固定 FULL 快照 |

`run_id` 是一次显式启用的身份，可用 UUID，不比较大小。普通进程重启保持原值；
停用、修改后重新启用使用新值，从空规则状态开始。对已启用 Runner 重复 Enable 不换 ID。
稳定停用时 run_id 为空；false+非空 run_id 是未完成控制操作，重启后只清理、不自动续启。

`last_result_id` 删除后，按 `(runner_id, run_id)` 查询周期最大的成功 Result。
保留求值开始时读取的前结果 ID，作为提交事务的校验参数和审计字段，不放回 Runner。
清理历史结果时必须保留当前运行最新成功 Result。

`bar_end_time` 替代策略语境的 as_of/period_time，是决策使用的已闭合 bar 的结束时刻，
不是触发时间、处理时间、创建时间或行情源的开盘标签。其他模块的原始 period_time 不全局改名，
只在输入适配边界明确转换。

### 1.2 DSL 示例与流程

```yaml
name: strategy_demo
triggers:
  schedule: {cron: "5 * * * *", timezone: UTC}
  event: {name: ViewFactorPeriodReady}
data: {bar: 1h, calendar: crypto_24x7}
rules:
  momentum_selection:
    pool: {udf: spot_symbols}
    filter_before: "turnover_20 > 1000000"
    score: "0.6 * pct_rank(return_20) + 0.4 * pct_rank(turnover_20)"
    select: {top: 10}
    weight: 0.60
    filter_after: "return_20 > 0"
  ma_signal:
    pool: [BTC-USDT-SPOT, ETH-USDT-SPOT]
    signals:
      entry: "bars[-1].ma20 <= bars[-1].close && bars[0].ma20 > bars[0].close"
      exit: "bars[-1].ma20 >= bars[-1].close && bars[0].ma20 < bars[0].close"
    weight_each: 0.10
```

示例 UDF、字段和标的必须由测试夹具或真实绑定提供，不假设它们已注册。

```text
定时 / 事件
  -> 确定 runner_id + run_id + bar_end_time
  -> 查询本期是否已成功、是否已有更晚结果，恢复当前运行最近 RuleState
  -> pool -> 按阶段加载必要输入 -> filter_before
  -> 标准化 / score -> select -> 可选 signals
  -> 基础份额与权重分配 -> filter_after
  -> 合成完整目标与下一期 rule_states
  -> 事务复核身份、前态和有效期，提交 Result + outbox
  -> 异步发送给 Trade
```

两种触发为 OR，不是交易调仓模式。每个 `(runner_id, run_id, bar_end_time)` 最多成功一次；
失败可在时效内重试，同期成功后的输入修订不重算。新成功周期即使权重不变也生成目标。
任何必要规则失败都不提交部分 FULL；成功空集才是完整零目标。

### 1.3 历史引用

- `bars[0]`：本次决策已闭合 bar 的行情与因子值。
- `bars[-1]`：同一交易日历的紧邻前一有效 bar，不是上次触发或最近可找到的记录。
- 第一版只允许常量索引 0/-1。拒绝正索引、-2、动态索引及未收盘数据。
- 这是相对周期视图，不采用普通 slice 的负下标含义。实现用整数键视图或受限 AST 转换。
- 跨期表达式两期均显式索引；普通单期评分、过滤仍支持裸字段简写。
- 缺少必需历史就跳过整次决策，不删除子条件、不补零、不把已有理论持仓解释成退出。
- ma20 等值来自 Factor 绑定；不在 Strategy 内重算指标窗口。

### 1.4 异步接收顺序

Trade 在任何去重快路径前校验当前授权 `(runner_id, run_id)` 和 `[effective_at, valid_until)`，
在接收事务内复核，再比较当前授权运行内的 `bar_end_time`：

| 输入 | 处理 |
| --- | --- |
| 更晚周期 | 接受；允许中间没有目标 |
| 同周期、同 target_id、全部规范内容相同 | 幂等，不重复唤醒执行 |
| 同周期但 ID 或内容不同 | 冲突，不替换 |
| 更早周期、旧 run_id 或过期 | 拒绝，不恢复旧目标 |
| 相同 target_id 但任意业务字段变化 | 冲突，即使已经存在 receipt |

新 run 单独建立周期基线，不继承旧发布者的高水位。Trade 重试、恢复和子订单提交前仍需检查
当前目标身份、授权与有效期；过期只停止新交易，不自动清仓，不隐含撤销已挂订单。
请求内容摘要可以保留，它不是 DSL 定义 hash。Trade 订单 Version 乐观锁不删除。

## 二、当前源码与改动地图

以下均为仓库根目录相对路径；“新增”路径目前不存在，其余为已有文件或目录。
实施时先检查工作树，不覆盖用户修改。

| 责任 | 当前入口 | 需要改变的行为 |
| --- | --- | --- |
| DSL / 编译 | modules/strategy/internal/config/manifest.go；internal/compiler/compiler.go、types.go | v2 long/short 与持久化制品改为 rules、输入依赖、内存 Expr |
| 输入 | modules/strategy/internal/input/types.go、pool.go、readiness.go；internal/storageio/view.go、rpc.go | 保留快照与 provenance；增加行情列和精确前一周期，检查按真实依赖进行 |
| 求值 | modules/strategy/internal/selection/evaluator.go | 当前全池排名、后筛后均分，均与新设计相反，必须修改 |
| 周期提交 | modules/strategy/internal/store/results.go；internal/trigger/processor.go | 删除 input_hash latest-wins、hold、递增序号和 Runner 结果指针 |
| 控制面 | modules/strategy/internal/registry/service.go；internal/rpc/service.go；internal/bootstrap/logical_account.go | 可停用编辑、启用编译、共同 run_id 授权 |
| Trade | modules/trade/internal/eventconsumer/target.go；internal/infra/store/target.go、target_receipt.go | 现有 receipt/sequence 快路径早于 owner 校验，需改为授权、时效、周期合同 |
| 再次执行 | modules/trade/internal/application/target/executor.go；internal/application/order/service.go；internal/infra/store/fact.go | 现有仅检查 owner runner，增加 run 与目标期限保护 |
| 协议 / UI | packages/tradeeventpb/trade_events.proto；两个模块 proto；web/src/api/strategy-types.ts | 同步去掉旧字段，不能只改 StrategyRunner |

当前 `packages/marketcalendar/calendar.go` 只提供 `cn_stock` 交易日工具，没有 crypto_24x7
或完整分钟会话映射；`packages/timerjob` 是任务回调工具，不是现成的动态 cron 调度器。
不得在计划执行时假称这些能力已经存在。

## 三、任务清单

任务按合同、纯计算、存储/触发、Trade、管理台、集成的顺序推进。每项先写列出的失败用例，
运行确认失败原因，再实现并复跑。共享类型或 Proto 变更要同步最小调用方，保持检查点可编译；
紧密依赖的任务可放在同一未发布分支，不能将单边协议变更部署出去。

以下命令除特别注明外均在 MooX 仓库根目录执行，使用现有 go.work。
每项通过后只暂存该项文件并提交，提交信息按仓库 moo-git-commit 规范填写。

### Task 1：定义 DSL 与表达式合同

**修改：** `modules/strategy/internal/config/manifest.go`、`manifest_test.go`；
`modules/strategy/internal/compiler/compiler.go`、`types.go`、`verify_dependencies.go`、`compiler_test.go`；
`modules/strategy/go.mod`、`go.sum`。
**新增：** `modules/strategy/internal/compiler/expressions.go`、`expressions_test.go`。

- [ ] 在现有配置文件内以 `config.DSL` 替代旧 Manifest 类型，不为改名字额外拆目录。
  YAML 根只接受 name/triggers/data/rules；规则字段和互斥关系逐项按设计第 3、6、7 节校验。
- [ ] 增加 `TestDSLContract` 表驱动用例：示例可解析；schedule-only/event-only 合法；
  重复键、未知字段、空 rules、空 select、top+tail、select 无 score、weight+weight_each、
  signals 缺 entry/exit、holding+signals、旧 api_version/kind 均失败。
- [ ] 将下面的表达式表落为 `TestBarsExpressionContract`，验证编译期字段类型与依赖提取：

| 表达式 | 预期 |
| --- | --- |
| bars[-1].ma20 <= bars[-1].close && bars[0].ma20 > bars[0].close | bool；依赖两期 ma20/close |
| bars[0].close > 0 | bool；只依赖当前期 |
| bars[1].close > 0、bars[-2].close > 0、bars[offset].close > 0 | 编译拒绝 |
| prev.close > 0、bars[0].unknown > 0 | 编译拒绝 |
| now()、rand()、未注册函数 | 编译拒绝 |
| pct_rank(return_20) | 仅 score 阶段合法，参数为绑定字段名 |
| pct_rank(return_20 + 1)、signals.entry 引用 score | 编译拒绝 |

- [ ] 为 Strategy 引入 Expr，复用 DataMind 已验证的类型约束思路，但不移植解释器或 DAG。
  禁用默认内置函数，只开放明确的纯函数与 pct_rank/zscore；遍历 AST 拒绝未授权能力。
  为输入别名保留 bars、score 及函数名称，防止命名冲突。
- [ ] CompiledStrategy 只保存内存程序、字段类型和阶段/偏移依赖，删除可落盘 JSON/hash。
  保留 Factor 多输出显式选择、Space/View/Binding/频率与来源校验；不删除 Factor source_hash。
- [ ] 运行前述新测试，再运行两个包全量，预期 PASS，无静默接受旧字段：

```sh
go test ./modules/strategy/internal/config ./modules/strategy/internal/compiler
```

### Task 2：标的池、周期映射与按阶段输入

**修改：** `modules/strategy/internal/input/types.go`、`pool.go`、`pool_test.go`；
`modules/strategy/internal/storageio/view.go`、`rpc.go`、`view_test.go`。
**新增：** `modules/strategy/internal/input/udf.go`、`udf_test.go`、`calendar.go`、`calendar_test.go`。
**收拢删除：** `internal/input/readiness.go`、`readiness_test.go`、`hash.go`：
先将有用检查和测试迁入加载流程，再删除独立检查器和同周期输入 hash 路径。

- [ ] 固定 pool UDF 注册签名：输入为预加载标的目录快照、bar_end_time、已校验参数；
  输出规范 instrument_id 列表和 error。列表排序去重校验，重复或目录外 ID 报错，不静默截取。
  固定列表与 UDF 走同一输出校验；合法空列表与 UDF 错误明确区分。
- [ ] 实现薄周期适配层。首版支持 crypto_24x7 的整分钟/整小时/日 bar，以及 cn_stock 日 bar；
  股票分钟会话、多频率混合暂不开放，启用时明确报不支持，不按 24 小时伪造交易日。
  连续市场锚点为 UTC 1970-01-01；股票使用已加载交易日历的稳定有序交易日索引，范围外报错。
  锚点和日历数据改变须停用后重新启用。
- [ ] 新增 `TestBarCalendarContract`：小时 10:00–11:00 在原定 11:05 触发、延迟到 11:07
  处理仍映射 11:00；股票周一前一根是日历中的前一交易日；节假日、范围外、未闭合 bar 均覆盖。
  明确源 timestamp 表示开盘还是收盘，在适配层转换，不改变 Storage 原始时间含义。
- [ ] 当前行情字段和因子字段一起装入相对周期视图；只引用 [0] 不读历史，引用 [-1]
  读取精确上一周期。保留事件指定的索引身份/修订，当前与历史读取不能混入不同冻结索引。
- [ ] 按规则阶段读字段：前筛对 pool、评分对初筛后集合、退出/后筛对旧持有项；
  池外旧信号持仓仍加载，非新批次不加载无关评分字段。
- [ ] 扩展 `view_test.go`：上一期缺失但更早一期存在仍失败；当前有 close 而因子缺失不能补值；
  前筛剔除项缺未使用评分列不阻塞；旧持仓缺退出列阻塞整次；分页索引切换、跨 venue、
  Factor source_hash 错配维持原有拒绝语义。临时未就绪与永久缺失错误分类保留。
- [ ] 运行并预期 PASS：

```sh
go test ./modules/strategy/internal/input ./modules/strategy/internal/storageio
go test ./packages/marketcalendar/...
```

### Task 3：标准化、选择、权重与后筛

**修改：** `modules/strategy/internal/selection/evaluator.go`、`evaluator_test.go`；
`modules/strategy/internal/quant/decimal.go`、`decimal_test.go`。
**新增：** `modules/strategy/internal/selection/normalize.go`、`normalize_test.go`。

- [ ] 将 `TestEvaluateRanksFullPoolBeforePreFilter` 替换为初筛集合评分测试。
  pct_rank 采用升序平均名次 (r-1)/(N-1)，单元素或全相同为 0.5；
  zscore 使用总体标准差，零方差为 0。只在 score 阶段批量预计算，不覆盖原始列。
- [ ] 使用下面固定输入验证 `TestNormalizeAndSelectContract`，再覆盖同分、零方差、NaN/Inf：

| ID | return_20 | turnover_20 | pct_rank(return_20) | pct_rank(turnover_20) | 0.6/0.4 合成分 |
| --- | --- | --- | --- | --- | --- |
| A | 0.05 | 10000000 | 0 | 1 | 0.4 |
| B | 0.10 | 2000000 | 0.5 | 0 | 0.3 |
| C | 0.15 | 5000000 | 1 | 0.5 | 0.8 |

- [ ] 排序固定 score 降序、ID 升序；top:2 得 C/A，tail:1 得 B。
  select.where 在标准化之后、top/tail 截取之前筛分数；不足取现有，同分不扩容；
  省略 select 全选，省略 score 则不提供排名和分数筛选。
- [ ] 新增 `TestPostFilterKeepsVacancy`：10 个拟持有项、weight=0.60，每个先分配 0.06，
  后筛去掉两项只剩 0.48，不补位、不把八项重新分到 0.60。
- [ ] 复用 quant 的 18 位定点：先按 ID 升序 DivideStable(1, IDs) 得 base_weights，
  再乘规则预算，最后应用方向；三个 offset 按数值升序分预算；乘法尾差留空。
  M=7、删除分得余数的首项、规则 map 顺序打乱均有精确字符串断言。
  空集合直接输出空，不调用除零；其他除零必须报错，不能沿用当前 Div 返回零而静默成功。
- [ ] 编译期证明总预留比例不超过 1，运行期在后筛/净额前再次检查。
  weight_each 需要可证明的数量上界；signals+weight_each 只允许固定池并按全池预留。
  现货 short 在规则层拒绝，不能靠其他 long 抵消。多规则同标的合并，零净额省略，输出按 ID 排序。
- [ ] 运行并预期 PASS：

```sh
go test ./modules/strategy/internal/selection ./modules/strategy/internal/quant
```

### Task 4：最小 RuleState 与时序规则

**修改：** `modules/strategy/internal/domain/types.go`、`types_test.go`；
`modules/strategy/internal/selection/evaluator.go`、`evaluator_test.go`。
**新增：** `modules/strategy/internal/selection/state.go`、`state_test.go`。

- [ ] 在 domain 中定义 RuleState：信号项保存 instrument_id、必要 entered_at；
  批次保存 offset、建立/到期 bar_end_time、后筛后 base_weights。
  Evaluation 返回 targets、按规则名组织的 rule_states 与解释，不返回 Trade 状态或 hold 动作。
- [ ] 新增 `TestSignalStateTransitions`：先 exit 后 entry；同周期退出不再进入；
  entry/exit 同真时 exit 优先；相等保持；固定池外旧持仓仍检查退出。
  后筛删除后从后态移除，下次必须再次满足 entry，不保留隐藏持仓自动恢复。
- [ ] 穿越夹具：上一期 ma20=9/close=10，本期 ma20=11/close=10，entry=true；
  两期 ma20 都为 11 时穿越 entry=false，但持续条件 entry=true。严格保留用户给定方向。
- [ ] 新增 `TestHoldingOffsets`：holding.bars=24、offsets=[0,12]、weight=0.60，
  每批预算 0.30；先清理到期，再建立命中 offset 的批次；未建批次预算留空；
  批次仅创建时筛选，期间不重分 base_weights；中断触发不补建中间批次。
- [ ] 新增 `TestRuleStateIsolation`：同一标的同时属于信号规则和两个批次，
  合成目标不能替代归属状态；一批到期不删另一批。任一规则失败时所有后态均不提交。
  无信号/无 holding 的普通规则没有跨期状态，不写空的持仓对象。
- [ ] 运行并预期 PASS：

```sh
go test ./modules/strategy/internal/domain ./modules/strategy/internal/selection
```

### Task 5：定义、Runner 与 Result 持久化

**修改：** `modules/strategy/schema/strategy.sql`、`schema_test.go`；
`modules/strategy/internal/store/database.go`、`database_test.go`、`store.go`、
`strategies.go`、`strategies_test.go`、`runners.go`、`runners_test.go`、
`results.go`、`results_test.go`、`runner_queries.go`、`runner_queries_test.go`；
`modules/strategy/internal/registry/service.go`、`service_test.go`。
**删除旧路径：** `modules/strategy/internal/store/inbox.go`；
`legacy.go` 中仅用于旧 Strategy 格式兼容的逻辑及调用方，不新增版本迁移平台。

- [ ] 先改 schema/结构测试，保留 definitions/runners/results/outbox 四张表。
  Result 增加 run_id、bar_end_time、dsl_yaml、rule_states 及解释，Runner 不持久化目标副本或结果指针。
  created_at/updated_at 等管理时间可保留，不参与决策排序。
- [ ] 用下面的唯一键和查询合同替换当前 latest-wins 索引，更新 database.go 的列校验与行转换：

```sql
CREATE UNIQUE INDEX IF NOT EXISTS ux_strategy_results_run_bar
ON t_strategy_results (runner_id, run_id, bar_end_time);
```

```sql
SELECT *
FROM t_strategy_results
WHERE runner_id = ? AND run_id = ?
ORDER BY bar_end_time DESC
LIMIT 1;
```

- [ ] 定义 CRUD 只保存 dsl_yaml。编辑与启用共用控制面串行校验：
  任一引用 Runner 启用时拒绝编辑；停用后可改，历史 Result 的 DSL 副本不可随之改变。
- [ ] CommitEvaluation 接收本次 Result 和预期前结果 ID；事务内复查 enabled/run_id、
  当前最近成功结果与预期一致、无更晚成功周期、目标未过期。
  本期已成功直接返回原行，失败不占周期；Result/后态/outbox 同事务，无单独状态更新。
- [ ] 新增 `TestCommitFirstSuccessfulBar`：定时和事件并发只插一条 Result/outbox；
  同期输入变化不覆盖；相同权重新期仍写 outbox；失败后同周期可成功；
  计算期间 run 改变、前结果改变或到期均整体回滚；观察模式不写 outbox。
- [ ] 新增 `TestRestoreLatestRuleState`：重启按当前 run 最大 bar_end_time 恢复；
  新 run 不读旧 run；跳过多个 bar 后仍读最近成功状态；保留策略不得删当前恢复点。
  去掉序号增长、同 hash 重算及旧字段存在的测试断言，不能只把测试删掉而不补新合同。
- [ ] 在空临时 SQLite 库加载 schema，并运行以下检查，预期 PASS；不读取或迁移个人实盘库：

```sh
go test ./modules/strategy/schema ./modules/strategy/internal/store ./modules/strategy/internal/registry
```

### Task 6：共同运行授权与管理协议

**修改：** `modules/trade/proto/trade_service.proto`；
`modules/trade/schema/logical_account.sql`；
`modules/trade/internal/domain/logicalaccount/account.go`、`account_test.go`；
`modules/trade/internal/application/logicalaccount/service.go`、`service_test.go`；
`modules/trade/internal/infra/store/logical_account.go`、`store.go`、`store_test.go`；
`modules/trade/internal/rpc/logical_account.go`、`logical_account_test.go`、`convert.go`；
`modules/strategy/internal/bootstrap/logical_account.go`、`logical_account_test.go`。
**生成并验证：** `modules/trade/proto/tradegen`，包括已有合同测试。

- [ ] Trade 显式保存当前授权 runner_id/run_id，替换当前 owner_generation 借用 c_owner_claimed_at 的行为；
  Claim/Rebind/Release 使用 expected 旧身份与 desired 新身份的原子比较，不靠跨服务墙上时钟排序。
- [ ] Trade 授权记录保留独立 auth_fence，仅为内部管理 CAS 元数据，不进入 Runner、DSL 或目标。
  Claim/Rebind 比较 expected 身份及 fence，实际状态转换生成新且不复用的 fence。
  空闲授权也保留该值：取消/释放原子推进新值，不恢复初始全空或任何旧值；
  取消遇到其他活动身份时不撤销它。Claim/Rebind 内部重试固定原 expected 值，不重新取值套用旧命令。
  已空闲且 fence 已变化时旧取消仅确认结果，不回写旧值；不新增历史版本表。
- [ ] 固定 Runner 三种组合：稳定停用=false+NULL；活动=true+run_id；未完成操作=false+run_id。
  Disable 保留旧 ID/目的地直到撤销确认，再清 ID；改绑必须先完成这一步。
  Enable 从稳定停用开始保存新 ID，授权确认后才 true；同一存活控制操作内部重试复用 ID。
- [ ] 新增启动恢复表驱动用例：true+ID 只在 Trade 同身份授权下恢复该 run，授权不同则停止；
  false+ID 一律取消并清理授权，确认后清 ID，不自动续启；false+NULL 不执行。
  准备身份后、新授权成功但本地未启用时崩溃，均走取消；用户之后显式启用产生新 ID。
  观察模式不调用 Trade。放弃未完成启用的自动续作，避免增加 pending_action/流程表。
  同一 Runner 的启停、编辑、授权协调和求值使用同一串行入口，撤销失败不能报告清理完成。
- [ ] 新增 `TestRunAuthorizationCAS`：expected 不匹配拒绝；desired 已是当前身份幂等；
  别的 Runner 占用拒绝；旧 run 迟到的 Release/Rebind 不影响新 run；
  新 run 不继承旧目标周期，普通重启不清目标。
- [ ] 新增 `TestDelayedClaimCannotReviveAfterCancel`：Claim(F0 -> runA) 挂起，
  Cancel 在 idle/F0 上提交 idle/F1，迟到 Claim 仍携带 F0，必须失败；
  重复取消不恢复旧检查值；已经由其他 run 接管时旧取消不能撤销新授权。
- [ ] 同步 RPC 转换和 Strategy 管理客户端，删除 generation=0 绕过验证的旧兼容分支。
  不改 Trade 订单 Version、独立订单管理及显式清仓接口。
- [ ] 通过真实 Makefile 生成代码，不手改 pb.go/trpc.go，再运行相关测试，预期 PASS：

```sh
make -C modules/trade/proto
go test ./modules/trade/proto/tradegen/...
go test ./modules/trade/internal/application/logicalaccount ./modules/trade/internal/infra/store ./modules/trade/internal/rpc
go test ./modules/strategy/internal/bootstrap
```

### Task 7：定时 / 事件统一触发与 outbox

**修改：** `modules/strategy/internal/trigger/processor.go`、`processor_test.go`；
`internal/trigger/eventconsumer/handler.go`、`handler_test.go`；
`internal/bootstrap/bootstrap.go`、`config.go`、`config_test.go`；
`internal/outbox/runtime.go`、`relay.go`、`publisher.go` 及对应测试；
`internal/store/outbox.go`、`outbox_test.go`。
以上 internal 路径均位于 `modules/strategy/`。
**新增：** `modules/strategy/internal/trigger/scheduler.go`、`scheduler_test.go`。

- [ ] 启用时解析五段 cron 和 timezone，注册可取消的进程内日程；使用成熟 cron 库，
  不手写 cron 解析器。回调保留原计划时刻，延迟执行不换成当前时间。
  事件保留原数据周期与来源信息；两者调用同一按 Runner/周期求值入口。
- [ ] 新增 `TestTimerEventFirstSuccess`：只定时、只事件、双触发均可运行；
  定时缺数后事件补算，定时成功后事件去重；旧周期不倒灌；
  启动不批量补历史 cron，非每 bar 强制求值。
- [ ] 将 ready 的实际依赖/标的范围判断合并输入加载流程，保留无关 binding degraded 不阻塞、
  相关 failed 不当 ready。暂时失败只在本周期有效期内有界重试，终态记录后 ACK；
  同一事件多个 Runner 独立按成功周期去重，不让已成功 Runner 重做。
- [ ] 框架运行配置提供默认目标有效长度 2 个 data.bar：
  effective_at=bar_end_time，valid_until=第 2 个后续有效 bar 结束；
  仍有效批次存在时取与最近批次到期时间的较小值。DSL 不增加 target 配置。
- [ ] outbox 冻结 target_id 和所有内容；发布前复核当前 run、enabled 与有效期。
  超期/旧 run 停止发布，不刷新时间。broker 确认丢失时仅重投原消息；
  本地停止不能证明 Trade 从未收到，最终保护由 Trade 自己执行。
- [ ] 扩展 outbox 测试覆盖确认丢失、身份变化、超期、空 FULL；
  保留 published 与 Trade accepted/filled 状态区别，失败摘要不伪装成功。
- [ ] 运行并预期 PASS：

```sh
go test ./modules/strategy/internal/trigger/... ./modules/strategy/internal/bootstrap
go test ./modules/strategy/internal/outbox ./modules/strategy/internal/store
```

### Task 8：目标事件与 Trade 接收 / 再次执行

**修改：** `packages/tradeeventpb/trade_events.proto`；
`packages/events/validation.go`、`validation_test.go`、`logical_account_target_test.go`；
`modules/strategy/internal/store/results.go` 及事件序列化测试；
`modules/trade/proto/trade_service.proto`、`internal/rpc/convert.go`；
`modules/trade/schema/logical_account.sql`、`target_receipt.sql`；
`modules/trade/internal/eventconsumer/target.go`、`target_test.go`；
`internal/infra/store/target.go`、`target_test.go`、`target_receipt.go`；
`internal/application/target/weight_resolver.go`、`weight_resolver_test.go`、`executor.go`、`executor_test.go`；
`internal/application/order/service.go`、`internal/infra/store/fact.go` 及对应测试。
本段缩写 internal 路径均位于 `modules/trade/`。

- [ ] LogicalAccountTargetWeightRequested 增加 run_id、strategy_id、bar_end_time、effective_at、valid_until，
  移除 command_sequence、signal_time、owner_generation。时间统一使用 Protobuf Timestamp，
  持久化为 UTC 毫秒；严格检查有效值、effective_at=bar_end_time、valid_until>effective_at。
  保留既有字段 1/2/3/5，新字段明确使用 8 至 12，不复用旧字段号 4/6/7：

```proto
message LogicalAccountTargetWeightRequested {
  string target_id = 1;
  string runner_id = 2;
  string logical_account_id = 3;
  repeated InstrumentWeightTarget targets = 5;
  string run_id = 8;
  string strategy_id = 9;
  google.protobuf.Timestamp bar_end_time = 10;
  google.protobuf.Timestamp effective_at = 11;
  google.protobuf.Timestamp valid_until = 12;
}
```

- [ ] 保留现有 `event.trade.target.weight_requested@1` 事件名、MOOX_TRADE stream 和
  `trade_target_weight_v1` durable，不为 DSL 去版本化另造 subject；
  这不表示支持旧 payload，新验证器必须拒绝缺少运行身份/期限的消息。
  同步 Strategy 序列化、事件验证、Proto、RPC 中的目标展示、Trade schema 和 row 映射。
- [ ] 增加旧 wire fixture 解码测试：旧 payload 即使解码成功，仍因缺少 run_id 和期限被新验证器拒绝；
  不添加旧字段到新字段的转换分支，也不只测新结构自己序列化后的往返。
- [ ] 接收严格执行第 1.4 节：授权/期限/周期检查先于 receipt 快路径和权益/报价读取，
  事务内再次校验，防止转换期间授权改变或目标到期。
  receipt 唯一周期键包含 space、logical_account、runner、run、bar_end_time，target_id 同样唯一。
- [ ] RequestHash 覆盖身份、周期、生效/失效时间及规范权重；删除 weight_resolver 业务时间解析失败
  后回退事件时间/当前时间的路径。已有 receipt 只能用于同一内容重试，不能越过旧 run 或过期判断。
- [ ] 当前目标写回 CAS 使用目标 ID/run/周期，旧任务不能覆盖新目标状态。
  executor 启动恢复、重试、继续收敛及子订单真正 Submit 前检查当前有效目标与授权，
  已准备但尚未提交的订单也受限。可由现有 OwnerID 回查目标，不重构整个订单域。
- [ ] 新增 `TestTargetRunAndBarContract`：新/同/旧周期、不同 ID 同周期、同 ID 改内容、
  旧 run 更晚时间、新 run 首目标、已接受 receipt 的旧身份重投、过期、边界等于 valid_until。
- [ ] 新增 `TestTargetExpiresBeforeSubmit`：接受时有效、转换后过期、重试时过期、
  订单准备后 run 被切换，均不得继续提交；不自动清仓。
  扩展现有 executor/order 测试，并保留 FULL omission 与空列表归零的回归。
- [ ] 生成并验证，预期 PASS；不要把旧“报价陈旧”测试当作目标有效期测试：

```sh
make -C packages/tradeeventpb
make -C modules/trade/proto
go test ./packages/tradeeventpb/... ./packages/events/...
go test ./modules/trade/proto/tradegen/... ./modules/trade/internal/rpc
go test ./modules/trade/internal/eventconsumer ./modules/trade/internal/infra/store
go test ./modules/trade/internal/application/target ./modules/trade/internal/application/order ./modules/trade/test
```

### Task 9：Strategy RPC、CLI 与管理台

**修改：** `modules/strategy/proto/strategy.proto`、`strategy_contract_test.go`；
`modules/strategy/internal/rpc/convert.go`、`service.go`、`service_test.go`；
`modules/strategy/cmd/cli/main.go`、`main_test.go`；
`web/src/api/strategy-types.ts`、`strategy.ts`、`strategy.test.ts`；
`web/src/store/modules/strategy.ts`；
`web/src/views/strategy/overview/index.vue`、`strategy-create-defaults.test.ts`；
`web/src/views/strategy/detail/index.vue`、`running/index.vue`；
`web/src/views/strategy/components/strategy-run-timeline.vue`、`strategy-target-table.vue`、
`strategy-operation-panel.vue`、`strategy-operation-panel.test.ts`、`strategy-status-badge.vue`；
`web/tests/strategy-console.spec.ts`；
`web/src/api/trade/types.ts`、`index.ts`、`trade.test.ts`；
`web/src/views/trading/logical-accounts/index.vue`、`logical-account-contract.test.ts`。

- [ ] RPC 定义对齐第 1.1 节。删除旧 source_code、namespace、trigger_bar_time、output_json、
  manifest_yaml、compiled_json、source_hash 等 Strategy 旧契约，不保留空兼容字段。
  ListStrategyTargets 从当前 run 最近成功 Result 返回目标及 bar_end_time，不返回 sequence。
- [ ] CLI validate 接受 DSL 文件，输出解析/绑定/编译错误位置，不再输出 API version、kind 或定义 hash。
  无真实绑定时只报告语法校验，不能宣称可执行；启用必须完成实际依赖验证。
- [ ] 管理台使用 dsl_yaml 编辑器和 rules 示例；展示 run_id、决策周期、目标、rule_states 解释、
  计算与投递状态。删除 compiled JSON 页签、sequence 标签、Runner 结果指针和重复 name 编辑。
  停用编辑约束由后端保证，不能只隐藏按钮。观察模式明确不投递。
- [ ] Trade 资金单元页面同步移除目标序号，展示运行身份、周期及有效期；
  将“恢复后继续收敛”改为仅对当前仍有效目标成立，不能暗示过期目标会恢复执行。
- [ ] 扩展现有 API/默认配置/操作面板测试：新请求字段、未知旧字段拒绝、停用编辑、
  重新启用 ID 改变、重复启用 ID 不变。Playwright 覆盖创建、启停、结果查看和异常提示；
  延续现有紧凑管理界面，不增加营销页或策略收益承诺。
- [ ] 生成并运行，预期 Go/Vitest/类型检查与构建均 PASS：

```sh
make -C modules/strategy/proto
go test ./modules/strategy/proto/... ./modules/strategy/internal/rpc ./modules/strategy/cmd/cli
pnpm --dir web test src/api/strategy.test.ts src/views/strategy/overview/strategy-create-defaults.test.ts src/views/strategy/components/strategy-operation-panel.test.ts
pnpm --dir web test src/api/trade/trade.test.ts src/views/trading/logical-accounts/logical-account-contract.test.ts
pnpm --dir web build:prod
```

### Task 10：集成验收与文档收口

**修改：** `modules/strategy/test/strategy_trade_external_e2e_test.go`；
`modules/strategy/internal/bootstrap/logical_account_external_e2e_test.go`；
`modules/trade/test/strategy_target_e2e_test.go`；
`scripts/test/e2e/test-strategy-trade-event-e2e.sh`；
`scripts/test/e2e/test-strategy-trade-logical-account-e2e.sh`；
`modules/strategy/docs/coin-selection-runtime.md`、`frontend-verification.md`；
本设计文档、本计划及 `docs/策略模块架构设计.md` 的状态说明。
仅有合同变化时更新相关部署合同测试，不在该任务执行远程部署。

- [ ] 更新本地 E2E 夹具为新 DSL、共同 run_id 和期限字段；覆盖定时缺数后事件成功、
  相同权重新期发布、信号跨期、后筛留空、旧 run/outbox 拒绝、Trade 执行前到期。
- [ ] 保留真实 NATS 外部测试的 PASS 标记检查；被 Skip 不算通过。
  使用现有本地隔离端口与临时数据目录，不读取生产账户配置。
  本地 paper/mock 结果不能被描述为真实交易所成交验收。
- [ ] 运行下列模块级门禁。根目录 `go test ./...` 不覆盖整个多模块仓库，不能替代这些命令：

```sh
go test ./modules/strategy/... ./modules/strategy/proto/strategygen/...
go test ./modules/trade/... ./modules/trade/proto/tradegen/...
go test ./packages/events/... ./packages/tradeeventpb/... ./packages/marketcalendar/...
go test -race ./modules/strategy/internal/store ./modules/strategy/internal/trigger/...
go test -race ./modules/trade/internal/eventconsumer ./modules/trade/internal/infra/store
bash scripts/test/e2e/test-strategy-trade-event-e2e.sh
bash scripts/test/e2e/test-strategy-trade-logical-account-e2e.sh
pnpm --dir web test
env -u MOOX_REMOTE_BASE_URL MOOX_REMOTE_PLAYWRIGHT=0 pnpm --dir web exec playwright test tests/strategy-console.spec.ts
git diff --check
```

预期全部 PASS。环境缺失、跳过或外部依赖失败单独记录，不包装成代码通过。
启动本地服务时按现有 Playwright 配置使用隔离环境；不得误用 remote 测试配置连接生产。

- [ ] 运行残留扫描，并逐项区分“待删旧 Strategy 合同”与“必须保留的其他模块概念”：

```sh
rg -n 'command_sequence|last_result_id|run_generation|DecisionState|TheoryState|prev\.' modules/strategy modules/trade packages/tradeeventpb web/src/api/strategy* web/src/views/strategy web/src/api/trade web/src/views/trading/logical-accounts
rg -n 'compiled_json|manifest_yaml|input_hash|api_version|ReadinessChecker' modules/strategy web/src/api/strategy* web/src/views/strategy
```

策略目标链不能再依赖旧序号/指针/定义 hash。其他模块输入 provenance、订单版本锁、
不相关 API 版本或领域对象不因命中字符串而删除。

- [ ] 用 codeCR 子 Agent 审查代码与测试，主 Agent 独立核验所有结论；存在独立调查任务时并行，
  等待全部结束后汇总。重点检查缺数误清仓、旧 run 重投、同周期覆盖、后筛重分配、
  状态恢复和订单提交前失效检查；未解决 P0/P1 不进入发布。
- [ ] 在本计划逐项记录实际完成范围、命令、结果与提交号，再勾选对应任务。
  设计文档标注已实现/仍未支持的能力；不复制原计划历史部署成功结论。
- [ ] 对不兼容数据库/队列切换单列操作说明：先停用并撤销旧授权，停止旧消费者，
  备份并确认旧数据处置、隔离旧消息，协调升级后再启用。未经用户授权，不执行清库或部署。

## 四、完成标准

只有以下条件同时满足，才可把本计划标记完成：

- [ ] DSL 示例可在明确输入绑定下执行，定时与事件均走同一周期合同。
- [ ] bars[0]/bars[-1]、初筛后标准化、top/tail、先分配后筛、信号/批次状态符合设计。
- [ ] Runner 无 command_sequence/last_result_id，运行隔离和最近成功状态恢复仍可靠。
- [ ] Strategy/Trade/协议/持久化/UI 同步使用 run_id、bar_end_time，不存在单边字段精简。
- [ ] 所有必需规则成功才形成 FULL，Result/RuleState/outbox 原子；缺数不清仓。
- [ ] 旧身份、旧周期、同 ID 改内容和过期目标不能启动新交易。
- [ ] 本地测试、跨进程事件验证、独立审查及文档状态有本轮实施证据。
- [ ] 生产发布与真实交易验证仍作为单独授权事项，不与本地计划完成混淆。
