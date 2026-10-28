# 因子表达式（DSL）与 Python 因子转换

MooX 的因子用一份 YAML 表达式定义（spec）描述，由 Go 写的表达式引擎计算；旧的 Python 因子（`compute(df, params, context)`）已经退役。
用户拿着原来的 Python 因子来时，按本文把它转写成标准写法、逐值核对、确认后写入系统。

全文的 `SKILL_ROOT` 指当前加载的 `SKILL.md` 所在目录的真实绝对路径（与 [data-query.md](data-query.md) 相同的约定），
每个命令块都先把它替换成真实路径再执行。

## 1. 工具

| 工具 | 作用 | 依赖 |
| --- | --- | --- |
| `"$SKILL_ROOT/scripts/factor-expr.sh" functions [--json]` | 列出全部函数、参数范围、回看规则（权威清单，与引擎同一份注册表） | Go 工具链 |
| `"$SKILL_ROOT/scripts/factor-expr.sh" check --library lib.yaml [--factor-id ID]` | 编译定义，输出类型、输入列、输出列、因子回看与每个输出的回看、规范文本；失败时给字段路径与行列 | Go 工具链 |
| `"$SKILL_ROOT/scripts/factor-expr.sh" eval --library lib.yaml --factor-id ID --input bars.csv --freq 1m --output dsl.csv` | 用引擎的编译器与执行器在 K 线 CSV 上试算：按频率建规则网格（`--freq` 必填，1m/5m/15m/30m/1h/4h/1d），并像流水线一样屏蔽回看窗口有空洞的结果 | Go 工具链 |
| `python3 "$SKILL_ROOT/scripts/factor_py_check.py" synthetic` | 生成合成 OHLCV K 线 | 标准库 |
| `python3 "$SKILL_ROOT/scripts/factor_py_check.py" rows-to-csv` | 把 `data-kline.sh`、`moox-cli data rows export` 的 JSON 转成 CSV | 标准库 |
| `python3 "$SKILL_ROOT/scripts/factor_py_check.py" run-python` | 按旧契约运行原 Python 因子，context 与旧 worker 相同（见第 6 节第 10 条） | pandas、numpy（用用户原来的环境） |
| `python3 "$SKILL_ROOT/scripts/factor_py_check.py" compare` | 逐值比较原结果与 DSL 结果，按第 5 节的规则判定通过 / 失败 | 标准库 |

`factor-expr.sh` 在 Skill 所在的 MooX 仓库里构建 `packages/factorexpr/cmd/factorexpr`，相对路径按调用时的工作目录解析；
打包分发的 Skill（`dist/moox-skill.tar.gz`）不含仓库源码，这时要在 MooX 仓库里使用它，`factor_py_check.py` 则可以单独使用。
CSV 统一格式：`data_time`（RFC3339 UTC）、`subject_id`、`series_tag`、各数值列，空值写空串。

## 2. 新旧对照

| 方面 | 旧 Python 因子 | 表达式（DSL） |
| --- | --- | --- |
| 定义形式 | 一个 `.py` 文件，`compute(df, params, context)` 返回 DataFrame | 因子库 YAML 里的一项 `{factor_id, name, spec, allow_partial_universe}` |
| 元数据 | 目录里手填 `factor_type`、`input_columns`、`outputs`、`params`、`lookback_periods` | 只写 spec；类型、输入列、输出列、回看全部由编译推导 |
| 参数 | `params` 字典，常见 `window` / `windows` / `output_names` | `params:` 声明可调参数（类型、范围、默认值）；输出列名固定，不再有列表参数与 `{参数名}` 列名模板；策略可在运行中每期调整取值 |
| 中间量 | 局部变量 | `let:`，有序，后面的可以引用前面的 |
| 输出 | DataFrame 的列 | 单输出写 `output: <表达式>`（列名就是成员键）；共用参数的多输出写 `outputs:` 输出名 → 表达式（至少两个），最多 64 个 |
| 分组 | 时序因子要自己 `groupby("series_tag")`；截面因子自己 `groupby(data_time)` | 自动：时序算子按 (subject_id, series_tag) 序列滚动，截面算子按 (data_time, series_tag) 分组 |
| 窗口 | `rolling(n, min_periods=…)`，常见 `min_periods=1` | 严格窗口：窗口内有一个 NaN 结果就是 NaN，没有 `min_periods` |
| 时间轴 | 只有存在的行，`rolling` 按行数滚动 | 按频率的规则网格，缺失的 K 线读作 NaN；流水线还会检查因子回看窗口（从该标的第一根 K 线起算），窗口里缺 K 线时该因子在该标的该期的全部输出都不写 |
| 空值 | NaN / ±Inf 写成 null | 同样：NaN、±Inf 一律记为 NaN，写入时为 null |
| 截面全集 | `allow_partial_universe` | 不变：false 时本期全集不完整就整因子跳过，true 时剔除不可用标的照常计算（只对截面因子有效） |

## 3. 表达式规格

```yaml
params:                            # 可选：可调参数的声明；type 为 int 或 float，min/max/default 必填
  window: {type: int, min: 2, max: 120, default: 20}
let:                               # 可选：有序中间量，只能引用前面的 let、params 和输入列
  ma: ts_mean(close, window)
output: close / ma - 1              # 单输出写 output；多输出写 outputs: {输出名: 表达式, …}（输出名固定，不随参数变化）
```

- 输入列就是表达式里出现的源数据列名（`open`、`high`、`low`、`close`、`volume`、`quote_volume` 等，以源数据集实际的列为准），不用声明。
- 名字（因子 ID、输出名、参数名）一律小写：`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`，名字内部用单下划线，不能连续下划线（双下划线留给成员键与列名的分段）；
  因子 ID 最长 24 个字符，输出名与参数名最长 16 个字符。`params`、`let` 的名字也不能与函数、系统列（`subject_id`、`freq`、`data_time`、`series_tag`）或 `true`/`false`/`nil` 重名。
- 输出：单输出因子写 `output: <表达式>`，没有输出名，列名就是成员键；多输出因子写 `outputs`（至少两个），输出名不能等于因子 ID、不能是 `var`、不能匹配参数值语法（如 `n5`），也不带因子前缀（写 `mid`，不写 `boll_mid`）。
  只有确实共用同一组参数的输出才放在一个因子里；不共用参数的拆成独立因子，引擎同一期合并公共子表达式，不会多算。
- 语法：数字、`true`/`false`、标识符、`+ - * / % **`、比较 `== != < <= > >=`、`and`/`or`/`not`、`c ? a : b` 和函数调用；其余一律拒绝。
- 窗口、位移等结构位置只接受整数字面量或**整数参数**（范围必须落在算子允许范围内），回看按参数范围上限推导，所以策略调参时窗口永远够用；其余数值位置可以放字面量或任意参数。
- 参数声明进因子指纹（`definition_hash`），取值不进：改范围、默认值会改变指纹，策略调整取值不会。同一定义可以用不同的固定参数多次加入同一个因子任务（成员键不同），也可以作为可变成员由策略逐期调参，见下一节。
- NaN 语义：任一输入为 NaN 结果即为 NaN（`where`、`fill_nan`、`is_nan` 除外）；除以 0 为 NaN；比较运算遇到 NaN 结果也是 NaN。
- 回看由编译推导：`ts_*` 为 L(x)+n−1，`delay`/`delta`/`pct_change` 为 L(x)+k，嵌套相加（`ts_rank(ts_mean(x, 20), 20)` 是 39）；
  `ema`/`sma` 截断到起点权重低于 1e−9 的 K 根（`ema(x, 20)` 为 208）；`ema(x, n, k)` 显式指定预热总根数 K=k（含起点那一根，窗口里最老的一根作为递推初值）：`ema(close, 20, 50000)` 是 EMA20 用 5 万根预热，k 不写或为 0 按通常方式；`ema(close, 50000, 50000)` 的起点权重约 13%，是近似值。
- 用到任何 `cs_*` 的因子是截面因子，否则是时序因子。
- 限制：spec 64 KiB，单条表达式 4096 字符、512 个节点，最多 16 个参数，展开后最多 64 个输出，窗口 1～100000，回看不超过 100000；实际可用的回看还受数据源回看数量、保留期与引擎 `window.lookback` 的限制。

### 成员键与列名

因子加入因子任务后成为「成员」，结果数据集里的列名由「因子 ID + 参数 + 是否可变 + 输出」按规则推导，不存别名。写策略规则、筛选条件或 SQL 时，列名就是这里推导出的名字：

```text
成员键 = 因子ID [ "__" 参数段 ]      参数段 = "var" | 值 { "_" 值 }      值 = n?[0-9]+(p[0-9]+)?
列名   = 成员键 [ "__" 输出名 ]       单输出因子没有输出段
```

- **参数值的写法**：十进制，小数点写 `p`、负号写 `n`（`0.5` → `0p5`，`-1` → `n1`），只写值、不写参数名，顺序是定义里 `params` 的声明顺序。
- **参数全为默认值时省略参数段**，成员键就是因子 ID（`rsi`、`boll`）；**可变成员（由策略逐期提交参数）固定以 `__var` 结尾**（`ma_fast__var`），一个因子在一个任务里最多一个可变成员，需要多个可变值就注册多个因子（`ma_fast`、`ma_slow`）。
- **单输出与多输出**：单输出因子的列名就是成员键；多输出因子的列名是成员键加 `__输出名`。

| 成员 | 成员键 | 列名 |
| --- | --- | --- |
| `ema`，span=20、warmup=50000（固定） | `ema__20_50000` | `ema__20_50000` |
| `rsi`，参数全为默认值 | `rsi` | `rsi` |
| `boll`，参数全为默认值（输出 mid/up/low/pctb） | `boll` | `boll__mid`、`boll__up`、`boll__low`、`boll__pctb` |
| `boll`，window=30 | `boll__30` | `boll__30__mid` 等 |
| `ma_fast`（可变） | `ma_fast__var` | `ma_fast__var` |

策略规则里直接用列名；要读可变成员这一行**实际用的**参数，用 `params("成员键", "参数名")`（值来自结果行的系统列 `__params`，记录这一行所有成员实际用的参数，包括固定成员）：

```yaml
rules:
  - id: trend
    type: rank
    filter: "close > ema__20_50000 && rsi < 70 && close > boll__mid"
    score: 'rank(ma_fast__var / close) - 0.01 * params("ma_fast__var", "window")'
    select: {top: 10}
    weight: {total: 1}
```

完整规则（含长度上限、`__params` 的结构与可变成员的提交流程）见 `docs/模块/因子.md` 的「成员键与列名」「动态调参」两节与 `docs/模块/策略.md`；加成员时控制台和 `moox-cli setup factors` 都会直接打印成员键与列名。

### 函数清单

以 `factor-expr.sh functions` 的输出为准；下表由它生成（2026-10-18）。

| 类别 | 函数 | 参数 | 回看 | 说明 |
| --- | --- | --- | --- | --- |
| 逐元素 | `abs` | x | 各参数回看的最大值 | 绝对值 |
| 逐元素 | `clip` | x、lo、hi | 各参数回看的最大值 | 截断到 [lo, hi]；lo > hi 时为 NaN |
| 逐元素 | `exp` | x | 各参数回看的最大值 | 自然指数 |
| 逐元素 | `fill_nan` | x、v | 各参数回看的最大值 | x 为 NaN 时取 v |
| 逐元素 | `is_nan` | x | 各参数回看的最大值 | x 是否为 NaN（结果不会是 NaN） |
| 逐元素 | `log` | x | 各参数回看的最大值 | 自然对数；x ≤ 0 时为 NaN |
| 逐元素 | `max` | a、b | 各参数回看的最大值 | 二元取大；任一为 NaN 时为 NaN |
| 逐元素 | `min` | a、b | 各参数回看的最大值 | 二元取小；任一为 NaN 时为 NaN |
| 逐元素 | `power` | x、y | 各参数回看的最大值 | x 的 y 次方；底数为负且指数不是整数时为 NaN |
| 逐元素 | `sign` | x | 各参数回看的最大值 | 符号：正数为 1，负数为 −1，0 为 0 |
| 逐元素 | `signed_power` | x、e | 各参数回看的最大值 | sign(x) · \|x\|^e |
| 逐元素 | `sqrt` | x | 各参数回看的最大值 | 平方根；x < 0 时为 NaN |
| 逐元素 | `where` | c、a、b | 各参数回看的最大值 | 条件选择：c 为真取 a，为假取 b；c 为 NaN 时为 NaN。等价于 c ? a : b |
| 时序 | `delay` | x（序列）、k（常量 1～100000） | L(x)+k | k 期前的值 |
| 时序 | `delta` | x（序列）、k（常量 1～100000） | L(x)+k | x − delay(x, k) |
| 时序 | `ema` | x（序列）、n（常量 1～100000）、k（常量 0～100000，可选） | L(x)+K−1，K 为 k；k 不写或为 0 时为让起点权重衰减到 1e−9 以下的根数 | 指数移动平均，α = 2/(n+1)，截断到最近 K 根（ewm(span=n, adjust=False)），窗口里最老的一根作为递推初值 |
| 时序 | `ema_relaxed` | 同 `ema` | 同 `ema` | 同 `ema`；窗口开头是 NaN（序列还没开始）时从第一个有效值开始递推，相当于 ewm(span=n, adjust=False) 从序列首根算起；有效值之后再出现 NaN 时为 NaN |
| 时序 | `pct_change` | x（序列）、k（常量 1～100000），默认 1 | L(x)+k | x / delay(x, k) − 1；分母为 0 时为 NaN |
| 时序 | `sma` | x（序列）、n（常量 2～100000）、m（常量 1～100000） | L(x)+K−1，K 为让起点权重衰减到 1e−9 以下的根数 | 国君 SMA：y = (m·x + (n−m)·y₋₁) / n，截断到最近 K 根 |
| 时序 | `ts_argmax` | x（序列）、n（常量 1～100000） | L(x)+n−1 | 窗口内最大值距今的根数（0 为当期，并列取最早的一根） |
| 时序 | `ts_argmin` | x（序列）、n（常量 1～100000） | L(x)+n−1 | 窗口内最小值距今的根数（0 为当期，并列取最早的一根） |
| 时序 | `ts_corr` | x（序列）、y（序列）、n（常量 2～100000） | L(x)+n−1 | 窗口内相关系数；任一方标准差为 0 时为 NaN |
| 时序 | `ts_count` | c（序列）、n（常量 1～100000） | L(x)+n−1 | 窗口内条件为真的个数 |
| 时序 | `ts_cov` | x（序列）、y（序列）、n（常量 2～100000） | L(x)+n−1 | 窗口内样本协方差（ddof=1） |
| 时序 | `ts_decay_linear` | x（序列）、n（常量 1～100000） | L(x)+n−1 | 线性衰减加权平均：最新一根权重为 n，最老一根为 1 |
| 时序 | `ts_kurt` | x（序列）、n（常量 4～100000） | L(x)+n−1 | 窗口内无偏超额峰度 |
| 时序 | `ts_max` | x（序列）、n（常量 1～100000） | L(x)+n−1 | 窗口内最大值 |
| 时序 | `ts_max_relaxed` | x（序列）、n（常量 1～100000）、m（常量 1～100000） | L(x)+n−1 | 窗口内最大值；序列开头的部分窗口：窗口开头是 NaN（序列还没开始）时在不少于 m 个有效值上计算（rolling(n, min_periods=m)）；有效值之后再出现 NaN 时为 NaN |
| 时序 | `ts_mean` | x（序列）、n（常量 1～100000） | L(x)+n−1 | 窗口内均值 |
| 时序 | `ts_mean_relaxed` | x（序列）、n（常量 1～100000）、m（常量 1～100000） | L(x)+n−1 | 窗口内均值；序列开头的部分窗口：窗口开头是 NaN（序列还没开始）时在不少于 m 个有效值上计算（rolling(n, min_periods=m)）；有效值之后再出现 NaN 时为 NaN |
| 时序 | `ts_median` | x（序列）、n（常量 1～100000） | L(x)+n−1 | 窗口内中位数 |
| 时序 | `ts_min` | x（序列）、n（常量 1～100000） | L(x)+n−1 | 窗口内最小值 |
| 时序 | `ts_min_relaxed` | x（序列）、n（常量 1～100000）、m（常量 1～100000） | L(x)+n−1 | 窗口内最小值；序列开头的部分窗口：窗口开头是 NaN（序列还没开始）时在不少于 m 个有效值上计算（rolling(n, min_periods=m)）；有效值之后再出现 NaN 时为 NaN |
| 时序 | `ts_product` | x（序列）、n（常量 1～100000） | L(x)+n−1 | 窗口内连乘 |
| 时序 | `ts_quantile` | x（序列）、n（常量 1～100000）、q（常量 0～1） | L(x)+n−1 | 窗口内 q 分位数，线性插值 |
| 时序 | `ts_rank` | x（序列）、n（常量 1～100000） | L(x)+n−1 | 当期值在窗口内的百分位，并列取平均名次（pandas rank(pct=True)） |
| 时序 | `ts_resi` | y（序列）、n（常量 2～100000） | L(x)+n−1 | 对时间序号 1..n 做最小二乘后当期的残差 |
| 时序 | `ts_rsquare` | y（序列）、n（常量 2～100000） | L(x)+n−1 | 对时间序号 1..n 做最小二乘的 R² |
| 时序 | `ts_skew` | x（序列）、n（常量 3～100000） | L(x)+n−1 | 窗口内无偏偏度 |
| 时序 | `ts_slope` | y（序列）、n（常量 2～100000） | L(x)+n−1 | 对时间序号 1..n 做最小二乘的斜率 |
| 时序 | `ts_std` | x（序列）、n（常量 2～100000） | L(x)+n−1 | 窗口内样本标准差（ddof=1） |
| 时序 | `ts_std_relaxed` | x（序列）、n（常量 2～100000）、m（常量 2～100000） | L(x)+n−1 | 窗口内样本标准差（ddof=1）；序列开头的部分窗口：窗口开头是 NaN（序列还没开始）时在不少于 m 个有效值上计算（rolling(n, min_periods=m)）；有效值之后再出现 NaN 时为 NaN |
| 时序 | `ts_sum` | x（序列）、n（常量 1～100000） | L(x)+n−1 | 窗口内求和 |
| 时序 | `ts_var` | x（序列）、n（常量 2～100000） | L(x)+n−1 | 窗口内样本方差（ddof=1） |
| 时序 | `ts_zscore` | x（序列）、n（常量 2～100000） | L(x)+n−1 | (x − ts_mean) / ts_std；标准差为 0 时为 NaN |
| 截面 | `cs_demean` | x（序列） | L(x) | 截面去均值 x − mean |
| 截面 | `cs_rank` | x（序列） | L(x) | 截面百分位（名次 / 有效样本数，并列取平均名次）；按 (data_time, series_tag) 分组，排除 NaN |
| 截面 | `cs_scale` | x（序列） | L(x) | 截面缩放 x / sum(\|x\|)；分母为 0 时为 NaN |
| 截面 | `cs_zscore` | x（序列） | L(x) | 截面标准化 (x − mean) / std（ddof=1）；样本少于 2 或标准差为 0 时为 NaN |

### 宽松窗口算子（`*_relaxed`）

引擎的时序算子默认是**严格窗口**：窗口里没凑满、或有 NaN，结果就是 NaN。宽松窗口算子是底层能力，让序列开头、窗口还没攒满时也能出值：
`ema_relaxed(x, n, k)`、`ts_mean_relaxed(x, n, m)`、`ts_std_relaxed(x, n, m)`、`ts_min_relaxed(x, n, m)`、`ts_max_relaxed(x, n, m)`。

- `ts_*_relaxed(x, n, m)` 对应 pandas `rolling(n, min_periods=m)`：窗口开头是 NaN（序列还没开始）时，有效值不少于 `m` 个就出值；`m ≤ n`，`ts_std_relaxed` 的 `m ≥ 2`。
- `ema_relaxed(x, n, k)` 对应 `ewm(span=n, adjust=False)` 从序列首根递推；`k` 的含义同 `ema`。
- 有效值之后再出现 NaN 仍为 NaN（pandas 的 `min_periods` 会跳过中途 NaN，这里不跳过）；窗口没有 NaN 时与严格算子逐位一致，只影响序列开头。
- **何时用**：只在需要与外部（pandas）因子在上市初期逐值对齐时用。**新写因子默认用严格算子。**
- **为什么不默认用**：用 1～2 根数据算的均值、标准差噪声很大，`ema_relaxed` 的开头值带起点偏差；截面排名里新上市标的会从第一根就参与并容易被噪声推到极端位置。
  需要排除历史不足的标的时，优先在策略里用年龄剔除（`aged_out`）与 `min_universe`，不要靠因子本身。
- 用了宽松算子的因子，验收时"暖机新空"必须为 0，并用一个历史很短的标的单独核对序列开头。

## 4. pandas → DSL 对照

时序写法都隐含“按序列分组”，转写时去掉 `groupby("series_tag")` / `groupby(level=…)` / `transform(lambda …)` 这层外壳，只留里面的运算。

| pandas | DSL |
| --- | --- |
| `s.rolling(n).mean()` / `.sum()` / `.min()` / `.max()` / `.median()` | `ts_mean(s, n)` / `ts_sum` / `ts_min` / `ts_max` / `ts_median` |
| `s.rolling(n).std()` / `.var()`（ddof=1） | `ts_std(s, n)` / `ts_var(s, n)` |
| `s.rolling(n).skew()` / `.kurt()` | `ts_skew(s, n)` / `ts_kurt(s, n)` |
| `s.rolling(n).quantile(q)`（linear） | `ts_quantile(s, n, q)` |
| `s.rolling(n).rank(pct=True)`（method="average"） | `ts_rank(s, n)` |
| `s.rolling(n).corr(t)` / `.cov(t)` | `ts_corr(s, t, n)` / `ts_cov(s, t, n)` |
| `s.rolling(n).apply(np.argmax)` 换算成距今根数 | `ts_argmax(s, n)`（0 为当期，并列取最早） |
| `(s - s.rolling(n).mean()) / s.rolling(n).std()` | `ts_zscore(s, n)` 或照原式写 |
| `s.shift(k)` / `s.diff(k)` / `s.pct_change(k)` | `delay(s, k)` / `delta(s, k)` / `pct_change(s, k)` |
| `s.ewm(span=n, adjust=False).mean()` | `ema(s, n)`；n 很大、要精确对齐时用 `ema(s, n, k)` 固定预热根数；新上市标的需要从首根递推时用 `ema_relaxed(s, n, k)`（见第 6 节第 1 条） |
| `s.rolling(n, min_periods=m).mean()` / `.std()` / `.min()` / `.max()`，下游依赖序列开头的值 | `ts_mean_relaxed(s, n, m)` / `ts_std_relaxed` / `ts_min_relaxed` / `ts_max_relaxed` |
| `s.ewm(alpha=m/n, adjust=False).mean()`（国君 SMA） | `sma(s, n, m)` |
| `np.where(c, a, b)` | `where(c, a, b)` 或 `c ? a : b`（注意第 6 节的 NaN 差异） |
| `s.clip(lo, hi)` / `s.abs()` / `np.log(s)` / `np.sqrt(s)` / `np.sign(s)` | `clip(s, lo, hi)` / `abs(s)` / `log(s)` / `sqrt(s)` / `sign(s)` |
| `np.maximum(a, b)` / `np.minimum(a, b)` | `max(a, b)` / `min(a, b)` |
| `s.fillna(v)` / `s.isna()` | `fill_nan(s, v)` / `is_nan(s)` |
| `(c).rolling(n).sum()`（布尔计数） | `ts_count(c, n)` |
| `df.groupby(["data_time", "series_tag"])[x].rank(pct=True)` | `cs_rank(x)` |
| 截面 `(x - mean) / std`（ddof=1） | `cs_zscore(x)` |
| 截面 `x - mean` / `x / x.abs().sum()` | `cs_demean(x)` / `cs_scale(x)` |
| `params["window"]`、`params["windows"]` + `output_names` | `params: {window: {type: int, min: 2, max: 120, default: 20}}`，每个旧窗口值对应一个成员（固定参数不同的成员，或可变成员 `ma_fast`、`ma_slow`）或一个共用参数的输出，输出名固定 |

### `rolling(n).apply(内置函数)`

引擎没有"窗口加任意函数"的通用写法。apply 里传的是下面这些内置函数时，有逐值等价的写法：
注意 numpy 和 scipy 的默认参数与 pandas 不同，表里已经写进了修正。窗口参数 `n` 可以直接在表达式里参与数值运算，但作为窗口参数时必须是常量或参数本身。

| pandas / numpy 写法 | DSL | 注意 |
| --- | --- | --- |
| `apply(np.mean)` / `np.sum` / `np.min` / `np.max` / `np.median` | `ts_mean(s, n)` / `ts_sum` / `ts_min` / `ts_max` / `ts_median` | — |
| `apply(np.prod)` | `ts_product(s, n)` | — |
| `apply(np.std)` | `ts_std(s, n) * sqrt((n - 1) / n)` | numpy 默认 ddof=0，`ts_std` 是 ddof=1；直接写 `.rolling(n).std()` 才是 ddof=1，用 `ts_std(s, n)` |
| `apply(np.var)` | `ts_var(s, n) * (n - 1) / n` | 同上 |
| `apply(np.argmax)` / `apply(np.argmin)` | `n - 1 - ts_argmax(s, n)` / `n - 1 - ts_argmin(s, n)` | numpy 从窗口最老的一根算起（0 为最老），`ts_argmax` 是距今根数（0 为当期）；遇到并列，两边都取最早的一根 |
| `apply(np.ptp)`（极差） | `ts_max(s, n) - ts_min(s, n)` | — |
| `apply(lambda w: np.percentile(w, p))` | `ts_quantile(s, n, p / 100)` | 只在 numpy 默认的 linear 插值下等价；p 是 0～100，q 是 0～1 |
| `apply(lambda w: np.polyfit(np.arange(n), w, 1)[0])` | `ts_slope(s, n)` | 回归残差、R² 分别用 `ts_resi`、`ts_rsquare` |
| 线性权重 1..n 的加权平均 | `ts_decay_linear(s, n)` | 最新一根权重为 n |
| `apply(scipy.stats.skew)`（默认 `bias=True`） | `ts_skew(s, n) * (n - 2) / sqrt(n * (n - 1))` | `ts_skew` 与 pandas `.rolling(n).skew()` 一样是无偏的；scipy 默认有偏，要换算 |
| `apply(scipy.stats.kurtosis)`（默认 `bias=True`、`fisher=True`） | `(ts_kurt(s, n) * (n - 2) * (n - 3) / (n - 1) - 6) / (n + 1)` | `ts_kurt` 与 pandas `.rolling(n).kurt()` 一样是无偏超额峰度；n ≥ 4 |

这张表在合成 K 线上用 `run-python` 加 `eval`、`compare` 逐值核对过（n = 20，其中一组数据带大量并列值）：
全部一致，最大相对误差 ≤ 7e-14。scipy 的两行是按 `bias=True` 的公式（m3/m2^1.5、m4/m2^2 − 3）核对的。

**转不了的 apply**：

- **自定义 lambda 要在窗口内二次遍历**：例如平均绝对偏差 `np.abs(w - w.mean()).mean()`，要先算窗口均值，再逐根处理，表达式写不出来。
- **跳过 NaN 的函数**：例如 `np.nanmean`、`np.nanstd`。引擎是严格窗口，只有"序列开头的 NaN"能用 `_relaxed` 算子处理。
- **非线性的自定义权重**：例如 `np.average(w, weights=...)`。
- **窗口长度来自运算**：例如窗口写成 `2 * n + 1`。换算要在参数层面做，做法是拆成多个参数，例如 `ewm(com=n)` 与 `ewm(com=3n)` 换成 `ema(close, short_span)` 与 `ema(close, long_span)`，转换脚本里按 `span = 2·com + 1` 换算后写进成员参数。

遇到这些写法，按下面"不支持"的规则处理。确实常用的（如 MAD、`nanmean`），可以在 `packages/factorexpr` 里新增函数。
新增后要发布 factor-mgr 和因子引擎，并重新编译本机的 `moox-cli`。

**不支持、不能硬转的写法**：不定长窗口（`expanding()`、`cumsum()`、`cummax()`）、前向填充（`ffill`）、未来数据（`shift(-k)`）、
`ewm(adjust=True)`、`rank` 的非 average 并列方式、上面列出的那几类 `rolling().apply`、跨标的的时序运算、读取外部文件或网络。
遇到这些先告诉用户，给出等价或近似的候选写法（例如用足够长的固定窗口代替 `expanding`），由用户决定；绝不悄悄换成近似公式。

## 5. 验收规则（`compare` 的判定）

`compare` 以 (data_time, subject_id, series_tag) 对齐两份结果，每个输出列分成这几类：

| 分类 | 含义 | 判定 |
| --- | --- | --- |
| 一致 | 两边都有值，`|a−b| ≤ atol + rtol·max(|a|,|b|)`（默认 rtol 1e−9、atol 1e−12） | 必须有 |
| 双空 | 两边都是 null | 允许 |
| 暖机新空 | 原来有值、DSL 为空，且距该序列第一根 K 线不足“该输出的回看 − 1”个周期（旧的 `min_periods=1`） | 允许，这是严格窗口的预期差异 |
| 空洞新空 | 原来有值、DSL 为空，且按因子回看的窗口里缺了 K 线 | 允许，这是流水线的空洞屏蔽 |
| 其余新空 | 原来有值、DSL 为空，不属于上面两类 | 默认失败；只有逐条确认是退化窗口（分母为 0、窗口内常数等）后，才加 `--allow-new-null` |
| 旧空新值 | 原来为空、DSL 有值 | 失败，说明写法不等价 |
| 离散跳变 | 排名类列（`--discrete`）超出容差 | 不超过双方有值点的 1%（`--max-discrete-ratio`）时允许 |
| 超差 | 其余超出容差 | 失败 |

另外，两份结果里都不能有身份重复的行，比较区间（`--start`、`--end`）内的行集合必须一致，输出列名必须与原因子相同（只在用户同意改名时用 `--rename`）。
回看从 `--check`（`factor-expr.sh check` 的输出）读取：因子回看用于空洞判定，每个输出的回看用于暖机判定；没有它时用 `--lookback` 手填，所有输出共用。
方差、标准差、z 分数类在价格量级上可以放宽到 `--column-rtol 列名=1e-7`（pandas 在线算法自身的误差）。
排名类（`ts_rank`、`cs_rank` 及以它们为最外层的输出）标为 `--discrete`：pandas 的滑动和在内容相同的窗口上会差末位，打破本该存在的并列，DSL 用精确求和保住并列。

## 6. 语义差异与陷阱（转写时逐条检查）

1. **`min_periods=1`**：旧代码几乎都这么写，头几行有值；DSL 是严格窗口，前 lookback−1 行为空。
   - **只喂单个因子列**：这是允许的差异，不要为了"对上"去拆窗口。
   - **下游会用到序列开头的值**：例如截面排名、选币策略要覆盖历史短的新上市标的，或者长回看 EMA 在标的上市不满 K 根时也要有值。这时空值会改变选币结果，不再是允许的差异。
     - 改用 relaxed 算子：`ema_relaxed` 对应 ewm 从首根递推，`ts_*_relaxed(x, n, m)` 对应 `rolling(n, min_periods=m)`。
     - 比较时"暖机新空"必须为 0，并且用一个历史很短的标的单独核对序列开头。
     - 迁移整套策略的做法见 [strategy-migration.md](strategy-migration.md)。
2. **NaN 参与比较**：pandas 里 `NaN > 0` 是 False，`np.where(change > 0, a, 0)` 在 change 为 NaN（例如第一行）时取 0；DSL 的比较遇到 NaN 得 NaN，`where` 结果也是 NaN。
   通常只影响暖机期；如果用户需要旧行为，写成 `where(fill_nan(change, 0) > 0, a, 0)`，并告诉用户这是有意保留的旧语义。
3. **空洞**：旧代码按存在的行滚动，跨过缺失 K 线时窗口实际覆盖更长的时间；新系统按规则网格，流水线把回看窗口里缺 K 线的整个因子输出屏蔽掉
   （`eval` 复现了这一点）。主验收用无空洞的数据；带空洞的数据只用来展示这类差异（`synthetic --gaps N`），它们会落在“空洞新空”。
   空洞会让一致样本变少，排名类的跳变比例可能超过 1%，这不代表写法错误。
4. **公式照抄，不“修正”**：例如旧 Bias 是 `close / 均值`（没有减 1），转写时保持 `close / ts_mean(close, window)`；要改公式是另一件事，必须用户明确要求。
5. **输出列名不变**：结果列按名字落盘，改名等于新因子，历史数据接不上。`output_names` 的取值要原样写进列名模板或单独的输出里。
6. **参数默认值**：旧代码 `params.get("window", 20)` 的默认值要写成显式 `params`；目录里的 `params` 才是生产实际用的值。
7. **分母为 0**：pandas 得 ±Inf 再归一为 null，DSL 直接为 NaN，结果一样；但 `0/0` 之外的退化（如窗口内常数导致 std=0）会在 DSL 里稳定为空，归入“其后新空”时要逐条确认。
8. **`lookback_periods`**：旧目录里是手填的，DSL 以 `check` 推导的为准；两者不一致时在汇报里说明（推导值偏大说明原来手填偏小，原因子在窗口不满时其实在用不完整的数据）。
9. **截面因子**：确认原因子是按 `(data_time, series_tag)` 分组，还是只按 `data_time`；DSL 固定按 `(data_time, series_tag)`。原来跨 series_tag 混排的写法需要和用户确认。
10. **`context`**：旧 worker 传入 `period_time`（目标周期的 Unix 秒）、`frequency`、`period_times`（本次输入覆盖的周期），时序因子另有 `subject_id`，
   截面因子另有 `expected_subjects`、`available_subjects`。`run-python` 按同样的键构造（`period_time` 取最后一根，`period_times` 取输入的全部时刻，两个全集都取输入里的全部标的）。
   表达式里没有这些量：读取 `context` 改变计算方式的因子（例如按 `frequency` 换算窗口、按全集补零）要先和用户确认等价写法，不能直接转。

## 7. 转换流程（Agent 按顺序执行）

### 7.1 收集原因子

向用户要（或在仓库历史里找）：Python 源码、原目录条目（`factor_id`、`factor_type`、`input_columns`、`outputs`、`params`、`lookback_periods`）、
原来所在的因子任务（源数据集与频率）。旧库可以从 git 历史取：`git show fd8d3ca3^:modules/factor/factors/<文件>.py`、`git show fd8d3ca3^:modules/factor/factors/catalog.json`。

### 7.2 写出 DSL

在工作目录（例如 `./factor-convert/<factor_id>/`）新建 `lib.yaml`，内容与正式因子库格式相同：

```yaml
- factor_id: zf_abs_mean         # 小写 snake_case（原 zf_abs_mean）
  name: 上涨振幅均值分位          # 展示名
  spec: |
    params: {window: {type: int, min: 2, max: 120, default: 20}}
    let:
      tp: (close + high + low) / 3
      amp: (high - low) / open
    output: ts_rank(ts_mean(where(pct_change(tp) > 0, amp, 0), window), window)
```

逐条对照第 4、6 节；拿不准某个函数的细节时运行 `factor-expr.sh functions` 看说明。

### 7.3 编译检查

```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
"$SKILL_ROOT/scripts/factor-expr.sh" check --library ./factor-convert/zf_abs_mean/lib.yaml
```

必须 `"ok":true`，并核对：`outputs` 与原输出列完全相同；`input_columns` 都在源数据集里；`factor_type` 与原来一致；记下 `lookback`。
失败时按 `error.path` 与行列修改后重试。

### 7.4 准备数据

**真实 K 线优先**（每次最多 1000 根，按时间段分几次取，长度至少 lookback + 300 根，选 3 个以上标的）：

```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
"$SKILL_ROOT/scripts/data-kline.sh" --data-type crypto --symbol BTC-USDT --interval 1m \
  --start-time 2026-10-17T00:00:00Z --end-time 2026-10-17T16:00:00Z --limit 1000 --output ./factor-convert/btc-1.json
python3 "$SKILL_ROOT/scripts/factor_py_check.py" rows-to-csv ./factor-convert/*.json --output ./factor-convert/bars.csv
```

运维机上也可以用 `moox-cli data rows export --dataset <源数据集> --subject <标的> --columns open,high,low,close,volume,quote_volume ...` 导出后同样转换。
**合成数据必做一份**（覆盖收平、并列等边界，截面因子要多个标的）：

```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
python3 "$SKILL_ROOT/scripts/factor_py_check.py" synthetic --subjects 5 --bars 600 --output ./factor-convert/synthetic.csv
```

真实数据里有缺失的 K 线时，`compare` 会把空洞窗口归入“其后新空”；挑无空洞的时间段做主验收。

### 7.5 两边各算一遍

```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
W=./factor-convert/zf_abs_mean
# 原 Python 因子：用用户原来的 Python 环境（需要 pandas、numpy），参数与输入列取原目录的值
python3 "$SKILL_ROOT/scripts/factor_py_check.py" run-python --factor-file "$W/zf_abs_mean.py" \
  --params '{"window": 20}' --inputs open,high,low,close --type timeseries --freq 1m \
  --input ./factor-convert/bars.csv --output "$W/py.csv"
# DSL：--freq 写源数据的频率，缺失的 K 线才会按引擎的网格语义处理
"$SKILL_ROOT/scripts/factor-expr.sh" eval --library "$W/lib.yaml" --factor-id zf_abs_mean \
  --input ./factor-convert/bars.csv --freq 1m --output "$W/dsl.csv"
```

截面因子用 `--type cross_section`（旧契约：一次拿到全部标的，多一列 `subject_id`）。
`eval` 复现表达式计算和时序窗口的空洞屏蔽，但不模拟截面因子的全集判断：生产里严格截面因子（`allow_partial_universe: false`）遇到本期全集不完整
或有标的空洞时整期跳过，本地照常计算。所以截面因子的主验收数据要求每个时刻全部标的齐全。

### 7.6 比较

```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
W=./factor-convert/zf_abs_mean
"$SKILL_ROOT/scripts/factor-expr.sh" check --library "$W/lib.yaml" > "$W/check.jsonl"
python3 "$SKILL_ROOT/scripts/factor_py_check.py" compare --expected "$W/py.csv" --actual "$W/dsl.csv" \
  --freq 1m --check "$W/check.jsonl" --factor-id zf_abs_mean --discrete zf_abs_mean --report "$W/report.json"
```

真实数据和合成数据都要通过（退出码 0）。未通过时：

- **超差 / 旧空新值**：写法不等价，回到 7.2，用报告里的样例点定位（常见：漏了一层窗口、`ddof`、NaN 比较、公式被“修正”）。
- **离散跳变超过 1%**：先确认排名的参数（窗口、方向）；再看样例点两边的值是否只差一个名次。
- **其余新空**：逐条看样例点是不是退化窗口（分母为 0、窗口内常数）；能解释才加 `--allow-new-null`，并在汇报里写明原因。

### 7.7 向用户汇报并确认

给用户看：原 Python 关键代码与 DSL spec 并排、`check` 的派生字段（尤其 lookback 与原目录的差异）、两份数据的 `compare` 汇总表、
所有放行的差异及原因、写入方式（第 8 节）和影响（启用成员不会自动补算历史，需要历史因子值时另行提交补算）。**用户明确同意后再写入。**

## 8. 写入系统

写入的是生产因子定义，属于外部写操作，每次都要用户确认。两种方式任选：

### 方式 A：仓库因子库 + `moox-cli setup factors`（可重复执行，适合批量）

1. 把 7.2 的条目追加到 `modules/factor/factors/<库文件>.yaml`（同一文件里 `factor_id` 不能重复），再跑一次 `check` 确认整个库都能编译。
2. 在用户的 `moox.toml` 里登记定义和成员：`[factors]` 必须是 `enabled = true`（否则命令什么也不导入，却照样返回成功）；
   成员写在所属 `[[factors.tasks]]` 的 `members` 里（任务已有的不用重复写），先写 `status = "disabled"`，核对后再启用：

   ```toml
   [factors]
   enabled = true
   source_dir = "./modules/factor/factors"

   [[factors.definitions]]
   factor_id = "zf_abs_mean"
   file = "xbx.yaml"

   [[factors.tasks]]
   id = "ftask_spot_1m"
   name = "现货一分钟"
   space_id = "crypto"
   source_dataset_id = "dataset_dasftksvjhj2jom4vhd0"
   freq = "1m"
   members = [
     { factor = "zf_abs_mean", status = "disabled" },   # → 列 zf_abs_mean
   ]
   ```

3. 执行（运维机，用户的 `moox.toml`）：

   ```bash
   moox-cli setup factors --file ./moox.toml
   ```

   输出 JSON 的 `factors` 里先确认 `enabled` 为 true、`definitions` 等于登记的个数，再看每个任务与成员的结果：
   逐项报告 `exists` / `created` / `conflict`（冲突不影响其余项），并打印每个成员的成员键与列名；配置里已去掉的成员只报告 `orphan`，不自动移除。FactorMgr 会重新编译定义，编不过会整体报错。
   再次执行是幂等的：定义没变时记为 `unchanged`。
4. 核对无误后把成员改成 `status = "enabled"` 再执行一次（启用不会自动补算历史；需要历史因子值时用 `moox-factor-mgr-cli recalc` 指定范围）。
5. 仓库里的因子库改动按项目规范提交。

**同一个因子的多组参数**：定义里用 `params` 声明可调参数，一个定义可以用不同的固定参数多次加入同一个因子任务，
每个成员带自己的参数，成员键按参数生成（全默认就是因子 ID，否则“因子ID__值1_值2…”，按声明顺序写全部参数值）。
不要为了换参数新建把参数写死的定义（例如 `ema20_warmup50000`）。`moox.toml` 里的写法：

```toml
members = [
  { factor = "ema", params = { span = 20, warmup = 50000 } },   # 没给的参数取定义的默认值 → 成员键、列名 ema__20_50000
  { factor = "ma_fast", var = true },                             # 可变成员，由关联的策略逐期提交参数 → ma_fast__var
]
```

固定参数加成员之后不再改，要换参数就另加一个成员；可变成员的 `params`（可省略）是默认参数，策略还没提交过时用它。

### 方式 B：控制台

「因子计算 → 因子定义 → 新建」：填 `factor_id` 与名称，把 spec 粘进编辑器（输入时自动校验，显示“编译通过”），
在「试算」里选择目标因子任务、可选几个标的，点「试算」用生产源数据算最近几期并核对数值，最后「保存因子」。
保存后的定义不属于任何因子任务：到「因子计算 → 因子任务」里把它添加到对应任务（选固定或可变，填参数，页面预览成员键和列名）并启用。

## 9. 写入后验证

1. **定义**：控制台因子详情（或 `GetFactor`）里的类型、输入列、输出列、回看与 7.3 的 `check` 结果一致。
2. **引擎**：引擎每分钟第 45 秒同步目录；启用成员后，下一期起结果数据集出现新列。
   控制台「因子计算」总览 / `GetStatus` 里引擎在线、目录一致，`factor_compile_errors_total` 不增长（编译失败只会出现在引擎与管理端的表达式版本不一致时）。
3. **补算**：启用不会自动补算；需要历史因子值时提交补算，在「因子任务」页或 `moox-factor-mgr-cli status` 看任务从排队到完成。
4. **生产数值复核**：取一段时间的源 K 线和结果列，本地 `eval` 后与生产结果比较，两边是同一个引擎，应当逐值一致。
   时序因子取一两个标的即可；**截面因子必须导出该因子任务在这段时间的全部标的**（不带 `--subject`，或逐个导出后合并），否则本地排名、标准化的样本不同，
   结果必然对不上。源数据要比比较区间提前至少“因子回看”个周期，比较时用 `--start`、`--end` 只比较目标区间：

   ```bash
   moox-cli data rows export --file ./moox.toml --space crypto --dataset <源数据集> --subject BTC-USDT --freq 1m \
     --start-time <起> --end-time <止> --columns open,high,low,close,volume,quote_volume --output src.json
   moox-cli data rows export --file ./moox.toml --space crypto --dataset <因子结果数据集> --subject BTC-USDT --freq 1m \
     --start-time <起> --end-time <止> --columns zf_abs_mean --output prod.json
   python3 "$SKILL_ROOT/scripts/factor_py_check.py" rows-to-csv src.json --output src.csv
   python3 "$SKILL_ROOT/scripts/factor_py_check.py" rows-to-csv prod.json --output prod.csv
   "$SKILL_ROOT/scripts/factor-expr.sh" eval --library lib.yaml --factor-id zf_abs_mean --input src.csv --freq 1m --output local.csv
   python3 "$SKILL_ROOT/scripts/factor_py_check.py" compare --expected prod.csv --actual local.csv \
     --freq 1m --lookback 40 --start <比较区间起点> --end <止>
   ```

   生产结果只有比较区间内写过的行；源数据提前的部分只用来计算，`--start` 把它们排除在行集合比较之外。

## 10. 现有 11 个因子的转换（参考样例）

生产因子库 `modules/factor/factors/xbx.yaml` 就是由下列旧因子按本流程转来的；在 400 根 × 4 个标的的合成数据上 `compare` 全部通过
（zf_abs_mean 有 7 个排名并列跳变，< 1%）。

| factor_id | 旧 Python 核心（均为 `min_periods=1`，按 series_tag 分组） | DSL 输出 | lookback（window=20 时；生产按参数上限 120 推导） |
| --- | --- | --- | --- |
| bias | `close / rolling_mean(close, w)` | `close / ts_mean(close, window)` | 20 |
| bias_q | `rolling_rank(close / mean - 1, w)` | `ts_rank(close / ts_mean(close, window) - 1, window)` | 39 |
| cci | `(tp - m) / (0.015 * rolling_mean(abs(tp - m), w))` | 同式，`tp`、`m` 写在 `let` | 39 |
| min_max | `(tp - rolling_min) / (rolling_max - rolling_min) - 0.5` | 同式，`ts_min`/`ts_max` | 20 |
| quote_volume_mean | `rolling_mean(quote_volume, w)` | `ts_mean(quote_volume, window)` | 20 |
| quote_volume_mean_q | `rolling_rank(rolling_mean(quote_volume, w), w)` | `ts_rank(ts_mean(quote_volume, window), window)` | 39 |
| volume_mean_q | `rolling_rank(rolling_mean(volume, w), w)` | `ts_rank(ts_mean(volume, window), window)` | 39 |
| zf_abs_mean | `rolling_rank(rolling_mean(np.where(pct_change(tp) > 0, amp, 0), w), w)` | `ts_rank(ts_mean(where(pct_change(tp) > 0, amp, 0), window), window)` | 40 |
| zf_mean_q | `rolling_rank(rolling_mean((high - low) / open, w), w)` | `ts_rank(ts_mean(amp, window), window)` | 39 |
| zf_std | `rolling_std(np.where(pct_change(close) > 0, amp, -amp), w)` | `ts_std(where(pct_change(close) > 0, amp, -amp), window)` | 21 |
| zscore_abs_mean_q | `rolling_rank(rolling_mean(abs((close - m) / s), w), w)` | `ts_rank(ts_mean(abs((close - m) / s), window), window)` | 58 |
