# 策略 DSL 语法与写入流程

策略用一份 YAML（DSL）描述：怎么在截面上选标的、怎么配权。时序计算（均线、动量、分位）一律写成因子（见 [factor-dsl.md](factor-dsl.md)），
策略只在截面上过滤、打分、配权。Go 代码只提供通用规则类型（`rank`、`signal`、`tranche`），新策略写配置即可，不改代码。

本文是 Agent 写策略时的语法速查；字段的完整语义、状态与明细以 `docs/模块/策略.md` 的第 3 节为准，解析与校验的源码在 `modules/strategy/internal/dsl/`。
整套 Python 策略迁移成 DSL 的流程见 [strategy-migration.md](strategy-migration.md)。

## 1. 顶层结构

```yaml
name: dual_ma_swap_1m          # 策略名
bar: 1m                        # 可选断言：与 View 周期不一致时拒绝启用
universe: {min_age_bars: 15}   # 策略级默认标的池，省略即全部
rules: [...]                   # 至少一条规则
portfolio: {leverage: 1}       # 组合级约束
factor_params: {...}           # 可选：可变成员的逐期调参（见 docs/模块/策略.md「动态调参」）
```

- 解析严格：重复键、未知字段、多文档一律拒绝，错误信息是中文。
- 规则里引用的列名就是**因子任务结果数据集的列名**，由成员键推导：`因子ID[__参数段][__输出名]`，参数全为默认值时省略参数段，可变成员以 `__var` 结尾
  （例如成员 `ma` 窗口 5 的列是 `ma__5`）。创建实例选好因子任务后，页面和校验报错都会列出该任务的全部列。
- 限制：DSL 文本 ≤ 64 KiB（含参数表的策略放宽到 1 MiB）；单条表达式 ≤ 1024 字符、≤ 256 个语法节点；`portfolio.leverage` ≤ 10。

### universe 与 portfolio

| 字段 | 说明 |
| --- | --- |
| `universe.tags` / `include` | 任一非空时取并集作白名单；都为空表示全部；再减去 `exclude_tags` 与 `exclude` |
| `universe.min_age_bars` | 标的在这么多根之前已有数据才参与；对所有规则生效 |
| `portfolio.leverage` | gross 上限，默认 1；现货 View 不允许大于 1；各规则 `weight.total` 之和不超过它 |
| `portfolio.max_weight` | 组合级单标的上限，超出部分留现金 |
| `portfolio.min_universe` | rank 规则进入 filter 的可用标的少于它则整期跳过 |
| `portfolio.max_missing` | 任一规则缺数占比超过它则整期跳过，默认 0.2 |

## 2. 规则类型

每条规则必须有稳定的 `id`（`^[a-z][a-z0-9_]{0,63}$`）；**改 id 等于规则状态从零开始**。`name` 只用于展示。

| `type` | 用途 | 关键字段 |
| --- | --- | --- |
| `rank` | 截面选股 | `filter`、`score`、`select`、`weight`，可选 `holding`、`filter_after`、`side`、`stop` |
| `signal` | 固定池择时 | `pool`、`entry`、`exit`、`weight`，可选 `stop` |
| `tranche` | 批次持仓：每根 K 线按本期选币新开一批，每批持有 h 期，多批叠加 | `tranche` 子策略模板、可选 `params` 参数表、`weight`，见第 4 节 |

含 `tranche` 规则的策略只能全部是 `tranche` 规则，且不能写 `factor_params`，只能绑定合约 View。

### rank 规则

```yaml
- id: ma_above
  type: rank
  filter: "ma__5 > ma__10"                 # 布尔表达式，淘汰的标的记为 filtered
  score: "rank(ma__5 / ma__10 - 1)"        # 数值表达式；rank(x)、zscore(x) 在通过 filter 的样本上计算，可嵌套
  select: {top: 10000, buffer: 2}          # top 与 bottom 二选一；where 二次筛选；buffer 是排名缓冲区
  weight: {total: 1, method: equal, cap: 0.05}   # method: equal | rank；cap 是规则内单标的上限
```

- 保存时要求 `cap × 选中数量 ≥ total`（预算分得完）。`select.top` 写成大于标的数的值就等于全选。
- `holding: {bars, offsets}` 分批换仓，与 `buffer` 互斥。`filter_after` 只剔除不补位，剔除的权重留现金。
- `side` 取 `long`（默认）或 `short`；现货 View 不允许做空。

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
- `params("成员键", "参数名")`：读取可变成员这一行实际用的参数。

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
  leverage: 2                               # 按数量持有会让 Σw 略大于 1，留出上限，不缩放
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

- 保存模板：`moox-cli gateway call trpc.moox.strategy.StrategyMgr SaveStrategyTemplate --data @save.json`，`save.json` 是 `{"template_yaml": "<模板原文>"}`。

## 7. 校验、创建与上线

写入生产的策略定义和实例属于外部写操作，每一步先告诉用户并等确认。

1. **本地先看语法**：模板用 `moox-strategy-cli template check`；普通 DSL 用 `ValidateStrategy`（只校验语法与结构，并在选定因子任务的结果 View 最近一期上试算一次，结果不落库；引用了不存在的列时报错带最接近的列名和该任务的全部列）。

   ```bash
   moox-cli gateway call trpc.moox.strategy.StrategyMgr ValidateStrategy --file ./moox.toml --data @validate.json
   ```

2. **创建定义**：`CreateStrategy`；已有定义用 `UpdateStrategy` 修改。创建后用 `GetStrategy` 读回 DSL 核对。
3. **创建实例**：`CreateStrategyInstance` 必须带 `factor_task_id`（只能选本空间里运行中、还没被别的实例关联的任务），**创建后不能改，实例也不能删，只能启用或停止**。
   不挂账户的是观察实例：只计算目标权重、不投递，适合先验证。
4. **启用**：`SetStrategyInstanceEnabled`。启用时才解析列、成员契约和市场类型，列不存在或现货做空等问题在这一步被拒绝；
   任务刚建、结果 View 还没数据时可能因“还没有数据（覆盖范围未知）”被拒，等任务算出几个周期后再启用。
5. **看结果**：`ListStrategyResults`、`ListStrategyTargets`（当前会话最新的 ok 目标）；控制台 `/strategy/detail/<实例ID>`。
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
| 提示 `cap × 选中数量 < total` | 预算分不完 | 调大 `cap` 或 `select.top`，或调小 `total` |
| 启用被拒，提示列不存在 | 规则引用的列不在因子任务结果里 | 按成员键推导列名；默认参数的成员省略参数段 |
| 含参数表的 DSL 被拒（超 64 KiB） | 参数表很大 | 含参数表的策略上限是 1 MiB，仍超出就拆成多条规则或多个策略 |
| tranche 策略启用被拒 | 绑定了现货 View，或混用了 `rank` 规则、`factor_params` | tranche 只能全部是 tranche 规则，且只绑合约 View |
