# Python 因子策略迁移到 MooX

用户拿着一套 Python 写的"因子 + 选币策略"（例如截面轮动策略，带多组子策略参数），
要在 MooX 上按**完全相同的参数和语义**运行时，按本文执行。
单个因子的转写与逐值比较见 [factor-dsl.md](factor-dsl.md)，本文只写整套策略多出来的部分：拆解、能力差距、参数表、上线，以及最重要的**测试验证**。

全文的 `SKILL_ROOT` 指当前加载的 `SKILL.md` 所在目录的真实绝对路径（约定同 [data-query.md](data-query.md)），执行前先替换。
批次持仓类选币策略（每根 K 线开一批仓位、持有 h 期、多个子策略按权重合并）对应 `type: tranche` 规则，选项与止损止盈见 `docs/模块/策略.md`。

## 0. 总原则

- **语义以源码为准**：读用户提供的框架源码，逐条确认行为。不要凭记忆、框架文档或"通常的写法"推断。每条语义都记下源码文件和行号，并写明框架版本。
- **参数完全一致**：参数一律从源文件里机械地抽取，不手抄，不四舍五入，不合并"差不多"的参数组。
- **不近似、不修正**：MooX 表达不了的写法，先补系统能力（写设计文档 → 审查 → 编码），不要悄悄换成近似公式；原公式看起来有问题时，照原样迁移，并告诉用户。
- **用户的策略与因子不进仓库**：用户的策略文件、参数 CSV、转写出的因子定义、生成的 DSL、转换与对比脚本、迁移记录都放在工作树里的 `local/<名称>/`，并加到 `.git/info/exclude`。只有通用的系统能力（新算子、新规则选项）进仓库，代码和文档里不出现用户策略或因子的名字。
- **先观察、后交易**：迁移后的策略先挂成**不挂账户的观察实例**，验收通过并经用户确认后，才关联账户。
- **对生产只读**：验收只读日志、只读打开数据库。要改生产配置或二进制，按 [native-release.md](native-release.md) 执行，并先征得用户同意。

## 1. 流程总览

| 步骤 | 做什么 | 产物 | 通过条件 |
| --- | --- | --- | --- |
| 1. 拆解 | 读源码，列出原子组件和语义清单 | 组件表、语义清单（附源码行号） | 每个组件都能指到源码 |
| 2. 能力差距 | 逐条对照 MooX 现有能力 | 差距表；有差距时写设计文档和执行计划 | 差距为 0，或设计已经审查通过 |
| 3. 转换 | 写转换脚本，生成因子定义、因子任务成员和策略 DSL | `local/<名称>/out/` | 三类核对全部通过（4.3 节） |
| 4. 验证 | 七层验收，见第 5 节 | 验收记录 | 第 5 节每层的通过条件 |
| 5. 上线 | 发布 → 因子任务 → 策略定义 → 观察实例分批启用 | 上线记录 | 首期结果符合预期 |

写代码、上线之前，先把设计文档和执行计划交给用户（必要时交给 Codex 审查）。大改动不要直接动手。

## 2. 拆解源策略

列出下面这些组件，每项都附上源码位置：

| 组件 | 要抽取的内容 | 常见形态 |
| --- | --- | --- |
| 左因子（选币因子） | 函数体、参数、窗口、`min_periods`、ewm 的 `adjust` 与起点 | 两条 EMA 之比（`com` 写法）、动量均值乘波动 |
| 右因子（过滤因子） | 同上 | 影线占比的滚动最大、区间位置 |
| 前置过滤 | 条件、比较方式、多条之间的组合顺序 | 串联执行，`rank` / `pct` 在上一步留下的样本上算 |
| 后置过滤 | 在哪个样本上判断，剔除后补不补位 | 在选中样本上一次判断，任一条件成立就剔除，不补位 |
| 子策略参数表 | 每一行的全部参数与权重 | (h, n1, n2, p2, w) |
| 写死的配置 | 多空权重、个数、名单、上市年龄门槛、计算方式 | `long_weight=2`、`long_coin_num=1` |
| 组合方式 | 多个子策略怎么合并 | 按 `stg_weight` 加权平均 |

**语义清单**：下面这些问题必须逐条回答，并写明源码行号。

1. **候选样本**：哪些行参与排名（例如只要 `volume > 0`）？缺失周期怎么补（例如 close 沿用上一根、量类填 0）？
2. **排名**：截面还是时序？并列怎么处理（`average` / `first`）？NaN 用什么补（例如同期中位数名次）？多空因子不同的时候，是不是分两遍算？分数是加权求和还是加权平均？
3. **选币个数**：整数和百分比各怎么处理？百分比的分母是什么？资金分母是配置的个数，还是本期选中数？
4. **比较运算**：开区间还是闭区间？无穷边界怎么写？取反类运算遇到 NaN 得什么？
5. **持有**：持有期按什么计数（按该标的的候选行，还是按时间）？中途没有行的期次算不算？
6. **权重**：每期新开一份、按开仓时的数量持有，还是按当期重新配权？输出截断到什么精度？
7. **起点**：回测是否从起点往前回退若干根？这会影响和实时运行做对比时，两边是否处在同一状态（5.4 节）。
8. **框架里没有被策略用到的写法**：用户以后可能会用（例如逻辑组合 `1&(2|3)`）。确认它在哪个入口生效：有的框架只在工具函数里支持，回测类本身不支持。

## 3. 能力差距与映射

| 源策略 | MooX | 注意 |
| --- | --- | --- |
| 因子函数 | 因子定义（表达式，[factor-dsl.md](factor-dsl.md)），用户自己的因子不进仓库的因子库，直接写入系统 | `min_periods`、ewm 从首根递推会影响新上市标的的选币，要用 relaxed 算子（`ema_relaxed`、`ts_*_relaxed`），见 factor-dsl.md |
| 因子参数的不同取值 | 同一定义在因子任务里的多个**固定参数成员**，成员键为 `因子ID__值1_值2` | 参数需要换算时（例如 `com=n` 换成 `span=2n+1`），换算只写在转换脚本里，并在 DSL 头部注释写明 |
| 长回看（EMA 一类） | `ema(x, n, k)` / `ema_relaxed(x, n, k)` 固定预热根数 | 受三道限制：Storage 保留期、因子机 `engine.yaml` 的 `window.lookback` 和 `compute.max_memory`，三者都要装得下最长的回看。预热根数是用户的决定 |
| 一个子策略 | `type: tranche` 规则的一行 `params` | 一条规则最多 5000 行，整个策略合计最多 10000 个子策略；含参数表的 DSL 上限是 1 MiB |
| 子策略模板 | `tranche:` 一节，用 `{列名}` 占位 | 选项（`hold_count`、`score`、`sizing`、`candidates.require_volume`）都要显式写出与原框架一致的取值，见 `docs/模块/策略.md` 的"tranche 规则"；同类策略可以做成策略模板（"策略模板"一节） |
| 加权合并 | `stg_weight` 归一化后乘以规则预算 | `portfolio.leverage` 要留出余量：按数量持有会让 Σw 略大于 1 |
| 上市年龄门槛 | `universe.min_age_bars` | 例如"去掉每个标的前 999 根"写成 `min_age_bars: 999` |
| 实时运行 | 策略实例（关联因子任务；观察期不挂账户） | tranche 规则只能绑定合约 View，不支持回放 |

**先查能不能配置出来**：tranche 规则的选项和 `stop` 止损止盈覆盖了批次持仓类策略的常见变体，先对照 `docs/模块/策略.md` 的选项表；只有确实缺一种机制时才编码，而且只给已有环节加选项。

**有差距时**：先写设计文档，每条语义都对照源码行号；再写执行计划，按阶段排、每个任务给出验收条件；交给审查（例如 Codex），把处置意见记进文档，然后才编码。

## 4. 转换

### 4.1 位置与产物

```
local/<名称>/                     # 加入 .git/info/exclude，不进仓库
  convert.py                       # 只用 ast 读源文件，不执行用户代码
  factors.yaml                     # 转写出的因子定义（用 import 写入系统，不进仓库的因子库）
  out/<策略>.yaml                  # 策略 DSL（含 params 参数表）
  out/moox_fragment.toml           # 因子任务成员片段
  apply_moox_toml.sh               # 把片段合入用户的 moox.toml（演练 / --apply，写前备份，不打印配置内容）
```

用户的 `moox.toml` 是加密文件。按 [install.md](install.md) 的规则，Agent 不读取、不打印它的内容，只通过 CLI 写入片段。

### 4.2 转换脚本的要求

- 用 `ast` 解析源文件，读出常量和参数表，**不 `import`、不执行**用户代码。
- 参数的换算（例如 `span = 2·n + 1`）集中写在一处，并写进生成文件的头部注释。
- 输出的 DSL 用 `{列名}` 模板加 `params` 表，不要展开成几千条规则；tranche 的选项全部显式写出。
- 因子任务的成员由参数表里实际用到的取值去重得到。成员键必须与 DSL 里引用的列名一一对应。

### 4.3 三类核对（转换脚本自带 `--check`，上线后再对系统读回的 DSL 跑一次）

1. **参数表逐字段一致**：DSL 的每一行与源文件的每个子策略一一对应，数值完全相等（权重比较 `repr`，不做容差比较）；子策略总数相等；Σ`stg_weight` 与源文件一致。
2. **写死的配置一致**：多空权重、个数、名单、年龄门槛、计算方式、过滤条件的方向、运算符和类型，都与源文件相同。
3. **引用列与成员一致**：DSL 引用的每个列名都对应因子任务里的一个成员，不多也不少。

## 5. 测试验证（逐层通过才能进入下一层）

每层都写进验收记录，内容包括：方法、数据范围、结果数字、是否通过。**做不到的层要写明原因和替代方法，不能写成"通过"。**

### 5.1 黄金用例（编码阶段，只在补系统能力时需要）

- **期望值必须由原框架实际运行得到**，不能手算：手算的 `rank(method='first')` 期望值很容易写错，用 pandas 跑一遍才发现。
- 每种写法至少一个用例：多空、个数与百分比、每种过滤运算与类型、前后置过滤、并列、NaN、`volume = 0`、多空权重不同的激活组合、逻辑组合、多子策略合并，以及持有期到期。
- 所有用例共用一份 K 线（`bars.json`）以控制体积，只含合成数据与合成参数。误差不超过 1e-12。
- 生成脚本放在 `local/`，用例数据进仓库（`modules/strategy/internal/engine/testdata/reference/`）。在仓库根目录用 `go test ./modules/strategy/internal/engine/ -run Tranche` 运行。

### 5.2 因子一致

- 按 [factor-dsl.md](factor-dsl.md) 第 7 节的流程，用 `factor_py_check.py run-python` 跑原因子，用 `factor-expr.sh eval` 试算 DSL，再用 `compare` 比较。
- **数据要覆盖两种标的**：一个历史很长的（不少于预热根数），一个上市不久的（只有几千根），后者用来核对序列开头的部分窗口。
- 用了 relaxed 算子时，"暖机新空"必须为 0。
- 长回看截断（例如 EMA 只预热 5 万根）要单独评估：分别在全历史和截断窗口上算一遍，统计每期截面 top1 的一致率。
- **原因子不是 `compute(df, params, context)` 契约时**（例如 `signal(df, n, diff, name)`），用一个包装文件适配，再交给 `run-python`：

  ```python
  # local/<名称>/wrap_factor.py：把原框架的因子函数包成 run-python 的旧契约（时序因子，逐标的调用）
  import importlib.util
  spec = importlib.util.spec_from_file_location("orig", "/path/to/原框架/factors/<因子>.py")
  orig = importlib.util.module_from_spec(spec)
  spec.loader.exec_module(orig)

  def compute(df, params, context):
      out = orig.signal(df.copy(), int(params["n"]), 0, "v")   # 列名 v 只是临时名
      return df[["data_time", "series_tag"]].assign(my_factor=out["v"].values)
  ```

  ```bash
  SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
  python3 "$SKILL_ROOT/scripts/factor_py_check.py" run-python --factor-file local/<名称>/wrap_factor.py \
    --params '{"n": 24}' --freq 5m --inputs close,high,low --input bars.csv --output py.csv
  # DSL 一侧用换算后的参数（例如 n=24 → short_span=49、long_span=145），再按 check 推导的回看逐值比较
  "$SKILL_ROOT/scripts/factor-expr.sh" eval --library local/<名称>/factors.yaml --factor-id my_factor \
    --param my_factor.short_span=49,my_factor.long_span=145 --input bars.csv --freq 5m --output dsl.csv
  "$SKILL_ROOT/scripts/factor-expr.sh" check --library local/<名称>/factors.yaml --factor-id my_factor > check.jsonl
  python3 "$SKILL_ROOT/scripts/factor_py_check.py" compare --expected py.csv --actual dsl.csv --freq 5m --check check.jsonl --factor-id my_factor
  ```

- **上线后**：最好从结果 View 读出因子列，与原版逐行对比（相对误差 ≤ 1e-10）。读 View 需要调用方身份，**不要借用服务身份**。拿不到时，以 5.4 节"权重逐期一致"替代，并在记录里写明原因和精度边界。

### 5.3 参数一致（上线后）

从系统读回策略定义的 DSL（控制台，或 `moox-cli strategy show --strategy-id <ID>`），用转换脚本的 `--check` 对源文件重跑 4.3 节的三类核对。
DSL 要逐字节和提交的内容一致；DSL 超过 64 KiB 时，同时验证了 1 MiB 上限是通的。

### 5.4 语义一致（重点）

在同一份真实数据上，用原框架算出每期的目标权重，与观察实例逐期比对。

**两边必须处在同一状态**，否则差异无法归因：

1. **同一根起点、同为空状态**：从实例会话的第一个 ok 结果的 bar 开始。找出原框架里的"起点回退"（例如回测把开始时间往前推一个持有期）并在副本里去掉，保证两边都从没有任何开仓记录开始。
2. **同口径的输入**：原框架的输入，用历史导入包，拼接交易所公开接口拉到的近期 K 线，再按 MooX 的方式补齐缺失周期（close 沿用、量类填 0）。因子列由原版因子函数在全部历史上计算。
3. **同一精度**：原框架的权重在比对前，按 MooX 的输出精度向零截断（`--trunc 1e-4`，按十进制截断）。
4. **时间对齐**：MooX 的 `bar_end` 是 K 线收盘；原框架如果用开盘时间标 K 线，bar_end = 开盘时间 + 周期。
5. **窗口长度**：至少覆盖最长持有期，实际做到 50 期以上。

**参考运行器**（放在 `local/`）：对原框架的**副本**打必要的补丁后运行，常见的有三类——第三方库版本兼容（例如 pandas 2 的
`Series.between(inclusive=...)` 只接受字符串）、关掉计时与日志输出、去掉上面第 1 条的"起点回退"。因子列预先用原版因子函数算好，
每个子策略分别跑出持仓，按 `stg_weight` 加权合并后写成 `bar_end,instrument_id,weight`（instrument_id 用 MooX 的 `BTC-USDT` 写法，不截断）。

**比对**（`strategy_check.py`，只读）：

```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
python3 -I "$SKILL_ROOT/scripts/strategy_check.py" targets \
  --instance <策略名>=<实例ID> --output out/targets.json
python3 -I "$SKILL_ROOT/scripts/strategy_check.py" compare --targets out/targets.json --name <策略名> \
  --reference out/ref_<策略名>.csv --since <首个 ok 的 bar_end> --until <参考数据覆盖到的最后一个 bar_end> --trunc 1e-4 --table
```

`compare` 的退出码：0 表示通过；1 表示有期次不一致（或 MooX 缺期）；2 表示窗口里没有持仓或权重从未变化，开仓和到期路径没被检验到。

**通过条件**：

- 每期持仓集合相同，权重差为 0（或在截断精度内）；
- 有持仓的期占多数，并且权重逐期有变化（`compare` 输出的"权重变化"不能为 0）。批次持仓策略每根 K 线都新开一份仓、旧仓按 h 到期，权重变化本身就检验了开仓和到期两条路径；
- 最好也出现减仓（"持仓数减少"大于 0），这样能检验到"整个币退出"的路径。窗口里一次都没有减仓时，在记录里写明，必要时延长窗口；`compare` 会打印提示，但不因此判失败；
- 写出**精度边界**：最小子策略占比 ÷ 最长 h，与截断粒度相比。例如 5e-4 ÷ 36 ≈ 1.4e-5，小于 1e-4，单个子策略偶尔选错可能被截断吸收。结论只能写"在 1e-4 精度内一致"，不能写"每个 top1 都一致"。
- 系统暂停恢复（追赶）之后的若干期也要比对：追赶期会求值并推进规则状态，放行后应与连续运行一致。

### 5.5 端到端耗时

```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
python3 -I "$SKILL_ROOT/scripts/strategy_check.py" latency --factor-host <因子机 SSH 目标> \
  --since <首个 ok 的 bar_end> --pair <策略名>=<实例ID>:<因子任务ID> --pair <现有策略>=<现有实例ID>:<现有任务ID> [--table]
```

- **分段**（以 K 线收盘为 0）：
  - 收盘 → 因子开算：采集写入加 Storage 提交；
  - 因子：load、compute、写出（assemble、write、report），以及因子合计；
  - 因子完成 → 就绪；
  - 就绪等待：从 View 就绪到本实例开始处理，包括排队；
  - 读取、求值；
  - 总计。

  各段含义以 `modules/strategy/internal/trigger/handler.go` 的 `logTiming` 和 `modules/factor/internal/engine/runner.go` 的 `factor_period_done` 为准。
- **周期长度**：`--bar-minutes` 默认是 5。迁移 1m、15m 等其他周期的策略时必须传入，否则会按错误的 period 关联日志。
- **冷启动与稳态分开**：冷启动指启用后前 3 期；稳态至少连续 48 期（5m 周期约 4 小时）。各段都报告 p50 / p95。
- **先核对时钟**：跨主机做差之前，在每台主机上看 `chronyc tracking`。偏差在 50 ms 以内才能跨主机做差，否则只用同一台主机的时间戳。
- **"因子完成 → 就绪"是 -0.05 秒左右**：这是正常的。策略在因子的 report 阶段就收到了就绪，而 `factor_period_done` 写在 report 之后，所以这一段约等于 0，不是时钟偏差。
- **与预估对照**：上线前按因子成本估算每个策略的耗时，上线后对照。超出时，指出超出的部分在哪一段。

### 5.6 对现有链路的影响

新任务和现有任务会在同一时刻开算，新实例和现有实例会在同一时刻读取数据，所以必须对照：

```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
# 1m 任务：收盘落在 5 分钟边界的那一分钟（与新任务同时开算）单独一组；上线前、上线后各跑一次
python3 -I "$SKILL_ROOT/scripts/strategy_check.py" task-lag --factor-host <因子机> --bar-minutes 1 \
  --task ftask_swap_1m --task ftask_spot_1m --since <开始> --until <结束> [--exclude <开始>,<结束>]
```

- 现有 5m 实例：用 `latency` 在上线前、上线后两个窗口各跑一次，逐段对比。
- **`--exclude` 只用来排除查明原因的事故窗口**，并在记录里写明原因。
- **变慢了先找证据再归因**：要有服务端耗时、抓包或 pprof 这样的逐跳证据。曾经有一次读取从 0.2 秒涨到 1～2 秒，起初推断是"多个实例同时读、互相争用"；逐跳排查后发现策略本来就是串行读取的，真正的原因是存储机与控制机之间的跨地域公网在突发流量后丢包、TCP 重传超时不断翻倍。没有证据的推断，只能写成"推测"。

### 5.7 稳定性（24 小时）

```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
python3 -I "$SKILL_ROOT/scripts/strategy_check.py" summary --instance <策略名>=<实例ID>
```

- **结果状态**：从启用起连续 24 小时没有 `config_error`；每个 skipped 的 `skip_reason`（`expired` 等）都要说得出原因。
- **追赶与迟到周期**：当前实现对所有规则类型在输入就绪且成功求值时推进状态，即使结果记为 `skipped(catchup / expired)`；这些周期没有交易目标、逐标的解释明细或下一期因子参数。核对 `c_rule_states_json` 是否非空及后续周期是否正确延续，不要把所有 skipped 都当成状态未变。与连续运行比较时要保证周期按序送达且成功求值；暂停时已忽略的事件、守门跳过和缺期不会自动补齐，理论状态一致也不代表实盘成交一致。
- **规则状态体积**：在达到最长持有期之后不再增长（"最近 N 期波动"很小）；超过 256 KB 的期次要逐个说明原因。
- **24 小时还没满时**：如实写"进行中"，注明起点和复核命令，不要提前宣布通过。

### 5.8 向用户汇报

- 控制台上看执行结果的入口：`/strategy/tasks` 的"结果"页签，或实例详情 `/strategy/detail/<实例ID>`。
- 每层的结论和数字。表格包括：子策略数、比对期数、一致期数、最大差；稳态耗时 p50 / p95；对现有链路的影响。
- 没做到的层、精度边界、还在进行中的验收。
- 验收过程中对生产造成的任何影响，如实写出原因、范围和恢复时间。
- 上线时的手工改动，例如因子机 `engine.yaml` 里的 `compute.workers`：重装单元会被覆盖，需要补回。

## 6. 上线

按 [native-release.md](native-release.md) 和项目里的执行计划执行。顺序如下：

1. **因子侧先于策略**：factor-mgr、因子引擎。只换二进制；因子机的 `engine.yaml` 不重装。发布后确认现有任务每期都正常。
2. **strategy 与 web-host**：发布后确认现有实例仍然每期都是 ok。
3. **写入因子**：因子定义用 `moox-factor-mgr-cli import --file local/<名称>/factors.yaml` 或管理台写入；因子任务成员用 `apply_moox_toml.sh` 先演练、再 `--apply`，然后执行 `moox-cli setup factors`。新任务要复用因子机上已有的预热数据，不要另导一份。
4. **建策略**：建策略定义（或用策略模板渲染）；读回 DSL 核对（5.3 节）；建观察实例，关联各自的因子任务，不挂账户。
5. **分批启用**：先启用最便宜的那个，观察 1 小时内引擎内存、单期耗时，以及现有任务的耗时，再启用下一个。
6. **首期检查**：子策略数、签名数、选中币数符合预期；状态在 256 KB 以内。
7. **撤回**：先停用实例，再停用任务（不能删除）。二进制用备份回滚。

已在运行的因子要改定义（例如换算子名）时：被已启用成员引用的定义不能改，先停用这些成员，再 `UpdateFactor`，重新启用并等预热完成；
定义哈希变了的因子，引用它的策略实例要重新启用。完整流程、兼容限制（不能增删改参数与输出）和“新建因子并行切换”的替代做法见
[factor-dsl.md](factor-dsl.md) 的“修改已有因子的定义”。

## 7. 数据获取与操作安全

- Skill 的 K 线查询（[data-query.md](data-query.md)）只能读取打包目录里已登记的市场与频率；当前默认目录的 crypto/binance 现货含 1m、1h，stockcn/cn_a_share 含 1m，不包含本例的 5m 合约数据。按实际目录检查所需频率与市场，不能把 1m 数据当成 5m。目录不能满足时，参考数据用两部分拼起来：
  - 用户的历史导入包（[initial-import.md](initial-import.md) 用的那份 CSV）；
  - 交易所公开行情接口拉到的近期 K 线（不需要凭据）。
- **不要在生产采集机上大批量下载，也不要从它往外整包传输**：海外机（collector-stream 所在）的上行带宽很小。2026-10-26 一次 81 MB 的 rsync，让采集写入 Storage 和币安 WS 接连超时，1m 因子最多晚了 8 分钟。
  - 必须在这台机器上拉时，只拉增量，压缩后用 `rsync --bwlimit=200` 限速传输。
  - REST 请求控制在交易所的权重上限以内（币安合约每分钟 2400），每请求一次停 0.25 秒。
- 长时间的远程任务用 `nohup … &` 放到后台，再轮询它的日志，不要依赖一条长 SSH 连接。
- 结果 View 和 Storage 节点都不能被验收脚本直接读取：View 需要调用方身份；Pebble 被运行中的进程占用，不能只读打开。策略库（SQLite）可以只读打开，`strategy_check.py` 用的就是这条路径。

## 8. 陷阱清单（实际遇到过的）

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 长 EMA 的因子任务每期 compute 37～48 秒 | 执行器按"标的数 × 输出行数"判断是否并行，实时只输出 1 行，所以永远单线程；`compute.workers` 默认只有 4 | 已修复为按窗口长度判断；因子机 `engine.yaml` 手工设了 `compute.workers: 32`，重装单元前要确认 |
| DSL 提交被拒（超过 64 KiB） | 参数表很大 | 含参数表的 DSL 上限已放宽到 1 MiB |
| 多空权重不同的子策略漏开仓 | 排名签名里没有包含"哪一侧权重大于 0" | 签名已加入激活位；用例 `mixed_side_weights` |
| 参数里的 NaN、Inf 绕过了校验 | 校验时只比较了大小 | 校验已拒绝非有限值 |
| `min_universe` 把停牌的币算了进去 | `volume = 0` 的行被当成候选 | 改为 inactive 分类，明细里记 `volume_zero` |
| 原框架在新版 pandas 下报错 | `between(inclusive=bool)` | 在副本里打兼容补丁（5.4 节） |
| 原框架回测类不接受逻辑组合 | 只有工具函数实现了逻辑组合 | 这类黄金用例改用工具函数生成 |
| 新上市标的的因子和原版不一致 | 原版 `min_periods`、ewm 从首根递推，DSL 是严格窗口 | 改用 relaxed 算子（factor-dsl.md） |
| 系统暂停恢复后权重与原框架分歧约一个持有期 | 追赶期不求值，开仓记录与持有计数缺失 | 已修复：追赶期与迟到周期也求值、只推进状态 |
| 后台 SSH 任务退出码 255 | 网络抖动导致长连接断开 | 改用 nohup 放到后台，再轮询它的日志 |
| 验收期间采集出现延迟 | 从采集机整包 rsync | 第 7 节 |
| 把读取变慢归因成"互相争用" | 没有逐跳证据就下结论 | 5.6 节 |
