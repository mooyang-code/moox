# 策略 DSL 语法与写入流程

策略用一份 YAML（DSL）描述：怎么在截面上选标的、怎么配权。时序计算（均线、动量、分位）一律写成因子（见 [factor-dsl.md](factor-dsl.md)），
策略只在截面上过滤、打分、配权。Go 代码只提供通用规则类型（`rank`、`signal`、`tranche`），新策略写配置即可，不改代码。

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
- 限制：DSL 文本 ≤ 64 KiB（含参数表的策略放宽到 1 MiB）；单条表达式 ≤ 1024 字符、≤ 256 个语法节点；`portfolio.leverage` ≤ 10。

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

- 不写 `market` 时按实例唯一绑定的因子任务的市场取默认值（现货任务就是 spot/spot）；绑定了两个任务必须写。`select` 为 spot 或 swap 时可省略 `prefer`。股票（A 股）不允许写 `market`。
- 同一个币的现货和永续是两个独立的目标，不互相抵消；空头一律在永续。
- 上期已持有正权重目标的币，本期沿用它所在的市场（避免永续偶尔缺数时来回倒仓），直到权重归零。
- 一个市场的因子本期被跳过，或失败（时序因子在该市场整体晚到、读取的标的全部失败；失败的标的占比超过 `portfolio.max_missing`，已持仓的币即使年龄没通过也计入）时，这个市场的币本期不可用：已持仓的保持不动（包括永续空头），新开仓用另一个市场。选币市场、或所有市场都这样不可用时整期按 `market_failed` 跳过（数据类原因，不是“积累中”，持续出现要看数据集新鲜度告警）。只涉及单个市场的策略仍按普通缺数处理。
- **两种输入方式**：同样的五种模式，因子数据可以是现货、永续各一个任务（`--factor-task spot=… --factor-task swap=…`），也可以是**统一数据集上的一个任务**（同一个数据集里放现货、永续两条序列，用 `series_tag` 区分；一个任务一个 View，`--factor-task multi=…`）。统一数据集上的任务必须显式写 `market`（没有可当默认值的市场），五种模式都能用，只绑这一个任务，不支持 `factor_params`、不能进策略池、不支持闭环回放（研究回放可以）；建实例时要把任务登记为 `multi`。两种方式的目标一致：现货、永续两条序列并存不算歧义，永续上失败的币现货照常，永续的因子被跳过时永续不可用、已持仓的永续保持不动。
- **规则级市场**：模式含两个市场（`both/*`、`spot/swap`）时，规则可写 `market: spot` 或 `market: swap`，这条规则只用该市场的因子值选择、目标也落在该市场，同一个币在两条规则里可以落在不同市场（不互相抵消）；不沿用上期持仓市场，缺数与年龄按这个市场判断，这个市场不可用时只冻结它在这个市场的持仓。写了它的策略必须显式写 `market`；单市场模式下只能写与策略相同的市场；`market: spot` 的规则不能做空。
- tranche 只能用在单市场模式（`spot/spot`、`swap/swap`）；纯现货的 tranche 不能做空且必须 `sizing.method: weight`；双市场模式（`both`、`spot/swap`）不支持 tranche。绑定两个任务的策略不支持 `factor_params`。
- 行为变化：现货权重之和超过 1、总敞口超过 `leverage` 时，现在按比例缩小并在摘要注明，不再整期跳过。

- **参考市场**：单市场策略可写 `market: {select: swap, reference: [spot]}`，额外绑定另一个市场的任务（`--factor-task spot=…`），只用于 `age_markets: all` 的年龄判定，不参与选择与下单。只能写模式不涉及的市场；必须有规则写 `age_markets: all`；不支持 `factor_params`、池成员不能写、统一数据集上的任务不能再写。

### sources：附加列来源

主帧仍是 `bar`，`sources` 让规则读更慢频率任务算好的列（例如小时帧读日线均线）：

```yaml
bar: 1h
sources:
  daily: {bar: 1d}                       # 至多两个；名字是 snake_case
rules:
  - {id: pick, type: rank, filter: "daily.close > daily.ma__20", score: "rank(daily.mom__20)", select: {top: 5}, weight: 1}
```

- 实例为每个来源按主帧的任务键各绑一个任务：`--factor-task daily:spot=…`（双市场主帧给 `daily:spot` 与 `daily:swap`，统一数据集写 `daily:multi`）。来源任务的周期要等于来源的 `bar`、是主帧周期的整数倍（A 股只能是 `1d`，主帧必须是日内周期），不能有可变成员。
- 每根主帧读来源里收盘时刻不晚于这根收盘时刻的最新一根（as-of）；读不到或缺了一期按缺数处理。收盘时刻对齐的那一根（如 00:00、A 股 15:00）等主帧与来源都到齐，其余时刻只等主帧；来源指纹变化在对齐的那一根触发 `factor_changed`。
- 每个来源至少要被一个表达式引用，否则保存即拒绝。
- 主帧很快、来源很慢（如 5m 配 1d）时，日线可能比对齐后的一根主帧晚就绪，那几根来源列缺数：写了 `allow_empty` 的规则会清仓再买回，这类规则请配 `on_missing: {keep_bars: N}`。
- A 股日线的时间键由数据层定下来（采集现在按 UTC 零点写日线键，策略要求上海零点）之前，不要在生产启用 A 股来源。
- 规则状态、持有计数、止损、冻结、缺数都只按主帧算。`bars[-1].daily.x`、`select.group.by` 写来源列、`factor_params` 读来源列都不支持；池与池成员不能写 `sources`；闭环回放不支持（研究回放可以）。

### strategy_pool：策略池

一个策略写 `rules` 或 `strategy_pool`，二选一。池由若干成员（引用已有策略）组成，按成员模拟净值分配资金：

```yaml
name: 双策略轮动
bar: 1h                                     # 必须写，成员的 bar 要一致
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

- 现货、永续成员必须显式写 `market`；股票策略不写 `market`，池绑定 `equity` 任务时按股票成员处理（股票与现货、永续成员不能混用，股票池只做多、`leverage ≤ 1`，停牌不清算）。成员不能是池、不能用 `factor_params`、`bar` 与池一致；池不写 `market`、`universe`、`factor_params`。
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
        - {when_gross_below: 1.0, min: 0, max: 0, adjust: raise_smaller}
    gross: {min: 1.0, max: 1.8}             # 总敞口区间；第一根不放大；max 不超过 portfolio.leverage
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
- 启用后先在后台预热（用历史推进成员净值），预热完成前不求值（事件稍后重投）也不能 `confirm`；停用再启用会重新预热。
- 成员净值、比例、择时与贡献写在每期结果的 `summary_json.pool`，页面在实例结果里显示。
- 研究回放支持策略池（闭环回放不支持）：`factor_tasks` 给成员市场的并集；回放发起时固化成员（从实例发起取会话的成员快照，否则取成员的当前定义），排队期间改成员定义不影响；资金费只按池 DSL 的 `fees.funding_column`，回放任务上不能写 `funding_column`。drift 写了 `start` 或 `initial.at` 时回放起点必须等于它，与同起点的预热逐期一致。每期记录的 `summary_json.decision.pool` 与 `decision.limits` 是池明细；`strategy_check.py targets --replay 名称=回放ID` 导出目标再用 `compare` 比对。
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
| `portfolio.limits` | `{long_max, short_max, overflow: {to}}`：单标的多头、空头上限（占权益，同一标的各市场同方向合计），在规则相加（池合成）之后、`max_weight` 之前执行；超出部分转给 `to` 指向的规则（池里是成员）本期同方向的其他标的，按它的权重比例分，最多三轮，剩下的留现金；不写 `overflow` 时留现金 |

## 2. 规则类型

每条规则必须有稳定的 `id`（`^[a-z][a-z0-9_]{0,63}$`）；**改 id 等于规则状态从零开始**。`name` 只用于展示。

| `type` | 用途 | 关键字段 |
| --- | --- | --- |
| `rank` | 截面选股 | `filter`、`score`、`select`、`weight`，可选 `holding`、`hold`、`filter_after`、`side`、`stop`、`scale`、`on_missing`、`market` |
| `signal` | 固定池择时 | `pool`、`entry`、`exit`、`weight`，可选 `stop`、`scale`、`on_missing`、`market` |
| `tranche` | 批次持仓：每根 K 线按本期选币新开一批，每批持有 h 期，多批叠加 | `tranche` 子策略模板、可选 `params` 参数表、`weight`，见第 4 节 |

含 `tranche` 规则的策略只能全部是 `tranche` 规则，且不能写 `factor_params`；只能用在单个市场的策略（`spot/spot` 按权重持有且不做空，或 `swap/swap`），双市场模式启用被拒（S1 暂不支持双市场 tranche：市场不可用时无法冻结持仓）。

### rank 规则

```yaml
- id: ma_above
  type: rank
  filter: "ma__5 > ma__10"                 # 布尔表达式，淘汰的标的记为 filtered
  score: "rank(ma__5 / ma__10 - 1)"        # 数值表达式；rank(x)、zscore(x) 在通过 filter 的样本上计算，可嵌套
  select: {top: 10000, buffer: 2}          # top 与 bottom 二选一；where 二次筛选；buffer 是排名缓冲区
  weight: {total: 1, method: equal, cap: 0.05}   # method: equal | rank；cap 是规则内单标的上限
```

- 保存时要求 `cap × 选中数量 ≥ total`（预算分得完；`top`/`bottom` 写比例时跳过这项检查，分不完的留现金）。`select.top` 写成大于标的数的值就等于全选。
- `select` 的扩展：`top: 0.1`（(0, 1) 是比例，分母是通过 filter 且分数有效的样本数）配 `count_round: floor | ceil | round`、`count_min`；`range: [10, 13]` 名次闭区间（与 top/bottom、buffer 互斥）；`group: {by: 列, max: 3}` 或 `{by: 列, quotas: [2, 1], rest: 1}` 分组名额（分组值 NaN 的不入选）；`then: {score: "表达式", top: 5}` 在第一段结果上二次排序（与 buffer、hold 互斥；`top`、`bottom` 也可以写 (0, 1) 的比例，分母是第一段选中且分数有效的个数，配 `count_round`、`count_min`）。
- `holding: {bars, offsets}` 分批换仓，与 `buffer` 互斥；`anchor: "<RFC3339 的 bar_start>"`（A 股可写交易日期）只改相位。日历换仓写 `holding: {calendar: trading_days | week | month, every: 5（仅 trading_days）, days: [1, -1], at: first | last}`，与 bars/offsets 互斥；建仓 bar 没有 ok 决策时在之后第一根 ok bar 补建。`filter_after` 只剔除不补位，剔除的权重留现金。
- `hold: {entry, exit, max_bars, cooldown_bars}` 进出场持有：持有中 exit 为真、不满足 filter、持满 max_bars、止损、缺数即出场（stop 优先），空位按名次从 entry 为真且 exit 不为真的候选里补，目标数取 `select` 换算的个数（持有多于目标不裁减）。持有标的只缺 score、entry 的列时不出场。与 buffer、holding.bars/offsets、then、range、filter_after 互斥；可与 `holding.calendar`（只能一个 days）同写，只在建仓 bar 判进出场。
- `scale: {expr, max}` 在规则配权（含 cap）之后逐标的乘系数，截到 [0, max]（max 默认 1、上限 10），乘掉的留现金；系数为 NaN 按缺数处理。`on_missing: {keep_bars: N}`（rank、signal；tranche 不支持）让持有标的本根缺数时冻结保持，连续缺数超过 N 根才移出。
- `side` 取 `long`（默认）或 `short`；空头只走永续，纯现货与股票不允许做空。
- `market: spot | swap` 是规则级市场（见第 1 节“market：市场模式”）。

### signal 规则

```yaml
- id: btc_trend
  type: signal
  pool: [BTC-USDT]                          # 固定列表或 {tags: [...]}
  entry: "bars[-1].bias <= 1 && bars[0].bias > 1"
  exit: "bars[0].bias < 1"
  weight: {total: 0.2}
```

先对旧理论持仓判 `exit`，再对候选判 `entry`，退出优先。

## 3. 表达式

用 Expr 编写，只能引用：

- 结果 View 的列（列名就是成员键）；
- `bars[0].<列>`（当期）、`bars[-1].<列>`（日历上紧邻的上一根）；`bars[...]` 之后只能用 `.列名`；
- `instrument_id`；仅 `select.where`、`filter_after` 里还能用 `score`；
- `params("成员键", "参数名")`：读取可变成员这一行实际用的参数；
- `来源名.列名`：写了 `sources` 时读附加来源的列（见上）。

禁用与注意：

- 内置函数一律禁用，只开放 `rank`、`zscore`（仅 `score` 中可用）。`rank` 同分取平均名次、单样本记 0.5；`zscore` 零方差记 0。
- `matches` 右边只能是字符串字面量的正则；`??`、范围 `..`、`in` 不可用。
- 三元表达式 `a ? b : c` 两个分支类型必须一致。
- 数字写法不超过 64 个字符、精度不超过 18 位小数、绝对值小于 10^9。
- 通过 filter 的候选全部分数无效时整期 `skipped(config_error)`，不当作“选不出标的”清仓。

## 4. tranche 规则（批次持仓）

```yaml
rules:
  - id: main
    type: tranche
    weight: {total: 1}                      # 规则预算：各子策略按 stg_weight 归一化后乘以它
    candidates: {require_volume: true}      # 规则级：候选是否要求 volume > 0，默认 true
    tranche:                                # 子策略模板；"{列名}" 取参数表这一行的值，整段占位符保留数字类型
      hold_period: "{h}"                    # 必填，1～2016
      hold_count: candidate_rows
      score: {method: rank_sum, missing: median}
      sizing: {method: quantity, scale: 0.5}
      long_factors: [{factor: "momentum__{n}", descending: true, weight: 1}]
      short_factors: []
      long_weight: 2
      short_weight: 0
      long_coin_num: 1                      # 整数是个数；0～1 之间是百分比
      short_coin_num: 0
      filter_before: []                     # 串联执行，符合条件的保留
      filter_after:                         # 在选中的样本上一次判断，任一条件成立就剔除，不补位
        - {direction: long, factor: "shadow__{m}", type: value, op: lte, value: "{p}"}
      stg_weight: "{w}"
      # stop: {loss: 0.08, cooldown_bars: 12}
    params:                                 # 可选：每行展开为一个子策略
      columns: [h, n, m, p, w]
      rows:
        - [1, 20, 24, 0.1, 0.0033333333333333335]
portfolio:
  leverage: 2                               # 按数量持有会让 Σw 略大于 1，留出上限；超过 leverage 时按比例缩小
```

| 选项 | 取值 | 默认 |
| --- | --- | --- |
| `hold_count` | `candidate_rows`：该标的本期是候选才计数；`bars`：每根 K 线都计数 | `bars` |
| `score.method` | `rank_sum`：各因子平均名次 × 权重 × 方向求和；`raw_sum`：因子原值加权求和 | `rank_sum` |
| `score.missing` | `median`：空值补同期名次中位数；`drop`：空值不参与本期排名 | `drop` |
| `sizing.method` | `quantity`：开仓时定数量；`weight`：每批份额固定 | `weight` |
| `sizing.scale` | (0, 10]，份额 = `scale × side_weight / h / 分母` | 1 |
| `long_weight`、`short_weight` | 0～20，某侧为 0 时该侧不选币 | 1、0 |
| `long_coin_num`、`short_coin_num` | 个数（分母固定）或 0～1 的比例（分母为实际选中数） | 1、0 |
| `stg_weight` | > 0 | 1 |
| 名单 | `long_white_list`、`long_black_list`、`short_white_list`、`short_black_list` | 空 |

- **参数表**：每行一个子策略，一条规则最多 5000 行、全策略合计最多 10000 个；占位符 `{列名}` 只能整段替换。
- **过滤条件**：`direction`（`long`/`short`）、`factor`、`type`（`value`/`rank`/`pct`）、`op`（`lt lte gt gte bt bte nbt nbte eq ne`，必填）、
  `value`（数字，或 `bt` 一类的 `[下限, 上限]`）、`rank_ascending`；逻辑组合写成 `{logic: "1&(2|3)", items: [...]}`。
- **要与外部框架逐值一致时**，把它的做法逐项显式写出，不要依赖默认值。
- **去重**：打分方式、缺值处理、多空因子、名单、前置过滤、个数、哪一侧权重大于 0、`stop.price` 都相同的子策略共用一次选币（“排名签名”）；
  后置过滤、定仓与止损逐个子策略计算。

## 5. 止损止盈

`rank`、`signal` 写在规则上，`tranche` 写在子策略模板里（可用参数表占位符）：

```yaml
stop:
  loss: 0.05            # 固定止损：相对入场价不利变动达到 5%
  trailing: 0.08        # 跟踪止损：相对此前最有利价格回撤 8%
  profit: 0.10          # 止盈：相对入场价有利变动达到 10%
  price: close          # close（默认）| extreme
  cooldown_bars: 12     # 触发后这么多根内本规则不能重新入场，默认 0
```

至少写一项；`loss`、`trailing` 取值 (0, 1)，`profit` 取值 (0, 10]。按收盘判断，不是盘中触价；View 必须有 `close`（`extreme` 时还要 `high`、`low`）。

## 6. 策略模板

模板是命名的 DSL 骨架 + 变量定义 + 可选的参数表样例行，生成的策略是普通策略定义，之后与模板脱钩。示例在 `modules/strategy/templates/tranche_example.yaml`。

- 只替换**值为完整 `"{{变量}}"`** 的 YAML 标量；键里、部分包含的占位符一律拒绝；`name` 是内置变量。
- 骨架带参数表时 `rows` 必须为空、`sample_row` 必填；字符串值与参数表单元格不能含花括号。
- 本地校验与渲染：

  ```bash
  moox-strategy-cli template check <模板.yaml>
  moox-strategy-cli template render <模板.yaml> --name demo [--var leverage=3] --params params.csv > demo.yaml
  ```

- 保存模板：`moox-cli strategy template save --template-file 模板.yaml`；`strategy template list|show|render|delete` 查看、渲染与删除（删除默认只预览，`--yes` 执行）。

## 7. 校验、创建与上线

写入生产的策略定义和实例属于外部写操作，每一步先告诉用户并等确认。

1. **本地先看语法**：模板用 `moox-strategy-cli template check`；普通 DSL 用 `moox-cli strategy validate`（只校验语法与结构，并在选定因子任务的结果 View 最近一期上试算一次，结果不落库；引用了不存在的列时报错带最接近的列名和该任务的全部列）。

   ```bash
   moox-cli strategy validate --dsl-file s.yaml --factor-task spot=<现货任务> [--factor-task swap=<永续任务>]
   ```

2. **创建定义**：`moox-cli strategy create --dsl-file s.yaml`；已有定义用 `strategy update --strategy-id <ID> --dsl-file s.yaml` 修改；删除用 `strategy delete`（默认只预览）。创建后用 `strategy show --strategy-id <ID>` 读回 DSL 核对。
3. **创建实例**：`strategy instance create --strategy-id <ID> --factor-task <市场>=<因子任务>`（可重复：市场模式含现货和永续时 `spot=…` 与 `swap=…` 两个都要给，或统一数据集上的一个任务 `multi=…`；股票策略 `equity=…`；默认停用；加 `--enable` 创建即启用）。市场必须是任务源数据集真实的市场。因子任务只能选本空间里运行中、还没被别的实例关联的任务，**创建后不能改**；已停用的实例可以用 `strategy instance delete --instance-id <实例>` 删除，绑定到它的因子任务和结果数据集一起删除、不可恢复（没绑定到它的公共任务保留）；停用期间可以用 `strategy instance update` 改绑策略定义与账户。
   不挂账户的是观察实例：只计算目标权重、不投递，适合先验证。
4. **启用**：`strategy instance enable --instance-id <ID>`（`disable` 停用；两者不带 `--yes` 只预览，确认后加 `--yes`）。启用时才解析列、成员契约和市场类型，列不存在或现货做空等问题在这一步被拒绝；
   任务刚建、结果 View 还没数据时可能因“还没有数据（覆盖范围未知）”被拒，等任务算出几个周期后再启用。
5. **看结果**：`strategy result list --instance-id <ID>`、`strategy target list --instance-id <ID>`（当前会话最新的 ok 目标）；控制台 `/strategy/detail/<实例ID>`。
   首次启用进入试运行（只发带影子标记的目标，Trade 不下单）；观察实例随后自动进入运行，挂账户的实例要用户执行 `confirm`。

### 修改已有策略

- 启用时定义会写入会话快照；已启用的实例改了定义，按文档的说法，会话与定义版本不一致时挂会话与置为启用都会被拒绝，所以改定义后要停用再启用（这一步没有在线上逐项验证，改之前先在观察实例上试）。
  **停用后重新启用会新建会话，规则状态（tranche 的开仓记录、持有计数、止损状态）从零开始**，改 `id` 同理。
- 引用的因子定义哈希变了（见 [factor-dsl.md](factor-dsl.md)“修改已有因子的定义”），引用它的实例同样要重新启用。
- 改动较大时，优先新建一份策略定义和观察实例并行跑一段时间，再切换。

## 8. 常见错误

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 保存即拒绝，提示未知字段 | 解析严格，拼错键名或写了不存在的字段 | 按本文字段名核对 |
| 池定义被拒，提示成员不存在或不能进池 | 成员没写 `market`、是池、用了 `factor_params`，或 `bar` 与池不一致 | 先修好成员策略再引用 |
| 提示 `cap × 选中数量 < total` | 预算分不完 | 调大 `cap` 或 `select.top`，或调小 `total` |
| 提示使用了 `max_age_bars` 必须写 `allow_empty: true` | 只保留新标的的规则经常没有候选，不写会让整期守门跳过 | 在这条规则上写 `allow_empty: true` |
| 提示互斥（hold 与 buffer / then / range / filter_after / holding.bars，range 与 top、buffer，then 与 buffer，calendar 与 bars） | 这些写法语义冲突 | 按提示二选一 |
| 启用被拒，提示 holding.anchor 不是 bar 起点 | 锚点要写某根 bar 的 bar_start（A 股日线是上海 0 点，可直接写交易日期） | 改成 bar 起点或交易日期 |
| tranche 规则写 `on_missing` 被拒 | tranche 不走冻结保持 | 去掉 `on_missing` |
| 规则写 `market` 被拒 | 策略没显式写 `market`；单市场策略写了不同的市场；`market: spot` 的规则做空；取值不是 spot、swap | 显式写策略的 `market`（双市场模式才能另选）；现货规则改成多头或改 `market: swap` |
| 启用被拒，提示列不存在 | 规则引用的列不在因子任务结果里 | 按成员键推导列名；默认参数的成员省略参数段 |
| 提示 `x 不是已声明的来源` 或不允许属性访问 | 表达式写了 `x.y`，但 `sources` 没有声明 `x` | 在 `sources` 里声明来源，或改回主帧的列名 |
| 启用被拒，提示来源要按主帧的任务键各绑定一个任务、或周期不是整数倍 | 来源任务少给了市场，或来源周期与主帧对不齐 | 按 `来源名:市场=任务` 补齐；换成周期是主帧整数倍的任务 |
| 含参数表的 DSL 被拒（超 64 KiB） | 参数表很大 | 含参数表的策略上限是 1 MiB，仍超出就拆成多条规则或多个策略 |
| tranche 策略启用被拒 | 混用了 `rank` 规则、`factor_params`，用在双市场模式，或纯现货里做空 / 按数量持有 | tranche 只能全部是 tranche 规则、只用单市场模式；纯现货只能 `sizing.method: weight` 且不做空 |
