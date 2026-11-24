# 策略 DSL 语法与写入流程

策略用一份 YAML（DSL）描述：怎么在截面上选标的、怎么配权。时序计算（均线、动量、分位）一律写成因子（见 [factor-dsl.md](factor-dsl.md)），
策略只在截面上过滤、打分、配权。Go 代码只提供一种通用规则类型（`rank`），新策略写配置即可，不改代码。

按功能讲写法、带例子与逐步推演的完整手册见 `docs/策略DSL手册.md`（含单标的上限、进出场持有、策略池漂移与组合约束的推演）。
本文是 Agent 写策略时的语法速查；字段的完整语义、状态与明细以 `docs/模块/策略.md` 的第 3 节为准，解析与校验的源码在 `modules/strategy/internal/dsl/`。
整套 Python 策略迁移成 DSL 的流程见 [strategy-migration.md](strategy-migration.md)。

## 1. 顶层结构

```yaml
name: dual_ma_swap_1m          # 策略名
bar: 1m                        # 可选断言：与 View 周期不一致时拒绝启用
market: {select: swap}         # 可选：市场模式（见下），不写按绑定任务的市场取默认值
universe: {min_age_bars: 15}   # 策略级默认标的池，省略即全部
rules: [...]                   # 至少一条规则（或写 strategy_pool，见下，二选一）
portfolio: {leverage: 1}       # 组合级约束
factor_params: {...}           # 可选：可变成员的逐期调参（见 docs/模块/策略.md「动态调参」）
sources: {daily: {bar: 1d}}    # 可选：附加列来源，规则里写 daily.列名（见下）
```

- 解析严格：重复键、未知字段、多文档一律拒绝，错误信息是中文。
- 规则里引用的列名就是**因子任务结果数据集的列名**，由成员键推导：`因子ID[__参数段][__输出名]`，参数全为默认值时省略参数段，可变成员以 `__var` 结尾
  （例如成员 `ma` 窗口 5 的列是 `ma__5`）。创建实例选好因子任务后，页面和校验报错都会列出该任务的全部列。
- 限制：DSL 文本 ≤ 64 KiB；单条表达式 ≤ 1024 字符、≤ 256 个语法节点；`portfolio.leverage` ≤ 10。

### market：市场模式

```yaml
market:
  select: spot | swap | both   # 从哪个市场的币里选
  prefer: spot | swap          # 一个币在两个市场都可用时，优先用哪个下单
```

只允许 5 种组合：

| select | prefer | 含义 | 实例要绑定的因子任务 |
| --- | --- | --- | --- |
| spot | spot | 纯现货 | 现货 1 个 |
| swap | swap | 纯永续 | 永续 1 个 |
| both | spot | 两边选，现货优先 | 现货、永续各 1 个 |
| both | swap | 两边选，永续优先 | 现货、永续各 1 个 |
| spot | swap | 现货选币，有永续就用永续下单 | 现货、永续各 1 个 |

- 不写 `market` 时按实例唯一绑定的因子任务的市场取默认值（现货任务就是 spot/spot）；绑定了两个任务必须写。`select` 为 spot 或 swap 时可省略 `prefer`。股票（A 股、港股）不允许写 `market`。
- 同一个币的现货和永续是两个独立的目标，不互相抵消；空头一律在永续。
- 上期已持有正权重目标的币，本期沿用它所在的市场（避免永续偶尔缺数时来回倒仓），直到权重归零。
- 一个市场的因子本期被跳过，或失败（时序因子在该市场整体晚到、读取的标的全部失败；失败的标的占比超过 `portfolio.max_missing`，已持仓的币即使年龄没通过也计入）时，这个市场的币本期不可用：已持仓的保持不动（包括永续空头），新开仓用另一个市场。选币市场、或所有市场都这样不可用时整期按 `market_failed` 跳过（数据类原因，不是“积累中”，持续出现要看数据集新鲜度告警）。只涉及单个市场的策略仍按普通缺数处理。
- **输入方式**：涉及现货和永续的模式，现货、永续各绑一个因子任务（`--factor-task spot=… --factor-task swap=…`）；永续上失败的币现货照常，永续的因子被跳过时永续不可用、已持仓的永续保持不动。
- **规则级市场**：模式含两个市场（`both/*`、`spot/swap`）时，规则可写 `market: spot` 或 `market: swap`，这条规则只用该市场的因子值选择、目标也落在该市场，同一个币在两条规则里可以落在不同市场（不互相抵消）；不沿用上期持仓市场，缺数与年龄按这个市场判断，这个市场不可用时只冻结它在这个市场的持仓。写了它的策略必须显式写 `market`；单市场模式下只能写与策略相同的市场；`market: spot` 的规则不能做空。
- 现货、永续两个任务的策略也可以写 `factor_params`：同一份参数提交给两个任务，可变成员在两个任务里都要有。
- 现货权重之和超过 1、总敞口超过 `leverage` 时，按比例缩小并在摘要注明，不会整期跳过。

- **参考市场**：单市场策略可写 `market: {select: swap, reference: [spot]}`，额外绑定另一个市场的任务（`--factor-task spot=…`），只用于 `age_markets: all` 的年龄判定，不参与选择与下单。只能写模式不涉及的市场；必须有规则写 `age_markets: all`；不支持 `factor_params`、池成员不能写。

### sources：附加列来源

主帧仍是 `bar`，`sources` 让规则读更慢频率任务算好的列（例如小时帧读日线均线）：

```yaml
bar: 1h
sources:
  daily: {bar: 1d}                       # 至多两个；名字是 snake_case
rules:
  - {id: pick, type: rank, filter: "daily.close > daily.ma__60", score: "rank(daily.roc__20)", select: {top: 5}, weight: 1}
```

- 实例为每个来源按主帧的任务键各绑一个任务：`--factor-task daily:spot=…`（双市场主帧给 `daily:spot` 与 `daily:swap`）。来源任务的周期要等于来源的 `bar`、是主帧周期的整数倍（股票，即 A 股、港股，只能是 `1d`，主帧必须是日内周期），不能有可变成员。
- 每根主帧读来源里收盘时刻不晚于这根收盘时刻的最新一根（as-of）；读不到或缺了一期按缺数处理。收盘时刻对齐的那一根（如 00:00、A 股 15:00）等主帧与来源都到齐，其余时刻只等主帧；来源指纹变化在对齐的那一根触发 `factor_changed`。
- 每个来源至少要被一个表达式或 `select.group.by` 引用，否则保存即拒绝。
- 分组名额可以按来源列分组：`select: {top: 5, group: {by: daily.industry, max: 2}}`（来源名要已声明，列名部分是普通列名）。分组值取这一根主帧按 as-of 读到的来源值；读不到、缺一期或这一列为空的标的按缺数处理（`missing:daily.industry`），与主帧的分组列口径一致。
- 主帧很快、来源很慢（如 5m 配 1d）时，日线可能比对齐后的一根主帧晚就绪，那几根来源列缺数：写了 `allow_empty` 的规则会清仓再买回，这类规则请配 `on_missing: {keep_bars: N}`。
- A 股日线的时间键是交易日期的 UTC 零点（采集与策略一致）。启用 A 股来源之前，先确认 A 股日线采集任务（内置的是“A 股日线（复权、行业、财务、换手率、股本）”）与对应的 1d 因子任务已启用并补齐历史，步骤见 [`ashare-daily.md`](ashare-daily.md)。交易日 D 的日线在北京时间 D+1 日 08:00 才采集：D 日 15:00 那一根主帧要等到那时才求值，盘中各根读的是 D−1 日的日线。日线数据集的附加字段可以直接读，例如按申万一级行业分组 `group: {by: daily.sw_l1, max: 3}`。
- 规则状态、持有计数、止损、冻结、缺数都只按主帧算。`bars[-1].daily.x`、`factor_params` 读来源列都不支持；池与池成员不能写 `sources`；写了 `sources` 的策略可以研究回放（两种来源下来源列都读 View）。

### strategy_pool：策略池

一个策略写 `rules` 或 `strategy_pool`，二选一。池由若干成员（引用已有策略）组成，按成员模拟净值分配资金：

```yaml
name: 双策略轮动
bar: 1h                                     # 必须写；成员写了 bar 的要与池一致
strategy_pool:
  members:
    - {id: a, strategy: strategy_xxx}       # id 是 snake_case；strategy 是已有策略的 ID
    - {id: b, strategy: strategy_yyy}
  allocation:
    method: rotation                        # fixed（写 ratios: {a: 0.6, b: 0.4}，覆盖所有成员）、rotation 或 drift（见下）
    rotation:
      factors: [{name: return, window: 24, weight: 1}, {name: ma_ratio, window: 72, weight: 1}]   # return / ma_ratio / drawdown，可选 order: asc|desc
      top: 1                                # 前几名，并列的平分
  timing: {window: 24}                      # 可选：净值在 24 期均线之上取 1，否则取 0
  step: 0.2                                 # 可选：每期每个成员的比例变化上限
  fees: {spot_bps: 10, swap_bps: 5, equity_bps: 3}   # 可选：成员模拟账本的手续费（股票成员用 equity_bps）
  # fees.funding_column: funding_rate       # 可选：永续持仓按这一列（本根累计的资金费率）计资金费
portfolio: {leverage: 1, max_weight: 0.1}
```

- 现货、永续成员必须显式写 `market`；股票策略不写 `market`，池绑定 `equity` 任务时按股票成员处理（股票与现货、永续成员不能混用，股票池只做多、`leverage ≤ 1`，停牌不清算）。成员不能是池、不能用 `factor_params`、不能写 `sources` 与 `market.reference`，写了 `bar` 的要与池一致（这些保存池定义时就检查）；没写 `market` 的现货、永续成员保存时放行，启用时拒绝。池不写 `market`、`universe`、`factor_params`。
- 组合约束（按成员汇总的分配比例，要求每个成员单方向：规则全部做多或全部做空）：
  ```yaml
  strategy_pool:
    allocation:
      method: fixed
      ratios: {long_core: 0.5, short_core: 0.45, short_new: 0.05, probe: 0}
      switch:                               # 条件转移（用于 fixed 与 drift），第一个成立的生效
        - when: {empty: probe}              # probe 本期有效目标为空（没通过时按沿用的上期目标判断）
          move: {from: short_new, to: [short_core]}   # from、to 必须同方向
    exposure:                               # 净敞口 = 多头 − 空头，各段按顺序执行
      bands:
        - {min: -0.1, max: 0.3, adjust: auto, pivot: 1.0}   # adjust：reduce_larger / raise_smaller / auto（auto 必须写 pivot）
        - {when_gross_below: 1.0, min: 0, max: 0, adjust: raise_smaller}   # 条件也可以写 when_gross_above
    gross: {min: 1.0, max: 1.8}             # 总敞口区间（min、max 至少写一个）；第一根不放大；min、max 都不超过 portfolio.leverage
  ```
  超出区间按超出方向修正：净敞口高于 `max` 时 `reduce_larger` 缩多头、`raise_smaller` 放大空头；低于 `min` 时反过来。三者只改合成用的比例，步长仍按约束之前的比例算。
- 漂移分配（`method: drift`，要求每个成员单方向；不能写 `timing`、`step`、`rotation`）：分配系数随成员持仓的名义价值漂移，再按两侧各自的规则拉回 `ratios` 的侧内构成：
  ```yaml
  strategy_pool:
    allocation:
      method: drift
      ratios: {long_core: 0.3, long_majors: 0.2, short_a: 0.25, short_b: 0.05, short_c: 0.2}   # 目标比例
      start: "2026-01-17T07:00:00Z"         # 可选：漂移起点（某根 bar 的 bar_start，或交易日期），预热从这一根开始
      # initial: {at: "2026-01-17T07:00:00Z", ratios: {...}}   # 与 start 二选一：在 at 这一根把系数置为给定比例
      sides:                                # 不写的一侧不再平衡
        long: {rebalance: every_bar}
        short:
          rebalance: both                   # none（默认）| every_bar | periodic | threshold | both
          period_bars: 2160                 # periodic、both 必填：每隔这么多根
          anchor: "2026-01-17T07:00:00Z"    # 可选：periodic 的相位起点，默认漂移起点
          threshold: 0.6                    # threshold、both 必填：|侧内占比 ÷ 目标侧内占比 − 1| 超过它触发
          spread_bars: 24                   # 可选：触发后分几根走完，默认 1（一次到位）
          drift_by: notional                # notional（默认）| nav：侧内构成按成员资金收益漂移
    freeze: {watch: short, window_bars: 1, threshold: 0.10, bars: 4}   # 可选，只用于 drift：watch 写 long、short 或 [成员 id…]
  ```
  `period_bars`、`anchor` 只能配 periodic、both，`threshold` 只能配 threshold、both，`spread_bars` 不能配 none。`start`、`initial`、`sides`、`freeze` 写在 fixed、rotation 下保存即拒绝。时刻必须是池周期上某根 bar 的起点（池不写 market，启用时才检查）；从 `start` 到最新已收盘的一根不能超过 20000 根，View 的历史要覆盖到起点，否则启用时拒绝。
- 实例要绑定的因子任务是成员所需市场的并集（现货、永续各最多一个）；View 要有 `close` 列，数据集保留期要装得下预热。
- 启用后先在后台预热（用历史推进成员净值），预热完成前不求值（事件稍后重投）也不能 `confirm`；停用再启用时池的 DSL、绑定解析、成员都没变就沿用规则状态，只把停用期间漏掉的根用历史补上，变了（或停用太久、状态早于预热起点）才从头预热。
- 成员净值、比例、择时与贡献写在每期结果的 `summary_json.pool`，页面在实例结果里显示。
- 研究回放支持策略池（只能用结果 View 来源，“因子现算”不支持）：`factor_tasks` 给成员市场的并集；回放发起时固化成员（从实例发起取会话的成员快照，否则取成员的当前定义），排队期间改成员定义不影响；资金费只按池 DSL 的 `fees.funding_column`，回放任务上不能写 `funding_column`。drift 写了 `start` 或 `initial.at` 时回放起点必须等于它：写 `start` 的与同起点的预热逐期一致，写 `initial` 的只有 `at` 那一根的比例一致（预热会从 `at` 往前多推成员所需的期数）。每期记录的 `summary_json.decision.pool` 与 `decision.limits` 是池明细；`strategy_check.py targets --replay 名称=回放ID` 导出目标再用 `compare` 比对。
- 成员净值是模拟曲线（不模拟强平；写了 `fees.funding_column` 才计资金费：上一根收盘调仓之前的永续持仓乘本根费率，费率为正时多头付），只用来分配资金，不代表实盘收益。语义与计算顺序见 `docs/模块/策略.md` 的“策略池”。

### universe 与 portfolio

| 字段 | 说明 |
| --- | --- |
| `universe.tags` / `include` | 任一非空时取并集作白名单；都为空表示全部；再减去 `exclude_tags` 与 `exclude` |
| `universe.min_age_bars` | 标的在这么多根之前已有数据才参与；规则没写规则级 `min_age_bars` 时生效 |
| `universe.max_age_bars` | 只保留年龄小于这么多根的新标的；写了它的规则必须同时写 `allow_empty: true` |
| `universe.age_markets` | `each`（默认）任一市场通过即算通过；`all` 要求在绑定的全部市场上都通过 |
| 规则级 `universe` | 规则里写 `universe`，写了的字段逐项覆盖策略级；写了显式 `pool` 的规则只能写年龄类字段 |
| 规则级 `allow_empty` | `true` 时这条规则不参与整期守门（`universe_too_small`、`min_universe`、`max_missing`），候选为空只是不出目标 |
| `portfolio.leverage` | gross 上限，默认 1；纯现货与股票不允许大于 1，只有市场模式含永续才可以；各规则 `weight.total` 之和不超过它 |
| `portfolio.max_weight` | 组合级单币上限，同一个币的现货、永续**合计**（绝对值之和，“现货多 + 永续空”的对冲也按绝对值之和截断）；两边都不为零时按比例缩小，只有一边时取上限，超出部分留现金 |
| `portfolio.min_universe` | rank 规则进入 filter 的可用标的少于它则整期跳过 |
| `portfolio.max_missing` | 任一规则缺数占比超过它则整期跳过，默认 0.2 |
| `portfolio.limits` | `{long_max, short_max, overflow: {to}}`：单标的多头、空头上限（占权益，同一标的各市场同方向合计），在规则相加（池合成）之后、`max_weight` 之前执行；超出部分转给 `to` 指向的规则（池里是成员）本期同方向的其他标的，按它的权重比例分，最多三轮，剩下的留现金；已截到上限的标的、冻结标的不作为去向，池里本期比例为 0 的成员没有可分的标的；不写 `overflow` 时留现金 |

## 2. 规则类型

每条规则必须有稳定的 `id`（`^[a-z][a-z0-9_]{0,63}$`）；**改 id 等于规则状态从零开始**。`name` 只用于展示。

| `type` | 用途 | 关键字段 |
| --- | --- | --- |
| `rank` | 截面选股；配显式 `pool` 与 `hold` 也用来对固定标的择时 | `filter`、`score`、`select`、`weight`，可选 `pool`、`holding`、`hold`、`filter_after`、`side`、`stop`、`scale`、`on_missing`、`market` |

### rank 规则

```yaml
- id: ma_above
  type: rank
  filter: "ma__5 > ma__10"                 # 布尔表达式，淘汰的标的记为 filtered
  score: "rank(ma__5 / ma__10 - 1)"        # 数值表达式；rank(x)、zscore(x) 在通过 filter 的样本上计算，可嵌套
  select: {top: 10000, buffer: 2}          # top 与 bottom 二选一；where 二次筛选；buffer 是排名缓冲区
  weight: {total: 1, method: equal, cap: 0.05}   # method: equal | rank；cap 是规则内单标的上限
```

- 保存时要求 `cap × 选中数量 ≥ total`（预算分得完；写了 `then` 时选中数量取 `then` 的个数；个数要到运行时才知道的写法跳过这项检查，即 `then` 写比例，或没写 `then` 而 `top`/`bottom` 写比例，分不完的留现金）。`select.top` 写成大于标的数的值就等于全选。
- `select` 的扩展：`top: 0.1`（(0, 1) 是比例，分母是通过 filter 且分数有效的样本数）配 `count_round: floor | ceil | round`、`count_min`；`range: [10, 13]` 名次闭区间（与 top/bottom、buffer 互斥）；`group: {by: 列, max: 3}` 或 `{by: 列, quotas: [2, 1], rest: 1}` 分组名额（分组值 NaN 的不入选）；`then: {score: "表达式", top: 5}` 在第一段结果上二次排序（与 buffer、hold 互斥；`top`、`bottom` 也可以写 (0, 1) 的比例，分母是第一段选中且分数有效的个数，配 `count_round`、`count_min`）。
- `holding: {bars, offsets}` 分批换仓，与 `buffer` 互斥；`offsets: all` 等于写出 0 到 `bars − 1`，即每根重建一个批次、每个批次持有 `bars` 根（批次最多 64 个，所以这时 `bars` 不能超过 64；要更长就放大 `bars`、`offsets` 取稀疏的几个）；`anchor: "<RFC3339 的 bar_start>"`（也可写交易日期 `YYYY-MM-DD`，取该日第一根）只改相位。日历换仓写 `holding: {calendar: trading_days | week | month, every: 5（仅 trading_days）, days: [1, -1], at: first | last}`，与 bars/offsets 互斥；建仓 bar 没有 ok 决策时在之后第一根 ok bar 补建。`filter_after` 只剔除不补位，剔除的权重留现金。
- `hold: {entry, exit, max_bars, cooldown_bars, allow_reentry}` 进出场持有：持有中 exit 为真、不满足 filter、持满 max_bars、止损、缺数即出场（stop 优先），空位按名次从 entry 为真且 exit 不为真的候选里补，目标数取 `select` 换算的个数（持有多于目标不裁减）。持有标的只缺 score、entry 的列时不出场。与 buffer、holding.bars/offsets、then、range、filter_after 互斥；可与 `holding.calendar`（只能一个 days）同写，只在建仓 bar 判进出场。；`allow_reentry: true` 让候选只看 entry（出场条件为真的标的也能入场、刚出场的标的当根可补回，要求 cooldown_bars 为 0），默认不写
- `scale: {expr, max}` 在规则配权（含 cap）之后逐标的乘系数，截到 [0, max]（max 默认 1、上限 10），乘掉的留现金；系数为 NaN 按缺数处理。`on_missing: {keep_bars: N}` 让持有标的本根缺数时冻结保持，连续缺数超过 N 根才移出。
- 已持有的标的停牌时自动冻结，保持上期目标（明细 `held(suspended_hold)`），不用配置：不计入缺数比例，同批次其他标的照常换仓，复牌后解除；退市的持仓照常清零。回放不做停牌冻结，与实盘不同。
- rank 表达不了的写法（参数表式的大量子策略、按数量持有、过滤条件里用名次或分位等）见 `docs/模块/策略.md` 的“表达不了的写法（已知限制）”表；`filter_after` 是保留条件（条件为真的留下），与外部框架“成立就剔除”的后置过滤方向相反。
- `side` 取 `long`（默认）或 `short`；空头只走永续，纯现货与股票不允许做空。
- `market: spot | swap` 是规则级市场（见第 1 节“market：市场模式”）。

### 固定标的择时

不选股、只对固定的几个标的判断进出，写显式 `pool` 加 `hold`：

```yaml
- id: btc_trend
  type: rank
  pool: [BTC-USDT]                          # 固定列表，或 {tags: [...]}（标签池保存时不检查成员数，见下）
  score: "0"                                # 不排序，写常数即可
  select: {top: 10000}                      # 足够大的上限：池里满足条件的都持有
  hold:
    entry: "bars[-1].bias <= 1 && bars[0].bias > 1"
    exit: "bars[0].bias < 1"
  weight: {total: 0.2}
```

- 每根先对持有的标的判 `exit`，再对其余标的判 `entry`；两者同根都成立不入场；持有的标的平分规则预算。
- 它与其他 rank 规则一样参与整期跳过的检查：池里本期没有可评估的标的时整期跳过（`universe_too_small`），同策略的其他规则也不出目标——全部年龄不够，或池与本期基础集合没有交集（标的停用、退市，标签池没有成员，ID 写错）。不写 `allow_empty` 时，1～4 个标的的小池按默认 `max_missing: 0.2` 缺一个就整期跳过。
- 池里的标的数少于 `portfolio.min_universe`：固定列表保存即拒；`{tags}` 池保存不拒，运行时成员少于 `min_universe` 就每根整期跳过。两种都要写 `allow_empty: true`；写了之后这条规则不参与整期跳过的检查，持有的标的普通缺数时出场（`exit_missing`；市场不可用、只缺上一根的仍冻结保持），建议同时写 `on_missing: {keep_bars: N}` 让缺数先冻结。
- 有标的被冻结时，冻结的一份按上期权重保持，其余持有标的按含冻结的个数平分预算；冻结标的上期份额大于本期平分份额时规则合计会超过 `weight.total`（预算 0.5：上期 A 独占 0.5，本期 A 冻结、B 入场得 0.25，合计 0.75），只受 `portfolio.leverage` 封顶，冻结解除后恢复。
- 止损等同出场，明细原因是 `exit_stop:<原因>`（例如 `exit_stop:stop_loss`）。

## 3. 表达式

用 Expr 编写，只能引用：

- 结果 View 的列（列名就是成员键）；
- `bars[0].<列>`（当期）、`bars[-1].<列>`（日历上紧邻的上一根）；`bars[...]` 之后只能用 `.列名`；
- `instrument_id`；仅 `select.where`、`filter_after` 里还能用 `score`；
- `params("成员键", "参数名")`：读取可变成员这一行实际用的参数；
- `来源名.列名`：写了 `sources` 时读附加来源的列（见上）。

禁用与注意：

- 内置函数一律禁用，只开放 `rank`、`zscore`（仅 `score` 与 `select.then.score` 中可用）和 `params`。`rank` 同分取平均名次、单样本记 0.5；`zscore` 零方差记 0。
- `matches` 右边只能是字符串字面量的正则；`??`、范围 `..`、`in` 不可用。
- 三元表达式 `a ? b : c` 两个分支类型必须一致。
- 数字写法不超过 64 个字符、精度不超过 18 位小数、绝对值小于 10^9。
- 通过 filter 的候选全部分数无效时整期 `skipped(config_error)`，不当作“选不出标的”清仓。

## 4. 止损止盈

写在规则上：

```yaml
stop:
  loss: 0.05            # 固定止损：相对入场价不利变动达到 5%
  trailing: 0.08        # 跟踪止损：相对此前最有利价格回撤 8%
  profit: 0.10          # 止盈：相对入场价有利变动达到 10%
  price: close          # close（默认）| extreme
  cooldown_bars: 12     # 触发后这么多根内本规则不能重新入场，默认 0
```

至少写一项；`loss`、`trailing` 取值 (0, 1)，`profit` 取值 (0, 10]。按收盘判断，不是盘中触价；View 必须有 `close`（`extreme` 时还要 `high`、`low`）。

## 5. 校验、创建与上线

写入生产的策略定义和实例属于外部写操作，每一步先告诉用户并等确认。

1. **本地先看语法**：用 `moox-cli strategy validate`（只校验语法与结构，并在选定因子任务的结果 View 最近一期上试算一次，结果不落库；引用了不存在的列时报错带最接近的列名和该任务的全部列）。

   ```bash
   moox-cli strategy validate --dsl-file s.yaml --factor-task spot=<现货任务> [--factor-task swap=<永续任务>]
   ```

2. **创建定义**：`moox-cli strategy create --dsl-file s.yaml`；已有定义用 `strategy update --strategy-id <ID> --dsl-file s.yaml` 修改；删除用 `strategy delete`（默认只预览）。创建后用 `strategy show --strategy-id <ID>` 读回 DSL 核对。
3. **创建实例**：`strategy instance create --strategy-id <ID> --factor-task <市场>=<因子任务>`（可重复：市场模式含现货和永续时 `spot=…` 与 `swap=…` 两个都要给；股票策略 `equity=…`；默认停用；加 `--enable` 创建即启用）。市场必须是任务源数据集真实的市场。因子任务要是本空间里运行中的任务（启用时核对），**创建后不能改**；一个任务可以被多个实例引用，只有 DSL 写了 `factor_params` 的实例启用时独占关联主帧任务：创建时不检查，主帧任务已关联到别的实例时启用被拒（“关联的是实例 X，不是本实例”）。已停用的实例可以用 `strategy instance delete --instance-id <实例>` 删除，关联到它的因子任务和结果数据集一起删除、不可恢复（还被别的实例引用时拒绝，先删引用方）；没关联到它的任务保留，只有固定成员的任务不随任何实例删除，不用了要手动停用；停用期间可以用 `strategy instance update` 改绑策略定义与账户。
   不挂账户的是观察实例：只计算目标权重、不投递，适合先验证。
   需要先看历史表现时用 `strategy replay start`（与 `validate` 同样给 `--factor-task`，加 `--from`、`--to`、费率与可选的 `--slippage-bps`）；声明了 `factor_params` 的策略只能加 `--input compute`（因子现算），它不支持策略池与股票。
4. **启用**：`strategy instance enable --instance-id <ID>`（`disable` 停用；两者不带 `--yes` 只预览，确认后加 `--yes`）。启用时才解析列、成员契约和市场类型，列不存在或现货做空等问题在这一步被拒绝；
   任务刚建、结果 View 还没数据时可能因“还没有数据（覆盖范围未知）”被拒，等任务算出几个周期后再启用。
   挂账户的实例停用后，Strategy 调一次 Trade 关闭会话，Trade 撤掉最后一份目标的挂单（持仓不动）。返回“实例已停用，但 Trade 未确认”时实例已经停用，
   等 Trade 可用后对它再执行一次 `disable --yes` 重试；没确认期间最后一份目标最多执行到有效期结束（2 根 K 线），要立刻停手就请用户在管理台暂停这个组合账户的自动执行。
5. **看结果**：`strategy result list --instance-id <ID>`、`strategy target list --instance-id <ID>`（当前会话最新的 ok 目标）；控制台 `/strategy/detail/<实例ID>`。
   挂账户的实例启用后先试运行（只发带影子标记的目标，Trade 不下单），观察实例启用即确认。确认实盘要用户决定：
   1. 先 `trade shadow list --instance-id <ID>` 把影子目标给用户看；
   2. `strategy instance confirm --instance-id <ID>` 预览，拿到预览里的 `session_id`；
   3. 用户确认后执行 `strategy instance confirm --instance-id <ID> --session-id <预览里的 session_id> --yes`，也可以请用户在管理台点“确认实盘”。

   `lifecycle` 命令只管模块级的暂停与恢复，不能确认实例。

### 修改已有策略

- 启用时定义会写入会话快照；被启用中的实例引用的定义不能修改，要先停用、改完再启用（启用期间定义被改，这次启用整体回滚，不留下会话）。
  **停用后重新启用会新建会话，但 DSL、绑定解析、成员都没变时规则状态（持有集合、holding 批次、hold 的入场序号、止损入场价与冷却）沿用**，停用的那几根算时间流逝。
  修改实例（换账户或定义，原样提交也算）或改 DSL 文本（加一行注释也行）会清空，改 `id` 同理。人工平仓后要让策略从零开始，必须先清空再启用，否则启用后第一期会把平掉的标的买回来。
- 清空规则状态（管理台不能改实例，只能用命令行），两条路任选其一：
    - 原样提交一次实例修改，只清这一个实例：
        1. 停用实例（停用的实例上没有会话，停用之后马上能 update）；
        2. `moox-cli strategy instance show --space-id <空间> --instance-id <ID>`，记下 `strategy_id` 和 `logical_account_id`；
        3. `moox-cli strategy instance update --space-id <空间> --instance-id <ID> --strategy-id <原值> --logical-account-id <原值>`。两条命令的 `--space-id` 默认是 `crypto`，别的空间的实例不写会报“实例不在当前空间”。
           **漏写 `--logical-account-id` 等于解绑组合账户**（这个参数是整体替换，留空表示观察实例）；原来就是观察实例的不写；
    - 改一下 DSL 文本（`strategy update`）：代价是共用这个定义的所有实例下次启用都会清空。
- 引用的因子定义哈希变了（见 [factor-dsl.md](factor-dsl.md)“修改已有因子的定义”），引用它的实例同样要重新启用。
- 改动较大时，优先新建一份策略定义和观察实例并行跑一段时间，再切换。

## 6. 常见错误

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 保存即拒绝，提示未知字段 | 解析严格，拼错键名或写了不存在的字段（进出场条件写在 `hold` 下，规则上没有 `entry`、`exit`） | 按本文字段名核对 |
| 保存即拒绝，提示 type 只能是 rank | 规则类型只有 `rank` | 固定标的择时写成显式 `pool` 加 `hold`（第 2 节“固定标的择时”）；每根开一批写成 `holding: {bars, offsets: all}` |
| 回放被拒，提示声明了 `factor_params` 的策略只能用“因子现算”来源 | View 里的历史因子值是按当时提交的参数算的 | 加 `--input compute`；策略池与股票不支持现算，这类调参策略不能回放 |
| 池定义保存被拒，提示成员不存在或不能进池 | 成员是池、用了 `factor_params`、写了 `sources` 或 `market.reference`，或 `bar` 与池不一致 | 先修好成员策略再引用 |
| 池实例启用被拒，提示成员没有显式写 market | 现货、永续成员必须写 `market`；只有池绑定 `equity` 任务时，成员按股票成员处理、可以不写 | 给成员策略补上 `market` |
| 提示 `cap × 选中数量 < total` | 预算分不完 | 调大 `cap` 或 `select.top`，或调小 `total` |
| 提示使用了 `max_age_bars` 必须写 `allow_empty: true` | 只保留新标的的规则经常没有候选，不写会让整期守门跳过 | 在这条规则上写 `allow_empty: true` |
| 提示互斥（hold 与 buffer / then / range / filter_after / holding.bars，range 与 top、buffer，then 与 buffer，calendar 与 bars） | 这些写法语义冲突 | 按提示二选一 |
| 启用被拒，提示 holding.anchor 不是 bar 起点 | 锚点要写某根 bar 的 bar_start（股票日线——A 股、港股都一样——是交易日期的 UTC 零点，如 `2026-10-09T00:00:00Z`，可直接写交易日期） | 改成 bar 起点或交易日期 |
| 规则写 `market` 被拒 | 策略没显式写 `market`；单市场策略写了不同的市场；`market: spot` 的规则做空；取值不是 spot、swap | 显式写策略的 `market`（双市场模式才能另选）；现货规则改成多头或改 `market: swap` |
| 启用被拒，提示列不存在 | 规则引用的列不在因子任务结果里 | 按成员键推导列名；默认参数的成员省略参数段 |
| 提示 `x 不是已声明的来源` 或不允许属性访问 | 表达式写了 `x.y`，但 `sources` 没有声明 `x` | 在 `sources` 里声明来源，或改回主帧的列名 |
| 启用被拒，提示来源要按主帧的任务键各绑定一个任务、或周期不是整数倍 | 来源任务少给了市场，或来源周期与主帧对不齐 | 按 `来源名:市场=任务` 补齐；换成周期是主帧整数倍的任务 |
