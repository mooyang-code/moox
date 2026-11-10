# 策略 DSL 手册

这份手册按**功能**讲策略 DSL：每个功能说清楚“能做什么、怎么写、运行时到底发生什么”，并给出可以直接改的例子和逐步推演。

三份文档的分工：

| 文档 | 用途 |
| --- | --- |
| 本手册 | 按功能讲怎么写、会怎样；每个功能有例子和推演，适合用户和 Agent 从头读，也适合按功能地图（第 1 节）跳读 |
| `skills/moox/references/strategy-dsl.md` | 字段速查，加上创建、校验、启用的命令流程；Agent 写完策略后照它走上线步骤 |
| [策略模块](模块/策略.md) | 设计与完整语义：状态、持久化、失败语义、回放与实时的已知差别；遇到边角行为以它为准 |

## 0. 策略做什么

策略在每根 K 线收盘、因子算好之后运行一次：读因子结果里的列，在**截面**（这一根上所有标的）上过滤、打分、选择、配权，输出一份**目标权重**（每个标的占账户权益的比例，空头为负）。策略自己不下单，目标权重交给 Trade 换算成订单。

```
因子列 ─▶ 标的池与年龄 ─▶ filter ─▶ score ─▶ select ─▶ weight(+scale) ─▶ 规则状态(holding / hold / stop)
                                                                          │
         多条规则的权重相加 ◀──────────────────────────────────────────────┘
                 │
                 ▼
   portfolio 约束：limits ─▶ max_weight ─▶ leverage ─▶ 目标权重
```

**分工原则**：时序计算（均线、动量、波动、分位、滞后）一律写成因子，策略只在截面上用这些列做过滤、打分、配权。所以策略里没有函数库，只有 `rank`、`zscore` 两个截面函数；想要“k 根之前的排名”这类需求，在因子里写 `delay(x, k)`，策略直接引用那一列。

规则里引用的列名就是因子任务结果数据集的列名，由成员键推导：`因子ID[__参数段][__输出名]`，参数全为默认值时省略参数段，否则带参数段（例如窗口取 20 的成员 `ma` 的列是 `ma__20`）。创建实例选好因子任务后，页面和校验报错都会列出这个任务的全部列。

## 1. 功能地图

| 想做什么 | 怎么写 | 看哪一节 |
| --- | --- | --- |
| 每根都重新选前 N 个，等权持有 | `rank` 规则：`score` + `select.top` + `weight` | 4 |
| 只选流动性够的标的 | `filter` | 4.1 |
| 按几个因子的综合名次选 | `score: "0.6 * rank(a) + 0.4 * rank(b)"` | 4.2 |
| 选前 10%、后 10% | `select.top: 0.1`，配 `count_round`、`count_min` | 4.3 |
| 先粗选再细选 | `select.then` | 4.3 |
| 限制同一行业最多几只 | `select.group` | 4.3 |
| 减少换手：上期持有的名次略掉也继续拿 | `select.buffer` | 4.3 |
| 做空 | 规则写 `side: short`（只能在永续） | 4.5 |
| 分批换仓，平滑进出 | `holding: {bars, offsets}` | 5.1 |
| 按周、按月、按交易日换仓 | `holding.calendar` | 5.2 |
| 满足入场条件才买，触发出场条件才卖 | `hold: {entry, exit, max_bars, cooldown_bars, allow_reentry}` | 5.3 |
| 固定几个币按信号进出（如 BTC 趋势） | `signal` 规则 | 6 |
| 每根新开一批、各批持有固定期数 | `tranche` 规则 | 7 |
| 止损、跟踪止损、止盈 | `stop` | 8.1 |
| 按行情给仓位打折 | `scale` | 8.2 |
| 偶尔缺数时先冻结持仓，不立刻清掉 | `on_missing` | 8.3 |
| 规则经常没候选，不要让整期跳过 | `allow_empty` | 3.3 |
| 只做上市满 N 根的老币，或只做新币 | `universe.min_age_bars`、`max_age_bars` | 3.2、3.3 |
| 同一个币在现货和永续里选择下单市场 | `market: {select, prefer}` | 2 |
| 总杠杆上限、单币上限 | `portfolio.leverage`、`max_weight` | 9 |
| 单标的多头、空头分别封顶，超出的转给别的标的 | `portfolio.limits` | 9.3 |
| 多个策略按比例或按业绩分钱 | `strategy_pool`：`fixed`、`rotation`、`drift` | 10 |
| 控制多空净敞口、总敞口 | 池的 `exposure`、`gross` | 10.5 |
| 某个成员空仓时把它的比例转给另一个成员 | 池的 `allocation.switch` | 10.5 |
| 空头组合波动太大时暂停再平衡 | 池的 `freeze` | 10.7 |

## 2. 顶层结构与市场模式

```yaml
name: demo_strategy            # 策略名
bar: 1h                        # 可选断言：与 View 周期不一致时拒绝启用
market: {select: swap}         # 可选：市场模式；不写按绑定的因子任务的市场取默认值
universe: {min_age_bars: 24}   # 可选：策略级默认标的池
rules: [...]                   # 至少一条规则（或写 strategy_pool，二选一）
portfolio: {leverage: 1}       # 可选：组合级约束
factor_params: {...}           # 可选：可变成员的逐期调参（见策略模块文档“动态调参”）
sources: {daily: {bar: 1d}}    # 可选：附加列来源（见策略模块文档“附加列来源与参考市场”）
```

- 解析严格：重复键、未知字段、多文档一律拒绝，错误信息是中文，会指出出错的路径。
- 实例的周期由绑定 View 的频率决定，`bar` 只是断言。
- 限制：DSL 文本不超过 64 KiB（含参数表的策略放宽到 1 MiB）；单条表达式不超过 1024 个字符、256 个语法节点；`portfolio.leverage` 不超过 10。
- 保存定义时只做语法与结构校验；列是否存在、市场类型在**启用实例**时才解析。想提前发现列名写错，用带因子任务的校验（第 12 节）。

### 市场模式

```yaml
market:
  select: spot | swap | both   # 从哪个市场的币里选
  prefer: spot | swap          # 一个币在两个市场都可用时，优先在哪个市场下单
```

| select | prefer | 含义 | 要绑定的因子任务 |
| --- | --- | --- | --- |
| spot | spot | 纯现货 | 现货 1 个 |
| swap | swap | 纯永续 | 永续 1 个 |
| both | spot | 两边都选，现货优先 | 现货、永续各 1 个 |
| both | swap | 两边都选，永续优先 | 现货、永续各 1 个 |
| spot | swap | 现货里选币，有永续就用永续下单 | 现货、永续各 1 个 |

要点：

- **做空只能在永续**，所以纯现货策略不能写 `side: short`。
- 同一个币的现货和永续是两个独立的目标，权重不互相抵消。
- 上期已持有的币，本期沿用它所在的市场，直到权重归零；这样永续偶尔缺数时不会来回倒仓。
- 一个市场本期整体不可用（因子被跳过、整体晚到、失败的标的占比超过 `max_missing`）时：这个市场已持有的仓位保持不动，新开仓用另一个市场。
- 规则里写 `market: spot` 或 `market: swap`，这条规则只用该市场的因子值选择，目标也落在该市场；用于“现货做多一组、永续做空另一组”。写了它的策略必须显式写 `market`。
- 想要只用一个市场下单、另一个市场只用来判断上市年龄，写 `market: {select: swap, reference: [spot]}`，细节见策略模块文档。
- 不写 `market` 时，现货任务就是 spot/spot；绑定了两个任务必须写。股票策略不写 `market`。

## 3. 标的池与年龄

### 3.1 universe：谁有资格参与

```yaml
universe:
  tags: [majors]            # 标签白名单
  include: [BTC-USDT]       # 标的白名单；与 tags 取并集
  exclude_tags: [stable]    # 排除的标签
  exclude: [LUNA-USDT]      # 排除的标的
  min_age_bars: 24          # 上市满这么多根才参与
```

- `tags`、`include` 任一非空时取并集作为白名单；都为空表示全部；然后再减去 `exclude_tags` 和 `exclude`。标的与标签 ID 区分大小写。
- 标签与名单只作用于没有显式 `pool` 的规则；`min_age_bars` 对所有规则生效，但不会剔除规则上期已持有的标的。
- 规则里也可以写自己的 `universe`，写了的字段逐项覆盖策略级，没写的沿用策略级。

### 3.2 年龄：只做老币

`min_age_bars: N` 表示这个标的在 N 根之前已经有数据才参与（`min_age_bars: 1` 就是“已有数据”，N 越大越要求老币）。新币上市初期数据短、因子不稳，这是最常用的保护。

```yaml
universe: {min_age_bars: 720}     # 1h 周期：上市满 30 天才参与
```

年龄只在**建仓时**判断，已经持有的标的不会因为年龄被踢出。想做多空对冲时，现货和永续两个市场的上市时间可能不同，用 `age_markets` 说清楚怎么算：

| `age_markets` | `min_age_bars` 的口径 |
| --- | --- |
| `each`（默认） | 任一市场通过就算通过 |
| `all` | 标的必须在绑定的**每个**市场都有数据，年龄按最晚上线的那个市场算；`min_age_bars: 1` 配 `all` 就是“各市场都已上线” |

### 3.3 年龄上限与 allow_empty：只做新币

`max_age_bars: N` 只保留年龄小于 N 根的新标的。这类规则经常整根没有候选，如果每次都触发“可用标的太少”的整期跳过，其他规则也会被拖累，所以**写了 `max_age_bars` 的规则必须同时写 `allow_empty: true`**。

`allow_empty: true` 的规则不参与整期守门（`universe_too_small`、`min_universe`、`max_missing`）：候选为空或缺数时这条规则本期不出目标，预算留现金，其他规则照常运行。

```yaml
rules:
  - id: fresh_listings
    type: rank
    allow_empty: true
    universe: {min_age_bars: 24, max_age_bars: 720}   # 上市 1 天到 30 天的新币
    score: "rank(mom__24)"
    select: {top: 3}
    weight: {total: 0.2}
```

`min_age_bars` 必须小于 `max_age_bars`。

## 4. rank 规则：截面选股

```yaml
- id: momentum_long             # 稳定标识，改 id 等于规则状态从零开始
  name: 动量多头                  # 只用于展示
  type: rank
  filter: "volume_mean > 20000"          # 布尔表达式：淘汰不合格的标的
  score: "rank(mom__20)"                 # 数值表达式：分数越高越靠前
  select: {top: 10, buffer: 2}           # 取前 10 名，上期持有的在前 12 名内继续拿
  weight: {total: 0.8, method: equal, cap: 0.1}   # 规则预算 0.8，等权，单标的最多 0.1
```

每根的处理顺序：**标的池 → filter → score → select → weight → scale**。每一步淘汰的标的都会在“解释明细”里留下原因，页面和回放结果里都能看到。

### 4.1 filter

布尔表达式，只保留结果为真的标的，被淘汰的明细阶段记为 `filtered`。filter 是**资格**：选股用，也是 `hold` 规则里继续持有的条件（持有中的标的不再满足 filter 就出场）。

```yaml
filter: "quote_volume_mean > 20000 && close > 0"
```

表达式里可以写列名、`bars[0].列`（当期）、`bars[-1].列`（上一根）、`instrument_id`，以及 `matches` 正则（右边必须是字符串字面量）。

### 4.2 score

数值表达式，分数越高名次越靠前。表达式里可以用两个截面函数，它们都在**通过 filter 的样本**上计算：

- `rank(x)`：名次（同分取平均名次，单样本记 0.5）；
- `zscore(x)`：标准化（零方差记 0）。

```yaml
score: "0.6 * rank(bias_q) + 0.4 * rank(quote_volume_mean_q)"   # 两个因子的综合名次
score: "zscore(mom__20) - 0.5 * zscore(vol__24)"                # 动量减去波动，各自先标准化
score: "-vol_ratio"                                              # 取负号即“越小越好”
```

要点：

- 截面函数可以嵌套；同一层的函数在同一个样本上计算一次，结果与书写顺序无关。
- 任一参数无效（NaN、Inf、运行错误）的标的会从这一层的名次里剔除；外层结果无效（例如除零）只淘汰它自己。
- 通过 filter 的候选**全部**分数无效时，整期 `skipped(config_error)`，不会当作“选不出标的”清仓，这样列写错了不会悄悄把仓位清空。

### 4.3 select：取哪些

| 写法 | 含义 |
| --- | --- |
| `top: 10` | 分数最高的 10 个；写成大于标的数的值等于全选 |
| `bottom: 10` | 分数最低的 10 个 |
| `top: 0.1` | (0, 1) 是比例：取通过 filter 且分数有效的样本数的 10% |
| `range: [10, 13]` | 名次闭区间（分数从高到低），与 top、bottom、buffer 互斥 |

`top` 与 `bottom` 二选一（或只写 `range`）；其他修饰如下。

**比例选币：`count_round`、`count_min`**

```yaml
select: {top: 0.1, count_round: ceil, count_min: 1}
```

- 比例换算个数用十进制精确计算（`0.1 × 30` 恰好是 3）；`count_round` 取 `floor`（默认）、`ceil`、`round`（0.5 进位）；`count_min` 是换算后的最少个数（默认 0）。这两项只能配比例写法。
- 写比例时选中个数要到运行时才知道，保存时跳过“`cap × 选中数量 ≥ total`”的检查，分不完的预算留现金。

**分组名额：`group`**

```yaml
select: {top: 10, group: {by: industry, max: 3}}                   # 每个行业最多 3 个
select: {top: 6, group: {by: industry, quotas: [2, 1, 1], rest: 1}} # 按组内最优名次给行业排序，前三个行业名额 2、1、1，其余行业各 1
```

按名次从前往后逐个接受，所属组已满就跳过（明细 `group_full`）。`by` 必须是 View 里的数值列；分组值为空的标的按缺数处理。`max` 与 `quotas` 二选一，`rest` 只能配 `quotas`。

**二次排序：`then`**

先粗选再细选：第一段按 `score` 选出一个集合，第二段在这个集合里按新分数再取。

```yaml
score: "-vol_ratio"                     # 第一段：波动最小
select:
  top: 0.1                              # 取最小的 10%
  then: {score: "rank(mom__20)", top: 0.5}   # 在其中再取动量最强的一半
```

- `then.score` 可以用 `rank`、`zscore`，在第一段选中的集合上计算；第一段选中、二次排序没取到的标的，明细记 `select.then`。
- `then.top`、`then.bottom` 与第一段同口径：整数是个数，(0, 1) 是比例，可配 `count_round`、`count_min`。比例的分母是**第一段选中且二次排序分数有效的个数**——第一段选出 7 个、`then: {top: 0.5}` 取 3 个。
- 二次排序的分数全部无效时，这条规则本期不出目标（预算留现金），不整期跳过。
- `then` 与 `buffer`、`hold` 互斥。

**二次筛选：`where`**

`where` 是用 `score` 再筛一次的布尔表达式（只有这里和 `filter_after` 能引用 `score`）。

**换手缓冲：`buffer`**

```yaml
select: {top: 10, buffer: 2}
```

上期持有的标的，只要本期名次仍在前 N + buffer 名内就继续持有；空位再按名次补新标的。名次按 `where` 之前的全样本名次判断，与解释明细的名次一致。`buffer` 与 `holding`、`hold`、`then`、`range` 互斥。

### 4.4 weight：配多少

```yaml
weight: {total: 0.8, method: equal, cap: 0.1}
```

- `total`：这条规则的预算（占权益的比例）。各规则的 `total` 之和不能超过 `portfolio.leverage`。
- `method: equal`（默认）平分；`method: rank` 按名次线性配权（最优 N 份、最差 1 份）。
- `cap`：规则内单标的上限。超过 `cap` 的权重被裁到 `cap`，**多出来的部分在规则内按原权重比例分给还没封顶的标的**，迭代到没有新的超限，仍分不出去的留现金。
- 保存时要求 `cap × 选中数量 ≥ total`（预算分得完）。

规则内的 `cap` 和组合级的 `max_weight`、`limits` 是三层不同的封顶，区别见第 9 节。

### 4.5 side：做多与做空

```yaml
- id: reversal_short
  type: rank
  side: short              # long（默认）或 short
  score: "rank(mom__5)"    # 分数最高的被做空
  select: {top: 10}
  weight: {total: 0.5}
```

- `side: short` 时，被选中的标的权重取负；选股逻辑不变：分数最高的 N 个就是被做空的 N 个，想做空“最弱的”就把分数取负或用 `bottom`。
- 空头只走永续；纯现货与股票策略不能做空：显式写了 `market` 的策略在保存时就拒绝，没写 `market` 的要到启用（或带任务的校验）时才拒绝。
- 多空对冲的写法是两条规则：一条 `side: long`，一条 `side: short`，各自配预算。各规则 `weight.total` 之和（空头也按绝对值计）不能超过 `portfolio.leverage`，例如多空各 0.5 要 `leverage: 1`。

### 4.6 filter_after

`filter_after` 是在选定之后再剔除的条件（可以用 `score`），**只剔除不补位**，剔除的权重留现金。常用来表达“选出来之后遇到某个极端状态就先不碰”，而不想让名次往后递补。

## 5. 持仓节奏：多久换一次、怎么进出

rank 规则默认**每根都按本期名次重新选**，名次一变仓位就变。要降低换手、平滑进出，有四种互相排斥的办法，先对照选型：

| 办法 | 写法 | 行为 | 适合 |
| --- | --- | --- | --- |
| 每根重选（默认） | 什么都不写 | 每根按本期名次重建 | 信号稳定、换手不敏感 |
| 名次缓冲 | `select.buffer: B` | 上期持有的在前 N+B 名内继续拿 | 只想少换一点 |
| 分批换仓 | `holding: {bars, offsets}` | 预算拆成几个批次，每个批次每隔 `bars` 根重选一次 | 想把“选股时点”平均开，避免踩单点 |
| 日历换仓 | `holding: {calendar, …}` | 按交易日、周、月的建仓日重选 | 周度、月度调仓 |
| 进出场持有 | `hold: {entry, exit, max_bars, …}` | 满足 `entry` 才入场，持有中 `exit` 为真才出场，空位按名次补 | 入场与出场用不同条件 |

互斥关系：`buffer` 与 `holding`、`hold`、`then`、`range` 互斥；`holding.bars/offsets` 与 `holding.calendar` 互斥；`hold` 与 `holding.bars/offsets`、`then`、`range`、`filter_after` 互斥，但可以与 `holding.calendar`（只能写一个 `days`）同写。

### 5.1 holding：分批换仓

```yaml
holding: {bars: 24, offsets: [0, 12]}      # 24 根为一个周期，两个批次，起点相差 12 根
```

- 规则预算在批次之间均分。每个批次**只在自己的 offset 上重选**，选出的标的持有 `bars` 根，之后到下一个 offset 才换。
- 批次相位按 bar 序号对 `bars` 取模：加密的 bar 序号是 `bar_end` 自 Unix 纪元起的根数，所以 1h、`bars: 24` 时，`offset 0` 就是 `bar_end` 落在 UTC 零点的那一根。
- 想改相位不改周期，加 `anchor: "2026-01-02T08:00:00Z"`（写某根 bar 的 `bar_start`），只改相位，不改批次数。
- 首次求值成功的那一根，所有批次一起按本期名次建好，不会空仓等一个周期。
- 延续期间只因**缺数**或**止损**把标的移出批次；本期被 filter 淘汰的标的仍按批次持有，直到该批次下次重选。缺数和 `filter_after` 剔除的份额留现金，不重新分配。
- 与 `buffer` 互斥。

推演：`top: 2`、`weight.total: 1`、`offsets: [0, 12]`，已稳定运行。批次 A 最近一次在第 0 根重选，选出 X、Y；批次 B 最近一次在第 12 根重选，选出 Y、Z。第 13 根的目标：

| 标的 | 批次 A 的份额 | 批次 B 的份额 | 合计 |
| --- | --- | --- | --- |
| X | 0.25 | — | 0.25 |
| Y | 0.25 | 0.25 | 0.50 |
| Z | — | 0.25 | 0.25 |

到第 24 根，批次 A 重选，X、Y 换成当时的前两名，批次 B 不动。

### 5.2 holding.calendar：按日历换仓

```yaml
holding:
  calendar: trading_days     # trading_days | week | month
  every: 5                   # 仅 trading_days：每 5 个交易日一个周期（必填）
  days: [1, 3]               # week、month：每周（月）的第几个交易日，1 起，-1 为最后一个；trading_days：周期内第几天
  at: last                   # 日内帧在建仓日的 first（第一根）或 last（最后一根，默认）建仓
```

- `days` 的每个值是一个批次，预算按批次数均分；周（月）的交易日不够指定序号时，这一期该批次不重建。
- 加密按 UTC 自然日（每天都是交易日，周从周一、月从 1 日开始）；A 股按内嵌交易日历。
- 建仓那一根没有结果（被整期跳过、停机漏掉）时，在之后第一根有结果的 bar 补建；到下一个建仓日仍没补上，就按下一个建仓日正常重建。
- 例子：`{calendar: week, days: [1]}` 每周一重选；`{calendar: month, days: [1, -1]}` 月初、月末各一个批次，各占一半预算。

### 5.3 hold：进出场持有

`hold` 把“怎么进、怎么出”分开写：

```yaml
- id: quiet_long
  type: rank
  filter: "volume_mean > 0"        # 资格：入场和继续持有都要满足
  score: "-vol_ratio"              # 补位时的排序
  select: {top: 10}                # 目标持有个数
  hold:
    entry: "vol_pct < 0.1"         # 新入场还要满足；不写表示不限制
    exit: "vol_pct > 0.7"          # 持有中满足即出场
    max_bars: 720                  # 持有满这么多根强制出场；0 表示不限
    cooldown_bars: 0               # 出场后这么多根内不能再入场
    # allow_reentry: true          # 见下
  weight: {total: 1}
```

**每根的处理顺序**

1. 持有中的标的依次判出场，先中者生效：不在预期集合（`not_expected`）→ 止损 → 缺数（`exit_missing`）→ 不满足 `filter`（`exit_filter`）→ `exit` 为真（`exit_condition`）→ 持满 `max_bars`（`exit_max_bars`）。
2. 空位 = 目标个数 − 留下的持有数（不为负；持有多于目标时**不裁减**）。
3. 候选要同时满足：通过 `where`；`entry` 为真；`exit` 不为真；不在持有中；本根没有出场；不在冷却中。按名次（受 `group` 名额约束）依次补满空位。
4. 对持有集合按 `weight` 配权；持有数少于目标时，预算按实际持有数分满。

**逐根推演**（`top: 1`，`score: m`，`entry: "p < 0.1"`，`exit: "p > 0.7"`，`max_bars: 3`，`cooldown_bars: 0`；两个标的 A、B，括号里是 (m, p)）

| 根 | A | B | 持有 | 发生了什么 |
| --- | --- | --- | --- | --- |
| 1 | (3, 0.05) | (2, 0.05) | A | 两个都满足 entry，A 分数高，A 入场 |
| 2 | (3, 0.4) | (2, 0.05) | A | A 的 exit 不成立，继续持有 |
| 3 | (3, 0.8) | (2, 0.05) | B | A 的 p > 0.7，`exit_condition` 出场；空位由 B 补上 |
| 4 | (1, 0.3) | (2, 0.05) | B | B 继续持有；A 的 p = 0.3，不满足 entry |
| 5 | (1, 0.3) | (2, 0.05) | B | 继续持有 |
| 6 | (1, 0.3) | (2, 0.05) | 空仓 | B 在第 3、4、5 根持满 3 根，`exit_max_bars` 出场，**本根不能补回**；A 不满足 entry，没有候选，预算留现金 |
| 7 | (3, 0.05) | (2, 0.5) | A | A 满足 entry，入场 |

要点：

- 持有标的**只缺 `score`、`entry` 的列**时不出场，只是不能参与补位；缺 `filter`、`exit`、`scale` 或 `group.by` 引用的列才按缺数出场。
- 持有根数、冷却都按 bar 序号的差计算，跳过、过期、追赶的根也算时间流逝。
- `entry` 与 `exit` 在同一根都为真时不入场（明细 `hold_not_entered`）。
- 停用再启用会新建会话，持有记录、冷却从零开始。

**`allow_reentry`：让出场条件只管持有中的标的**

默认情况下，`exit` 为真的标的不能入场，刚出场的标的当根也不能补回。这在补位排名用**滞后分数**时会有问题：刚触发出场的标的排名往往还在前面，补位时又排在第一，结果每次出场后只能空一根或换成较差的标的。

`allow_reentry: true` 把“出场条件”只当作持有中的退出条件：候选只需 `entry` 为真，出场条件为真的标的也可以入场，本根刚出场的标的也可以立刻补回（按新入场计，持有根数重新数、不记冷却）。要求 `cooldown_bars` 为 0，写了大于 0 的冷却时保存即拒绝。

对照推演（`top: 1`，`entry: "m > 0"`，`exit: "p > 0.7"`；A 的 m = 3、B 的 m = 2，B 的 p 一直是 0.3）：

| 根 | A 的 p | 默认 | `allow_reentry: true` |
| --- | --- | --- | --- |
| 1 | 0.3 | A 入场 | A 入场 |
| 2 | 0.8 | A 出场，不能补回，B 入场 | A 出场，立即按新入场补回，仍是 A |
| 3 | 0.3 | B 继续持有 | A 继续持有（持有根数在第 2 根重新数起） |

**与日历同写**：`hold` 可以与 `holding.calendar`（只能一个 `days`）同写，这时 `filter`、`exit`、`max_bars` 与补位只在建仓日判断，其余 bar 保持上一次的持有集合；止损、缺数、冻结每根都判。

## 6. signal 规则：固定币择时

不选股，只对固定的几个标的判断进出：

```yaml
- id: btc_trend
  type: signal
  pool: [BTC-USDT]                          # 固定列表或 {tags: [...]}
  entry: "bars[-1].bias <= 1 && bars[0].bias > 1"    # 上一根不大于 1、这一根大于 1：上穿
  exit: "bars[0].bias < 1"                  # 跌破
  weight: {total: 0.2}
```

- 先对旧持仓判 `exit`，再对候选判 `entry`，**退出优先**。
- 本期持有的标的平分规则预算：`total: 0.2`、池里有 2 个同时持有，每个 0.1。
- 持有标的的 `exit` 输入缺失按退出处理（写了 `on_missing` 才冻结保持，见 8.3）。
- 判断“穿越”要用 `bars[-1]` 取上一根的值；第一期没有上一根，`bars[-1]` 的值都缺失。
- 可以配 `stop`、`scale`、`on_missing`、`market`。

## 7. tranche 规则：批次持仓

每根 K 线按本期选币**新开一批**仓位，每一批持有 h 期，多批叠加。打分、选币、过滤、定仓方式都是选项，同类策略写配置即可。适合“每根都开仓、各批固定持有 h 期”的日内或短周期风格。

```yaml
rules:
  - id: main
    type: tranche
    weight: {total: 1}
    tranche:
      hold_period: 6                  # 每批持有 6 期
      score: {method: rank_sum}
      long_factors: [{factor: "momentum__20", descending: true, weight: 1}]
      long_weight: 1
      long_coin_num: 1                # 每批选 1 个
portfolio: {leverage: 1}
```

- 一条策略里只能全是 `tranche` 规则，不能写 `factor_params`，只能用于单个市场（`spot/spot` 按权重持有且不做空，或 `swap/swap`）。
- 想让各批的子策略逐行变化（不同周期、不同因子参数），写 `params` 参数表，每行展开一个子策略，一条规则最多 5000 行。
- 全部选项（打分方式、缺值处理、前后过滤、定仓方式、名单）见 `skills/moox/references/strategy-dsl.md` 第 4 节，设计细节见 [策略模块](模块/策略.md)“tranche 规则”。

## 8. 止损、仓位系数、缺数处理

### 8.1 stop：止损止盈

`rank`、`signal` 写在规则上，`tranche` 写在子策略模板里：

```yaml
stop:
  loss: 0.05            # 固定止损：相对入场价不利变动达到 5%
  trailing: 0.08        # 跟踪止损：相对此前最有利价格回撤 8%
  profit: 0.10          # 止盈：相对入场价有利变动达到 10%
  price: close          # close（默认）| extreme
  cooldown_bars: 12     # 触发后这么多根内本规则不能重新入场，默认 0
```

- 至少写一项；`loss`、`trailing` 取值 (0, 1)，`profit` 取值 (0, 10]。
- **按收盘判断，不是盘中触价**；View 必须有 `close`（`price: extreme` 时还要 `high`、`low`）。
- 止损与 `exit` 同一根成立时按止损记，冷却按 `stop.cooldown_bars`。

### 8.2 scale：按行情给仓位打折

```yaml
scale: {expr: "timing_coef", max: 1}    # 数值表达式（逐行，不能用截面函数），结果截到 [0, max]
```

- 系数只乘在规则的输出权重上（rank 在规则内配权与 `cap` 之后），**乘掉的部分留现金**，不重新分配。
- `max` 默认 1、上限 10；允许大于 1 用来加仓。
- 系数为 NaN、无穷或求值出错的标的按缺数处理；想“缺值当 0”，在因子里用 `fill_nan`。

### 8.3 on_missing：偶尔缺数先冻结

```yaml
on_missing: {keep_bars: 3}     # 持有标的连续缺数不超过 3 根时冻结保持，超过后移出；1～1000
```

默认情况下持有中的标的某一根缺数就按缺数口径处理（rank 规则移出，signal 规则清仓）。写了 `on_missing` 后，缺数的持有标的冻结：保持上期目标、不交易，持有根数照常累计；数据恢复即清零，连续缺数超过 `keep_bars` 根才移出。`keep_bars` 数的是“连续多少次求值都缺数”。tranche 规则不支持。

### 8.4 缺数、冻结与整期跳过

求值时每个标的可能缺数（列为空、因子失败、没有行）。策略对缺数分三层处理：

| 层次 | 做法 |
| --- | --- |
| 单个标的缺数 | 没有持有：这根不入选。持有中：rank 规则移出，signal 规则清仓；写了 `on_missing` 则冻结 |
| 持仓冻结 | 市场本期整体不可用，或只缺上一根（`bars[-1]`）而当期输入齐全：持有的标的**不判出场、不调仓**，保持上期目标权重，明细阶段 `held` |
| 整期跳过 | 缺数的标的占比超过 `portfolio.max_missing`（默认 0.2），或可用标的少于 `portfolio.min_universe`：这一根不产出新目标，已有目标继续有效 |

常见的整期跳过原因（`skipped(原因)`，页面“结果”里可见）：

| 原因 | 含义 | 怎么办 |
| --- | --- | --- |
| `no_data` | 这一根没有任何行 | 等数据；持续出现看数据集新鲜度告警 |
| `universe_too_small` | 通过标的池与年龄后可用标的太少 | 放宽 `min_age_bars`，或给规则写 `allow_empty` |
| `too_many_missing` | 缺数占比超过 `max_missing` | 查因子是否失败；必要时放宽 `max_missing` |
| `config_error` | 列被删、View 被改、候选的分数全部无效等配置问题 | 实例标 `degraded`，按提示改完重新启用 |
| `ambiguous_series` | 同一标的同一根出现多个序列 | 查数据集，同一标的只保留一个序列 |
| `history_insufficient` | View 的历史不够判断年龄 | 等数据积累，或降低 `min_age_bars` |
| `factor_changed` | 绑定的因子定义变了 | 重新启用实例 |
| `market_failed` | 选币的市场本期整体不可用 | 数据类原因，查因子与采集 |
| `market_missing` | 双任务策略只来了一个 View 的事件 | 等另一个 View，持续出现查因子任务 |
| `catchup`、`expired` | 事件晚到：追赶补算或已过有效期，只推进规则状态、不出目标 | 正常现象，持续出现查链路延迟 |

`degraded` 的实例在下一个正常周期自动恢复。每种原因的完整语义见 [策略模块](模块/策略.md)“失败语义”。

## 9. portfolio：组合级约束

```yaml
portfolio:
  leverage: 1.5        # 总敞口上限，默认 1
  max_weight: 0.1      # 单币上限（现货、永续合计）
  min_universe: 20     # rank 规则可用标的少于它则整期跳过
  max_missing: 0.2     # 缺数占比超过它则整期跳过，默认 0.2
  limits: {long_max: 0.08, short_max: 0.05, overflow: {to: momentum_long}}
```

### 9.1 字段

| 字段 | 说明 |
| --- | --- |
| `leverage` | 总敞口（多头加空头的绝对值）上限，默认 1，不超过 10；纯现货与股票不允许大于 1，只有市场模式含永续才可以；各规则 `weight.total` 之和不超过它。超过时整体按比例缩小，不放大 |
| `max_weight` | 单币上限，同一个币的现货与永续**合计**（绝对值之和，“现货多 + 永续空”的对冲也按绝对值之和截断）；两边都不为零时按比例缩小，只有一边时取上限；超出部分留现金 |
| `min_universe` | rank 规则进入 `filter` 的可用标的少于它则整期跳过 |
| `max_missing` | 任一规则缺数占比超过它则整期跳过；显式写 0 表示不容忍任何缺数 |
| `limits` | 单标的多头、空头分别封顶，超出部分转给指定来源的其他标的，见 9.3 |

### 9.2 执行顺序

多条规则各自配权后相加（同一个标的的权重相加）→ **`limits`** → **`max_weight`** → 现货预算 → **`leverage`**。冻结的标的先按上期目标预留预算，其余标的在剩余预算内缩小。

现货预算这一步只在 `leverage` 大于 1 且市场模式含现货时才单独执行：现货部分的总权重不能超过 1（减去冻结保持的现货目标），超过时现货目标整体按比例缩小，永续不受影响；`leverage` 不大于 1 时，这一步已经被总敞口约束包含。

### 9.3 limits：单标的上限与超限转移

`limits` 解决的问题：多条规则（或策略池的多个成员）可能同时重仓同一个标的，合起来超过了你能接受的单标的暴露。它**按方向分别封顶**，并且可以把超出的部分**转给别处**，而不是单纯留现金。

```yaml
portfolio:
  leverage: 1
  limits:
    long_max: 0.3                    # 单标的多头上限（占权益的比例，不随杠杆换算）
    short_max: 0.05                  # 单标的空头上限
    overflow: {to: fill}             # 超出部分转给哪个来源；不写则留现金
```

规则：

- 一个标的的**多头敞口**是它各市场正权重之和，**空头敞口**是各市场负权重绝对值之和（同一个币的现货与永续同方向合计判上限）。
- 超出 `long_max` 或 `short_max` 的标的，先把它的权重**按比例削到上限**（现货、永续各削同一比例）。
- `overflow.to` 指向一个来源：普通策略写**规则 id**，策略池写**成员 id**。削掉的量按方向汇总，转给这个来源本期**同方向**的其他标的，**按该来源各标的的权重比例分**。
- 去向不包括：已经被削到上限的标的、冻结的标的，以及该来源本期没有同方向持仓的情形（这时全部留现金）。池里本期比例为 0 的成员没有可分的标的，不能当去向。
- 分配之后又超限的部分再分一轮，**最多三轮**，之后仍分不出去的留现金。
- `long_max`、`short_max` 至少写一个，取值大于 0 且不超过 `portfolio.leverage`；`overflow.to` 必须是存在的规则或成员 id，保存时校验。
- 每次削减都记在结果摘要的 `limits` 里：标的、方向、超出量、第几轮、去向（空表示留现金）。

**推演 1：单轮转移**。两条规则：`big`（`top: 1`，`total: 0.6`）与 `fill`（`top: 3`，`total: 0.3`）。三个标的 X、Y、Z，X 分数最高。

| | X | Y | Z |
| --- | --- | --- | --- |
| `big` 的权重 | 0.6 | — | — |
| `fill` 的权重 | 0.1 | 0.1 | 0.1 |
| 相加 | **0.7** | 0.1 | 0.1 |

设 `long_max: 0.3`、`overflow: {to: fill}`：X 超出 0.4，削到 0.3；转给 `fill` 持有的、没封顶的 Y、Z，按它们在 `fill` 里的权重（0.1 : 0.1）平分，各 +0.2。结果 X = Y = Z = 0.3，现金 0.1。摘要：`{X, long, excess 0.4, round 1, to fill}`。

**推演 2：多轮后留现金**。同样的输入，`long_max: 0.25`。

- 第 1 轮：X 超出 0.45，削到 0.25；Y、Z 各 +0.225，变成 0.325。
- 第 2 轮：Y、Z 都超限（各超 0.075）；`fill` 的标的里 X 早已封顶，Y、Z 也刚被削到上限，没有可去的地方，各削到 0.25，**多出来的 0.15 留现金**。

结果 X = Y = Z = 0.25，现金 0.25（规则预算本来就只分了 0.9，再加上这 0.15）。摘要里有三条：X 第 1 轮（去向 `fill`）、Y 与 Z 第 2 轮（没有去向）。

**推演 3：三轮级联**。X 0.9，来源 `t` 里有 A 0.28、B 0.02，`long_max: 0.3`。

- 第 1 轮：X 超出 0.6，按 A : B = 0.28 : 0.02 分，A 变成 0.84，B 变成 0.06。
- 第 2 轮：A 超出 0.54，去向只剩 B（A 已封顶），B 变成 0.6。
- 第 3 轮：B 超出 0.3，已经没有去向，留现金。

结果三个标的都是 0.3。

**推演 4：现货与永续合计**。X 的现货 0.3、永续 0.3，`long_max: 0.4`，不写 `overflow`：合计 0.6，超出 0.2，现货与永续各按比例削到 0.2，超出部分留现金。

**在策略池里**：来源是成员，“该来源的各标的权重”是**成员本期的合成比例乘以成员目标**。比例为 0 的成员分组权重为 0，不能当去向。

### 9.4 四层封顶对比

| | 作用范围 | 超出的部分 |
| --- | --- | --- |
| 规则内 `weight.cap` | 一条规则内的单标的 | 规则内按原权重比例分给没封顶的标的，迭代到没有新的超限，仍分不出去留现金 |
| `limits` | 整个组合，同一标的各市场同方向合计，多空分别设 | 转给 `overflow.to` 的同方向其他标的，最多三轮，已封顶的不接收，剩下留现金 |
| `max_weight` | 整个组合，同一标的现货与永续绝对值合计 | 直接留现金，不转移 |
| `leverage` | 总敞口 | 整体按比例缩小，不放大 |

`limits` 与规则内的 `cap` 思路一致：**封顶是严格的**，分出去的部分只给还没封顶的标的。

## 10. strategy_pool：策略池

策略池把**多个已有策略**当成员，按成员的模拟净值分配资金，合成一份目标权重。适合：

- 把几个互补的策略（一个做多、一个做空）装进同一个账户，固定比例分钱；
- 让业绩好的策略多拿资金（轮动）；
- 让多空比例随各自盈亏自然漂移、再按规则拉回（漂移），并控制多空净敞口与总敞口。

一个策略要么写 `rules`，要么写 `strategy_pool`，二选一。

```yaml
name: two_strategies
bar: 1h                                      # 必须写；成员写了 bar 的要与它一致
strategy_pool:
  members:
    - {id: core, strategy: strategy_aaa}     # id 是 snake_case；strategy 是已有策略的 ID
    - {id: hedge, strategy: strategy_bbb}
  allocation:
    method: fixed
    ratios: {core: 0.7, hedge: 0.3}
portfolio: {leverage: 1}
```

**成员约束**：成员最多 20 个，是已有的策略定义；不能是池（不嵌套），不能写 `factor_params`、`sources`、`market.reference`；现货、永续成员必须显式写 `market`（股票成员不写）；池本身不写 `market`、`universe`、`factor_params`。池要绑定的因子任务是**成员所需市场的并集**（现货、永续各最多一个），每个绑定的 View 都要有 `close` 列，因为成员账本按收盘价估值。

**每期怎么算**（一句话版）：每个成员各自求值、得到成员目标 → 成员账本按本根收盘价估值 → 算本期分配比例 → 成员账本按成员目标调仓、追加期末净值 → 每个成员的目标乘各自的比例后按（标的, 市场）相加，得到池目标，再走组合约束（`limits`、`max_weight`、`leverage`）。没通过的成员（缺因子、被跳过）沿用它上期的目标；只有所有成员都没通过时，池才整期跳过。

### 10.1 三种分配方式

| `method` | 比例怎么来 | 适合 |
| --- | --- | --- |
| `fixed` | 直接用 `ratios`，不随业绩变；可配 `timing`、`step` | 固定比例 |
| `rotation` | 每期按成员净值上的因子排名，取前 `top` 名平分 | 业绩轮动 |
| `drift` | 比例随各成员持仓价值的自然变化漂移，再按两侧各自的规则拉回目标构成 | 多空长期持有、让盈利成员自然变大 |

`ratios` 要覆盖所有成员（不分配的写 0），可以不归一，总敞口受 `portfolio.leverage` 约束。

### 10.2 fixed 与 timing、step

```yaml
allocation:
  method: fixed
  ratios: {a: 0.6, b: 0.4}
timing: {window: 24}      # 成员净值严格高于 24 期均线取 1，否则取 0（序列不足取 1）；window 取 1 到 2016
step: 0.2                 # 每期每个成员的比例变化上限
```

- `timing` 让成员在自己的净值跌破均线时**休息**（比例变 0），回到均线之上再恢复。
- `step` 限制比例变化的速度：上期 0.6、目标 0.4、`step: 0.2` 时，本期取 0.4；目标 0 时只能每期降 0.2。上期没有比例时不限制。

### 10.3 rotation：业绩轮动

```yaml
allocation:
  method: rotation
  rotation:
    factors: [{name: return, window: 24, weight: 1}, {name: ma_ratio, window: 72, weight: 1}]
    top: 1                # 取名次靠前的几个，并列的平分
```

因子作用在成员的**净值序列**上，`order` 可选 `asc` 或 `desc`（默认 `return`、`ma_ratio` 降序，`drawdown` 升序）：

| 因子 | 含义 |
| --- | --- |
| `return(w)` | 区间收益 = 净值 ÷ w 期之前的净值 − 1（需要 w+1 个值） |
| `ma_ratio(w)` | 净值 ÷ 最近 w 期均线 − 1（需要 w 个值） |
| `drawdown(w)` | 最近 w+1 个净值上的最大回撤，越小越好 |

每个因子在未爆仓的成员之间排名（并列取平均名次），按因子权重加权得综合名次分，取最小的前 `top` 名（与第 `top` 名并列的一并入选）平分 1。序列不够长、算不出某个因子时，这一期不排名，未爆仓的成员均分。

### 10.4 成员净值：是模拟曲线，不是实盘收益

每个成员有自己的模拟账本：初始权益 1，按成员目标在收盘价调仓，扣手续费，永续逐期结算价差。**它只用来分配资金，不代表实盘收益**：不模拟最小下单额、拆单、强平与保证金率。

```yaml
fees: {spot_bps: 10, swap_bps: 5, equity_bps: 3, funding_column: funding_rate}
```

- `fees` 是成员账本的单边手续费（默认现货 10、永续 5、股票 3 个基点）。
- 写了 `funding_column`（取值是本根内各次结算费率之和）时，永续持仓按这一列结算资金费：费率为正时多头付、空头收；第 k 根的费率按第 k−1 根收盘调仓**之前**的持仓结算。只读永续的 View，要求池绑定了永续、且永续 View 有这一列。
- 成员账本权益不为正即模拟爆仓：从下一根起比例为 0，明细标 `bankrupt`。

### 10.5 组合约束：switch、exposure、gross

这三项都按**成员汇总的分配比例**计算：多头敞口 = 多头成员的比例之和，空头敞口 = 空头成员的比例之和，不按实际持仓折算、不做同标的抵消。因此写了 `drift`、`switch`、`exposure`、`gross` 任意一项，**每个成员都必须是单方向的**（规则全部做多或全部做空）。

执行顺序：步长之后（drift 是侧内再平衡之后）依次执行 `exposure` 各段 → `gross` → `switch`，再合成池目标。

**exposure：净敞口区间**（净敞口 = 多头敞口 − 空头敞口）

```yaml
exposure:
  bands:                                  # 各段按书写顺序执行
    - {min: -0.1, max: 0.3, adjust: auto, pivot: 1.0}
    - {when_gross_below: 1.0, min: 0, max: 0, adjust: raise_smaller}
```

净敞口超出 `[min, max]` 时，按**超出的方向**修正，侧内构成不变：

| `adjust` | 净敞口高于 `max` | 净敞口低于 `min` |
| --- | --- | --- |
| `reduce_larger` | 缩小多头一侧 | 缩小空头一侧 |
| `raise_smaller` | 放大空头一侧 | 放大多头一侧 |
| `auto`（必须写 `pivot`） | 总敞口大于 `pivot` 同 `reduce_larger`，否则同 `raise_smaller` | 同左 |

`when_gross_below` / `when_gross_above` 让一段只在执行到它时总敞口满足条件才生效。

推演（区间 `[-0.1, 0.3]`）：

| 多头 : 空头 | 净敞口 | `reduce_larger` | `raise_smaller` | `auto, pivot: 1.5` | `auto, pivot: 0.5` |
| --- | --- | --- | --- | --- | --- |
| 0.8 : 0.2 | 0.6，高于 max 0.3 | 多头缩到 0.5（净敞口 0.3） | 空头放大到 0.5 | 总敞口 1.0 ≤ 1.5，放大空头到 0.5 | 总敞口 1.0 > 0.5，缩多头到 0.5 |
| 0.2 : 0.8 | −0.6，低于 min −0.1 | 空头缩到 0.3 | 多头放大到 0.7 | — | — |

- 区间不含 0 时（如 `[0.2, 0.5]`）也按超出方向修正，让净敞口回到区间边界：多头 0.55、空头 0.45、净敞口 0.1 低于 min 0.2，`reduce_larger` 把空头缩到 0.35，`raise_smaller` 把多头放大到 0.65。
- 要缩放的一侧比例为 0 时无法修正，原样保留并在明细里注明。
- 条件段例子：多头 0.5、空头 0.3（总敞口 0.8 低于 1.0），第二段把净敞口拉到 0，空头放大到 0.5；多头 0.7、空头 0.5（总敞口 1.2）时这一段不执行。

**gross：总敞口区间**

```yaml
gross: {min: 1.0, max: 1.8}       # min、max 至少写一个，都不超过 portfolio.leverage
```

- 总敞口大于 `max` 时整体按比例缩小，小于 `min` 时整体按比例放大；整体缩放时净敞口按同一倍数变化。
- 池的第一根（还没推进过）**不放大**；drift 的漂移起点那一根同样不放大。
- 推演：多头 0.4、空头 0.2（总敞口 0.6），`gross: {min: 1.2, max: 1.5}`：第一根不放大；第二根整体放大 2 倍到多头 0.8、空头 0.4。多头 1.2、空头 0.6（总敞口 1.8）时，整体缩小到多头 1.0、空头 0.5。

**switch：条件转移**

```yaml
allocation:
  method: fixed
  ratios: {long_core: 0.5, short_core: 0.45, short_new: 0.05, probe: 0}
  switch:                                       # 按顺序判断，第一个成立的生效
    - when: {empty: probe}                      # probe 本期有效目标没有任何持仓
      move: {from: short_new, to: [short_core]} # 把 from 的比例全部转给 to
```

- 条件目前只有 `empty: 成员 id`。“有效目标”是成员本期参与合成的目标：成员没通过、沿用上期目标时按沿用后的目标判断。比例恒为 0 的“监控成员”照常求值，只用来做条件判断。
- `from` 的比例按 `to` 各成员本根的比例分给它们（都为 0 时平分）；`from` 与 `to` 必须同方向，保存时校验，要改净敞口请用 `exposure`。
- 只能与 `method: fixed` 或 `drift` 同写，不支持 `rotation`。

在 `fixed`、`rotation` 下，这三项只作用于**本期合成用的比例**，下一期步长用的上期比例仍是步长之后、这几步之前的值。`drift` 下不同：`exposure`、`gross` 修正后的系数会写入状态，下一根从它漂移；只有 `switch` 的转移与 `limits` 不写回（见 10.6）。

### 10.6 drift：漂移分配

`drift` 的想法：成员比例不再每根回到固定值，而是随成员持仓价值的自然变化漂移——持有的东西涨了，它在池里的占比就变大——然后每一侧（多头侧、空头侧）按自己的规则拉回目标构成。

```yaml
strategy_pool:
  members:
    - {id: long_a, strategy: strategy_a}
    - {id: long_b, strategy: strategy_b}
    - {id: short_a, strategy: strategy_c}
  allocation:
    method: drift
    ratios: {long_a: 0.3, long_b: 0.2, short_a: 0.5}   # 目标比例，覆盖所有成员
    start: "2026-03-01T00:00:00Z"                      # 可选：漂移起点（写某根 bar 的起点）
    sides:                                             # 不写的一侧不再平衡
      long: {rebalance: every_bar}
      short:
        rebalance: both
        period_bars: 2160
        threshold: 0.6
        spread_bars: 24
  freeze: {watch: short, window_bars: 1, threshold: 0.10, bars: 4}
```

**每根的漂移公式**：gᵢ′ = gᵢ·(1 + ρᵢ) ÷ (1 + Σⱼ gⱼ·πⱼ)，其中 gᵢ 是成员 i 的分配系数，ρᵢ 是成员持仓**名义价值**的收益率（空头的名义价值随价格上涨变大，但这时在亏钱），πᵢ 是成员**权益**的收益率，分母是整个账户权益的变化。

推演：多头成员 `l` 0.6、空头成员 `s` 0.4，各满仓、权益 1。一根里 `l` 持有的 X 涨 10%、`s` 做空的 Y 涨 20%：

| | 名义价值收益率 ρ | 权益收益率 π |
| --- | --- | --- |
| `l`（多 X） | +0.10 | +0.10 |
| `s`（空 Y） | +0.20（名义价值变大） | −0.20（亏钱） |

分母 = 1 + 0.6 × 0.10 + 0.4 × (−0.20) = 0.98；`l` 的系数 = 0.6 × 1.10 ÷ 0.98 ≈ **0.673**，`s` 的系数 = 0.4 × 1.20 ÷ 0.98 ≈ **0.490**。多头盈利后占比变大；空头亏钱但名义价值变大，系数也变大，总敞口从 1.0 增长到约 1.16，需要 `gross` 把它拉回区间。预算没用满、`scale` 截掉的部分、`filter_after` 留下的现金都不影响 ρ；成员空仓时只随权益变化。

**侧内再平衡**（每侧分别配置；侧的总量保持漂移后的值，只改侧内构成，目标构成是 `ratios` 的侧内比例）

| `rebalance` | 何时触发 |
| --- | --- |
| `none`（默认） | 不再平衡，侧内构成一直漂移 |
| `every_bar` | 每根 |
| `periodic` | 每隔 `period_bars` 根（相位按漂移起点或 `anchor` 算）；触发点落在被跳过的根上时，在之后第一根成功求值的根补触发 |
| `threshold` | 任一成员的 \|侧内占比 ÷ 目标侧内占比 − 1\| 超过 `threshold` |
| `both` | 周期或阈值任一满足；同一根都满足记 `periodic` |

- `spread_bars`（默认 1，一次到位）：触发后分几根走完，每根移动“当前构成到目标的距离 ÷ 剩余根数”，最后一根精确到达；分步期间不重新判断触发。
- `drift_by`：`notional`（默认）侧内构成按成员持仓名义价值漂移；`nav` 改按成员资金收益漂移（盈利的成员占比变大）。
- 阈值推演：`ratios: {long_a: 0.3, long_b: 0.2, ...}`，多头侧目标构成 long_a : long_b = 0.6 : 0.4。long_a 持有的标的涨 20% 后，侧内占比变成约 0.643 : 0.357；long_b 偏离 |0.357 ÷ 0.4 − 1| ≈ 10.7%，`threshold: 0.1` 触发，`threshold: 0.2` 不触发。目标比例为 0 的成员不参与阈值判断。
- 数值范围：`period_bars`、`spread_bars` 取值 1 到 10000，冻结的 `window_bars` 取 1 到 2016、`bars` 取 1 到 10000；`period_bars`、`anchor` 只能配 `periodic`、`both`，`threshold` 只能配 `threshold`、`both`，`spread_bars` 不能配 `none`。

**每根的顺序**：估值 → 漂移 → 冻结判断 → 侧内再平衡 → `exposure` → `gross` → `switch` → 合成。`gross` 之后的系数写入状态，下一根从它漂移。

### 10.7 freeze：波动冻结

```yaml
freeze: {watch: short, window_bars: 1, threshold: 0.10, bars: 4}
```

- `watch` 写 `long`、`short` 或成员 id 列表。被观察成员的组合净值 = Σ `ratios`ᵢ × 成员净值；与 `window_bars` 根之前比较，涨跌幅绝对值超过 `threshold` 时冻结 `bars` 根；冻结中再次触发从那一根重新计时。
- 冻结期间跳过侧内再平衡（分步暂停）、`exposure` 与 `gross`，**直接用漂移后的系数**；`switch`、`limits`、`leverage`、`max_weight` 照常执行。
- 用途：行情剧烈时不去追着把比例拉回来，等波动过去。只能与 `method: drift` 同写。

### 10.8 漂移起点

- `allocation.start: "<时刻>"`：漂移从这一根开始，系数置为 `ratios`；成员的状态与账本也从这一根起步，推进到最新已收盘的一根。
- `allocation.initial: {at: "<时刻>", ratios: {...}}`：在 `at` 那一根把系数置为给定的比例（例如从一次回放结果里取），之后照常漂移；与 `start` 二选一。
- 都不写：漂移从预热的第一根开始。
- 时刻必须是池周期上某根 bar 的起点；从起点到最新一根不能超过 20000 根，View 的历史要覆盖到起点，否则启用时拒绝。

### 10.9 池里的 limits 与结果字段

池目标合成之后，走与普通策略相同的组合约束：先 `limits`（分组权重是成员的合成比例乘成员目标），再 `max_weight`、`leverage`。成员冻结的标的保持上期池目标。

每期结果摘要的 `summary_json.pool` 记：各成员的状态、期末净值、基础比例、步长前后的比例、实际合成比例（`composed`，与 `ratio` 不同时才有）、总敞口与净敞口、各（标的、市场）的贡献；`adjustments` 记 `exposure`、`gross` 各段的修正；`drift` 记漂移阶段、各侧触发原因与冻结状态；研究回放里这些在 `decision.pool` 与 `decision.limits`。

### 10.10 预热与回放

- 启用池后先在后台**预热**：用历史按真实顺序推进成员账本，推进到最新已收盘的一根；预热完成前不求值、不能 `confirm`。
- 研究回放支持策略池：回放起点要在预热允许的范围内；写了 `allocation.start` 的 drift 池，回放起点必须等于 `start`，这样与实时逐期一致。
- 非 drift 池和没写起点的 drift 池，回放从区间第一根起步，与实时的前几期不可比。详见 [策略模块](模块/策略.md)“回放与实时的已知差别”。

## 11. 表达式

表达式用 Expr 语法编写，在 `filter`、`score`、`select.where`、`select.then.score`、`filter_after`、`entry`、`exit`、`hold.entry`、`hold.exit`、`scale.expr` 里使用。

**能引用什么**

| 写法 | 含义 |
| --- | --- |
| 列名 | 结果 View 的列（成员键） |
| `bars[0].列`、`bars[-1].列` | 当期、日历上紧邻的上一根；`bars[...]` 之后只能用 `.列名` |
| `instrument_id` | 标的 ID |
| `score` | 仅 `select.where`、`filter_after` 里可用 |
| `params("成员键", "参数名")` | 可变成员这一行实际用的参数 |
| `来源名.列名` | 写了 `sources` 时附加来源的列 |

**能用什么运算**

- 比较、算术、`&&`、`||`、`!`、三元 `a ? b : c`（两个分支类型必须一致）、`matches`（右边必须是字符串字面量的正则）。
- 内置函数一律禁用，只开放 `rank`、`zscore`，并且只能写在 `score`（含 `select.then.score`）里。
- `??`、范围 `..`、`in` 不可用。
- 写在 YAML 数值字段（`weight.total`、`cap`、`limits` 等）里的数字，写法不超过 64 个字符、精度不超过 18 位小数、绝对值小于 10^9。

**常见写法**

```yaml
filter: "volume_mean > 20000 && close > ma__20"             # 两个条件同时满足
filter: "instrument_id matches '^[A-Z]+-USDT$'"             # 只要 USDT 计价
entry: "bars[-1].bias <= 1 && bars[0].bias > 1"             # 上穿
score: "rank(mom__20) - rank(vol__24)"                      # 综合名次
scale: {expr: "close > ma__200 ? 1 : 0.5"}                  # 年线上方满仓，下方半仓
```

## 12. 校验、回放与上线

写入生产的策略定义和实例属于外部写操作，每一步先和用户确认。

**1. 先校验语法与列名**

```bash
moox-cli strategy validate --dsl-file s.yaml --factor-task swap=<永续因子任务>
```

只校验语法与结构，并在选定任务的结果 View 最近一期上试算一次，结果不落库。引用了不存在的列时，报错带最接近的列名和该任务的全部列。

**2. 历史回放，看策略在历史上的表现**

```bash
moox-cli strategy replay start --dsl-file s.yaml --factor-task swap=<永续因子任务> \
  --from 2026-01-01T00:00:00Z --to 2026-03-01T00:00:00Z --spot-fee-bps 10 --swap-fee-bps 6
moox-cli strategy replay show --replay-id <回放ID>
```

- 回放在 View 的历史数据上逐期求值，用理论账本按收盘价成交。**默认不计资金费，不模拟强平与保证金率**；用 `funding_column` 才计资金费（策略池写在池的 `fees` 里）。
- 回放只用已闭合的 K 线，终点截到最新一根之前，根数不超过 20000。起点要留够 `min_age_bars` 与 `bars[-1]` 所需的历史，否则提交时提示可用起点。
- 回放用来比较策略版本，不承诺复现历史上的实时结果；与实时的差别见 [策略模块](模块/策略.md)“回放与实时的已知差别”。
- 每期记录里有解释明细：每个标的的最终阶段与原因，以及 `decision.limits`、`decision.pool`。

**3. 创建定义、创建实例、启用**

```bash
moox-cli strategy create --dsl-file s.yaml
moox-cli strategy instance create --strategy-id <ID> --factor-task swap=<永续因子任务>
moox-cli strategy instance enable --instance-id <实例ID> --yes
```

不挂账户的实例是**观察实例**：只计算目标权重、不投递，适合先验证。首次启用进入试运行（只发带影子标记的目标，Trade 不下单），挂账户的实例要用户 `confirm` 才进入运行。完整流程与注意事项见 `skills/moox/references/strategy-dsl.md` 第 7 节。

## 13. 示例配方

下面每个例子都是完整的策略，列名换成你的因子任务里的列即可。

### 13.1 动量 Top N，现货做多

```yaml
name: spot_momentum_top10
universe: {min_age_bars: 720}
rules:
  - id: momentum
    type: rank
    filter: "quote_volume_mean > 1000000"
    score: "rank(mom__20)"
    select: {top: 10, buffer: 2}
    weight: {total: 1, method: equal, cap: 0.15}
portfolio: {leverage: 1, max_weight: 0.15}
```

### 13.2 永续多空对冲

```yaml
name: swap_long_short
market: {select: swap}
universe: {min_age_bars: 720}
rules:
  - id: long_leg
    type: rank
    score: "rank(mom__20)"
    select: {top: 0.1, count_min: 3}        # 最强的 10%，至少 3 个
    weight: {total: 0.5}
  - id: short_leg
    type: rank
    side: short
    score: "-rank(mom__20)"                  # 取负：最弱的被做空
    select: {top: 0.1, count_min: 3}
    weight: {total: 0.5}
portfolio: {leverage: 1}
```

### 13.3 低波入场、过热出场（hold）

```yaml
name: quiet_hold
market: {select: swap}
rules:
  - id: quiet_long
    type: rank
    filter: "volume_mean > 0"
    score: "-vol_ratio"
    select: {top: 0.1, count_min: 1}
    hold:
      entry: "vol_pct < 0.1"
      exit: "vol_pct > 0.7"
      max_bars: 720
      cooldown_bars: 0
      allow_reentry: true
    weight: {total: 1}
portfolio: {leverage: 1}
```

### 13.4 先粗选再细选（then）

```yaml
name: calm_then_strong
rules:
  - id: pick
    type: rank
    score: "-vol__24"                        # 第一段：波动最小的 20%
    select:
      top: 0.2
      then: {score: "rank(mom__20)", top: 5} # 第二段：其中动量最强的 5 个
    weight: {total: 1}
portfolio: {leverage: 1}
```

### 13.5 分批换仓

```yaml
name: staggered_rebalance
rules:
  - id: pick
    type: rank
    score: "rank(mom__20)"
    select: {top: 10}
    holding: {bars: 24, offsets: [0, 6, 12, 18]}   # 四个批次，每个 24 根重选一次，相隔 6 根
    weight: {total: 1}
portfolio: {leverage: 1}
```

### 13.6 BTC 趋势择时加行业分散选股

```yaml
name: trend_plus_rotation
rules:
  - id: btc_trend
    type: signal
    pool: [BTC-USDT]
    entry: "bars[-1].bias <= 1 && bars[0].bias > 1"
    exit: "bars[0].bias < 1"
    weight: {total: 0.2}
  - id: alt_rotation
    type: rank
    score: "rank(mom__20)"
    select: {top: 6, group: {by: industry, max: 2}}
    weight: {total: 0.8, cap: 0.2}
portfolio: {leverage: 1}
```

### 13.7 单标的上限，超出的转给另一条规则

```yaml
name: capped_with_overflow
rules:
  - id: concentrated
    type: rank
    score: "rank(mom__20)"
    select: {top: 1}
    weight: {total: 0.6}
  - id: diversified
    type: rank
    score: "rank(mom__20)"
    select: {top: 10}
    weight: {total: 0.3}
portfolio:
  leverage: 1
  limits:
    long_max: 0.3
    overflow: {to: diversified}
```

### 13.8 多空两个成员的固定池，控制净敞口

```yaml
name: long_short_pool
bar: 1h
strategy_pool:
  members:
    - {id: long_core, strategy: strategy_long}
    - {id: short_core, strategy: strategy_short}
  allocation:
    method: fixed
    ratios: {long_core: 0.7, short_core: 0.3}
  exposure:
    bands:
      - {min: -0.1, max: 0.3, adjust: auto, pivot: 1.0}
  gross: {min: 0.8, max: 1.2}
  fees: {spot_bps: 10, swap_bps: 6}
portfolio: {leverage: 1.2}
```

### 13.9 漂移池，带冻结

```yaml
name: drift_pool
bar: 1h
strategy_pool:
  members:
    - {id: long_a, strategy: strategy_a}
    - {id: long_b, strategy: strategy_b}
    - {id: short_a, strategy: strategy_c}
  allocation:
    method: drift
    ratios: {long_a: 0.3, long_b: 0.2, short_a: 0.5}
    start: "2026-03-01T00:00:00Z"
    sides:
      long: {rebalance: every_bar}
      short: {rebalance: threshold, threshold: 0.3, spread_bars: 12}
  freeze: {watch: short, window_bars: 1, threshold: 0.10, bars: 4}
  exposure:
    bands:
      - {min: -0.1, max: 0.3, adjust: auto, pivot: 1.0}
  gross: {min: 1.0, max: 1.8}
  fees: {spot_bps: 10, swap_bps: 6}
portfolio: {leverage: 1.8}
```

## 14. 报错速查

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 保存即拒绝，提示“包含未知字段” | 解析严格，拼错键名或写了不存在的字段 | 按本手册字段名核对 |
| `hold.allow_reentry 不能与大于 0 的 cooldown_bars 同时使用` | 允许当根补回与冷却语义冲突 | 去掉冷却，或不写 `allow_reentry` |
| 提示互斥（hold 与 buffer、then、range、filter_after、holding.bars） | 这些写法语义冲突 | 按提示二选一 |
| 提示“weight.cap 乘以最多可持有的标的数小于 weight.total，预算永远分不完” | 预算分不完 | 调大 `cap` 或 `select.top`，或调小 `total` |
| 提示使用了 `max_age_bars` 必须写 `allow_empty: true` | 只保留新标的的规则经常没有候选 | 在这条规则上写 `allow_empty: true` |
| `portfolio.limits.overflow.to … 不是规则 / 成员` | 转移去向写了不存在的 id | 写成存在的规则 id（池里写成员 id） |
| `portfolio.limits 至少写 long_max、short_max 之一` | `limits` 里没有任何上限 | 至少写一个上限 |
| 提示成员同时有多头与空头规则 | 写了 `drift`、`switch`、`exposure`、`gross`，成员必须单方向 | 把成员拆成纯多头和纯空头 |
| `adjust 是 auto，需要写 pivot` | `auto` 要用 `pivot` 判断缩还是放 | 写上 `pivot` |
| `allocation.method 是 drift，不能写 timing / step` | 择时与步长只用于 `fixed`、`rotation` | 去掉，或改用 `fixed` |
| `allocation.start 与 initial 只能写一个` | 两种起点只能选一种 | 删掉其中一个 |
| `strategy_pool.freeze 只能与 allocation.method: drift 一起写` | `freeze` 只作用于漂移 | 改用 `drift` |
| `rules 与 strategy_pool 只能写一个` | 一个策略不能既有规则又有池 | 拆成两个策略，池引用规则策略当成员 |
| 池实例启用被拒，提示成员没有显式写 market | 现货、永续成员必须写 `market` | 给成员策略补上 `market` |
| 启用被拒，提示列不存在 | 规则引用的列不在因子任务结果里 | 按成员键推导列名；默认参数的成员省略参数段 |
| 启用被拒，提示 holding.anchor 不是 bar 起点 | 锚点要写某根 bar 的 `bar_start` | 改成 bar 起点 |
| 整期 `skipped(config_error)`，提示分数全部无效 | `score` 引用的列写错或全为空 | 核对列名，看因子是否在算 |
| 某根整期 `skipped(too_many_missing)` | 缺数占比超过 `max_missing` | 查因子是否失败，必要时放宽 |
