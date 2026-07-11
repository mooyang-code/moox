# Strategy Python 策略接入手册（设计稿）

> 本文定义 `modules/strategy` 面向 Python 策略开发者的目标接口。Strategy 模块尚未实现，文中的目录、协议和命令是待实现契约，当前不能直接运行。
>
> 模块边界、运行链路、数据模型和一致性设计见 [Strategy 交易策略模块架构设计](策略模块架构设计.md)。

## 先看结论

策略开发者只负责策略规则，不负责数据查询、调度、状态落库、账户换算或交易执行。

一个可导入策略包必须包含 `strategy.yaml` 和 `strategy.py`。仓库内置策略还必须包含测试：

```text
modules/strategy/strategies/momentum_top_n/
├── strategy.yaml              # 必须：策略清单、输入依赖和参数约束
├── strategy.py                # 必须：策略计算函数
└── tests/
    └── test_strategy.py       # 内置策略必须；个人上传策略推荐
```

Python 只实现一个入口：

```python
def run(context, data, params, state):
    ...
```

这个函数接收已经准备好的行情与因子宽表，返回完整目标组合和下一份策略状态：

```text
StrategyInput
  -> strategy.py:run(...)
  -> decision + TargetWeights + next_state
```

Go 框架完成其余工作：

```text
Storage/View 查询
  -> Bar 完整性与未来数据检查
  -> Python worker
  -> 输出校验与运行事实落库
  -> 多策略资金聚合与硬风控
  -> 权重换算为目标数量
  -> BacktestExecution / PaperExecution / LiveTradeExecution
```

## 开发者负责什么

| 开发者负责 | Go 框架负责 |
| --- | --- |
| 声明策略需要的列和回看长度 | 从 Storage View 读取行情与因子数据 |
| 定义策略参数及参数约束 | 校验参数、列、数据范围和 Bar 完整性 |
| 选股、过滤、排名、择时和目标权重 | 调度、超时、重试、幂等和 Python worker 管理 |
| 计算策略内部状态的下一版本 | 原子保存运行结果和策略状态 |
| 编写确定性测试 | 多策略聚合、账户资金分配和硬风控 |
| 解释目标产生原因 | 权重转目标数量、模拟撮合和实盘提交 |

策略代码不得自行访问 Storage、Factor、Trade、交易所 API、网络或本地文件。策略也不得提交订单。

## 策略包

### `strategy.yaml`

`strategy.yaml` 描述策略代码本身，不描述某次回测或某个实盘账户。

```yaml
api_version: moox.strategy/v1
strategy_id: momentum_top_n
name: 动量多因子 Top N
version: 1.0.0
entrypoint: strategy.py:run
description: 按两个因子综合排名，等权持有排名最优的 N 个标的。

input:
  lookback_bars: 1
  required_columns:
    - Bias_20
    - Cci_96

params_schema:
  type: object
  additionalProperties: false
  properties:
    top_n:
      type: integer
      minimum: 1
      maximum: 100
      default: 5
  required:
    - top_n

state_schema_version: 1
```

字段规则：

| 字段 | 规则 |
| --- | --- |
| `api_version` | V1 固定为 `moox.strategy/v1`。 |
| `strategy_id` | 稳定唯一标识，只能使用小写字母、数字和下划线。 |
| `version` | 使用 SemVer。同一版本的源码和清单不可修改。 |
| `entrypoint` | 固定格式为 `<文件>:<函数>`。V1 只支持 Python 文件。 |
| `lookback_bars` | Go 为每个标的准备的最大历史 Bar 数。 |
| `required_columns` | 除基础键列外，策略运行前必须存在的数据列。 |
| `params_schema` | Go 在启动 Python 前按 JSON Schema 子集校验参数。 |
| `state_schema_version` | `next_state` 的结构版本；无状态策略仍填写 `1`。 |

Go 先应用 `params_schema` 中的 `default`，再校验 `required` 和其他约束。因此示例未传 `top_n` 时会得到 `5`；传入非法值仍会被拒绝。

以下内容属于 Go 管理的策略部署，不写入策略包：

- `space_id`、`view_id` 和 `freq`
- 标的范围、交易日历、调度周期和 offset
- 参数实际值
- 回测区间或实时触发配置
- 策略资金预算和多策略分配比例
- 账户、通道和交易模式
- 最大总敞口、净敞口、单标的权重和杠杆限制

同一个策略包可以绑定到多个数据范围、参数组合和账户。Python 无需为每个部署复制代码。

### `strategy.py`

`strategy.py` 必须提供以下函数：

```python
def run(
    context: dict,
    data: "pandas.DataFrame",
    params: dict,
    state: dict,
) -> dict:
    ...
```

V1 运行环境只保证提供 Python 标准库、`pandas` 和 `numpy`。策略不得依赖未声明的本机包。

## 输入协议

### `context`

`context` 是只读运行上下文：

```python
{
    "api_version": "moox.strategy/v1",
    "strategy_id": "momentum_top_n",
    "strategy_version": "1.0.0",
    "run_id": "strun_01...",
    "state_revision": 17,
    "trigger_bar_time": "2026-07-11T08:00:00Z",
    "trigger_bar_end": "2026-07-11T09:00:00Z",
    "decision_time": "2026-07-11T09:00:02Z",
    "data_cutoff": "2026-07-11T09:00:02Z",
    "data_revision": "view_42:00001873",
    "freq": "1h",
    "data_start": "2026-07-11T08:00:00Z",
    "data_end": "2026-07-11T08:00:00Z",
    "random_seed": 1734958331,
}
```

关键语义：

- `trigger_bar_time` 是触发本次决策的 Bar 开始时间，`trigger_bar_end` 是其右开区间结束时间。
- `decision_time` 是本次决策允许发生的时间，不早于 `trigger_bar_end`。
- `data_cutoff` 是输入数据的可见性截止时间，必须满足 `data_cutoff <= decision_time`。Go 只传入 `available_at <= data_cutoff` 的数据。
- `data_revision` 固定本次输入使用的 View、行情和因子版本。
- `state_revision` 标识本次输入基于哪一版策略状态。过期响应不能覆盖新状态。
- 同一策略、版本和逻辑调度点重试时，`run_id`、`state_revision` 与 `random_seed` 保持稳定。
- `context` 不提供 `backtest`、`paper` 或 `live` 标识。
- `context` 不提供账户、交易通道和交易所密钥。

策略不得使用 `datetime.now()`、`time.time()` 或系统时区判断交易时点。需要当前决策时间时，只使用 `context["decision_time"]`。

### `data`

`data` 是按长表组织的 `pandas.DataFrame`。每行唯一对应：

```text
(candle_begin_time, instrument_id)
```

Go 保证包含七个基础列：

| 列 | 类型 | 说明 |
| --- | --- | --- |
| `candle_begin_time` | `datetime64[ns, UTC]` | Bar 开始时间。 |
| `candle_end_time` | `datetime64[ns, UTC]` | Bar 右开区间结束时间。 |
| `available_at` | `datetime64[ns, UTC]` | 该行所有实际提供字段可见时间的最大值。 |
| `is_final` | `bool` | 是否为已闭合、可用于决策的最终 Bar。V1 只传 `true`。 |
| `instrument_id` | `string` | MooX 唯一合约身份，包含 venue、市场和必要的到期信息。策略只透传，不自行拼接。 |
| `symbol` | `string` | MooX 规范化标的，如 `BTC-USDT`。 |
| `market_type` | `string` | `spot`、`margin`、`swap` 或 `futures`。 |

`required_columns` 声明的行情和因子列会附加在同一张宽表中。Go 按 `candle_begin_time`、`instrument_id` 升序排列数据，但不会填充策略未声明的缺失值。回测使用当时的标的范围、因子版本和数据可见时间，不能用今天的完整数据反推历史决策。

策略可以这样取得当前截面：

```python
import pandas as pd

trigger_bar_time = pd.Timestamp(context["trigger_bar_time"])
latest = data.loc[data["candle_begin_time"] == trigger_bar_time].copy()
```

Python 输出的 `instrument_id`、`symbol` 和 `market_type` 必须沿用输入值。交易所代码映射由 Go 和 Trade 完成。

如果策略观察了 `[08:00, 09:00)` 的完整 Bar，最早成交时间必须满足 `execution_time >= decision_time + execution_latency`，并且市场当时可交易。任何执行器都不能使用策略已经观察过的 Bar 收盘价，也不能使用决策尚未产生时的价格。`execution_latency` 由 Go 部署配置统一管理，不传给 Python。

### `params`

`params` 是已经通过 `params_schema` 校验的只读字典：

```python
{"top_n": 5}
```

策略不得从全局配置、环境变量或文件中补充参数。这样才能保证相同输入产生相同结果。

### `state`

`state` 是上一次已接受策略运行保存的状态。首次运行和无状态策略收到空字典：

```python
{}
```

状态适合保存：

- 已选标的和入场时间
- 连续持有 Bar 数
- 卖出后的冷却计数
- 移动止损参考价
- 策略计划的分批权重阶段，只按决策推进，不按成交推进
- 上一次市场状态或信号状态

状态不适合保存：

- 历史行情或完整 DataFrame
- 账户真实仓位、余额、订单和成交
- 可从 `data` 重新计算的大型中间结果
- 文件路径、连接对象或 Python 自定义对象

`next_state` 必须能被标准 JSON 编码，默认最大为 64 KiB。禁止返回 pickle、DataFrame、NumPy 数值对象或字节串。

`next_state` 是完整状态快照，不是对旧状态的 merge patch。`strategy.yaml` 中的 `state_schema_version` 是唯一状态版本来源；状态对象内不重复保存版本。策略版本或状态版本变化时，Go 必须显式迁移或重置状态，不能静默沿用不兼容状态。

## 输出协议

`run` 必须返回一个字典：

```python
{
    "decision": "rebalance",
    "targets": targets_df,
    "next_state": next_state,
    "debug_info": {
        "message": "selected top 5",
        "metrics": {"candidate_count": 182},
    },
}
```

必填字段是 `decision`、`targets` 和 `next_state`。`debug_info` 可省略。

### `decision`

V1 支持两种决策：

| 值 | 含义 |
| --- | --- |
| `rebalance` | `targets` 是新的完整目标组合。 |
| `hold` | 不生成新目标，继续保持上一次已接受的目标组合。 |

这两个值解决“空结果”歧义：

- `decision="hold"` 且空 `targets`：保持原目标，不产生新的调仓决策。
- `decision="rebalance"` 且空 `targets`：完整目标为空，全部平仓。
- `decision="rebalance"` 且有 `targets`：未出现的旧标的目标权重变为 `0`。

`hold` 时 `targets` 必须是具有标准列的空 DataFrame。策略不得返回 `None` 表示无操作。

`hold` 不是停止执行。如果账户尚未到达上一次目标，Go 仍会让执行层继续向该目标收敛。首次运行尚无旧目标时，`hold` 等价于继续空仓。

### `TargetWeights`

`targets` 是完整的策略级目标组合 DataFrame：

| 列 | 必填 | 说明 |
| --- | --- | --- |
| `instrument_id` | 是 | 输入中的 MooX 唯一合约身份。 |
| `symbol` | 是 | 输入中的 MooX 规范化标的。 |
| `market_type` | 是 | `spot`、`margin`、`swap` 或 `futures`。 |
| `target_weight` | 是 | 相对于该策略资金预算的有符号目标权重。 |
| `score` | 否 | 策略排名或评分，仅用于审计。 |
| `reason` | 否 | 简短的人类可读原因，仅用于审计。 |

权重规则：

- `1.0` 表示使用该策略全部资金预算做多，不表示使用账户全部资金。
- `-0.2` 表示使用该策略资金预算的 20% 做空。
- `0 < sum(abs(target_weight)) < 1` 表示保留部分现金。
- Go 不会自动把权重归一化，因为自动归一化会改变策略意图。
- `spot` 的 `target_weight` 不能为负。
- V1 使用净仓模式，同一 `instrument_id` 只能出现一次。
- 所有权重必须是有限数；不允许 `NaN`、正负无穷或重复标的。
- 零权重行应省略。需要全平时返回空目标组合。
- 行顺序不影响组合语义；Go 会按 `instrument_id` 规范化排序后计算结果 hash。

Go 会在接受结果前检查允许的市场、标的范围、单标的权重、总敞口和净敞口。任何一项不合法都会使本次运行按失败处理，不会截断或偷偷修正权重。

### `next_state`

`next_state` 是当前 Bar 计算完成后的策略状态。Go 在下一次调用时将它作为 `state` 传回 Python。

```python
next_state = {
    "held": {
        "binance:spot:BTC-USDT": {
            "symbol": "BTC-USDT",
            "entry_time": context["decision_time"],
            "hold_bars": 1,
        }
    },
    "cooldowns": {},
}
```

无状态策略也必须显式返回：

```python
"next_state": {}
```

`hold` 也可以返回更新后的完整状态，例如推进冷却计数；它只是保持目标组合，不是禁止策略状态变化。

状态提交规则：

1. Python 成功返回后，Go 先校验 `decision`、`targets` 和 `next_state`。
2. `rebalance` 在一个本地事务中保存运行结果、新的完整目标、`next_state` 和执行 Outbox。
3. `hold` 只保存运行结果和 `next_state`，继续引用上一个目标，不创建新的执行 Outbox。
4. 执行失败不会回滚策略状态；Go 使用已保存目标继续恢复执行，不会再次调用 Python 推进同一 Bar。
5. Python 不得在 `next_state` 中假设订单已经成交。

这套顺序把“策略已经做出什么决定”和“账户是否已经到达目标”分开。Trade 负责后者的收敛与对账。

### `debug_info`

`debug_info` 是可选调试信息，只用于日志、排查和回测报告。它必须能被 JSON 编码，不能参与 Go 的执行决策。

```python
"debug_info": {
    "message": "not enough candidates",
    "metrics": {
        "input_rows": 12000,
        "candidate_count": 3,
    },
}
```

不要把大表、逐 Bar 序列或敏感信息放进 `debug_info`。

## 完整最小示例

下面的策略按两个因子排名，等权持有综合排名最优的 N 个现货标的。

```python
import pandas as pd


TARGET_COLUMNS = [
    "instrument_id",
    "symbol",
    "market_type",
    "target_weight",
    "score",
    "reason",
]


def empty_targets():
    return pd.DataFrame(columns=TARGET_COLUMNS)


def run(context, data, params, state):
    trigger_bar_time = pd.Timestamp(context["trigger_bar_time"])
    top_n = int(params["top_n"])

    latest = data.loc[data["candle_begin_time"] == trigger_bar_time].copy()
    latest = latest.loc[latest["market_type"] == "spot"]
    latest = latest.dropna(subset=["Bias_20", "Cci_96"])

    if len(latest) < top_n:
        return {
            "decision": "hold",
            "targets": empty_targets(),
            "next_state": state,
            "debug_info": {
                "message": "not enough candidates",
                "metrics": {"candidate_count": len(latest)},
            },
        }

    latest["score"] = (
        latest["Bias_20"].rank(ascending=False, method="min")
        + latest["Cci_96"].rank(ascending=False, method="min")
    )
    selected = latest.sort_values(
        ["score", "instrument_id"],
        kind="mergesort",
    ).head(top_n).copy()
    selected["target_weight"] = 1.0 / len(selected)
    selected["reason"] = "multi_factor_top_n"

    targets = selected[TARGET_COLUMNS].reset_index(drop=True)
    return {
        "decision": "rebalance",
        "targets": targets,
        "next_state": {},
        "debug_info": {
            "message": f"selected top {top_n}",
            "metrics": {"candidate_count": len(latest)},
        },
    }
```

这个策略不读取账户资产，不计算下单数量，也不知道自己运行在回测还是实盘环境中。

## 有状态策略示例

entry/exit、最长持仓期和冷却期属于策略规则，可以使用 `state`。下面只展示状态更新方式：

```python
def run(context, data, params, state):
    held = dict(state.get("held", {}))
    cooldowns = dict(state.get("cooldowns", {}))

    # 根据当前闭合 Bar、输入因子和旧状态更新 held/cooldowns。
    # 省略具体选股规则。

    targets = build_target_weights(held)
    return {
        "decision": "rebalance",
        "targets": targets,
        "next_state": {
            "held": held,
            "cooldowns": cooldowns,
            "last_processed_bar": context["trigger_bar_time"],
        },
    }
```

策略状态描述策略自己的选择过程。账户实际持仓仍由 Trade 管理。

## 回测与实时运行

策略开发者面对一套协议：

| 环节 | 历史回测 | 实时运行 |
| --- | --- | --- |
| 时间推进 | Go 按历史闭合 Bar 顺序回放 | Go 消费闭合 Bar/因子完成事件 |
| 输入准备 | Go 查询截至 `data_cutoff` 当时可见的历史窗口 | Go 查询截至 `data_cutoff` 的同口径窗口 |
| Python 调用 | 同一个 `strategy.py:run` | 同一个 `strategy.py:run` |
| 状态推进 | 每个 Bar 保存隔离的回测状态 | 每个 Bar 保存绑定实例状态 |
| 策略输出 | 同一个完整 `TargetWeights` | 同一个完整 `TargetWeights` |
| 最后执行 | `BacktestExecution` 模拟成交 | `PaperExecution` 或 `LiveTradeExecution` |

给定相同的 `context`、`data`、`params` 和 `state`，Python 必须返回相同结果。运行模式只能改变 Go 的最后执行端口，不能改变策略计算。

## 确定性要求

Python worker 是常驻进程，同一模块可能被多次调用。策略必须满足：

- 不使用可变模块级全局变量保存状态。
- 不读取系统时间、环境变量、网络或本地文件。
- 不依赖偶然索引或输入顺序；排名并列时必须使用唯一的 `instrument_id` 打破平局。
- 不修改传入的 `data`；需要增加列时先调用 `copy()`。
- 不使用无种子的随机数。需要随机行为时使用 `context["random_seed"]`。
- 不根据 `run_id` 或其他偶然标识偷偷分支。
- 相同输入可以安全重试。

Go 会设置执行超时和响应大小限制。超时、进程退出或协议错误都会使本次运行失败，不产生执行意图。

## 失败与重试

| 情况 | 框架行为 |
| --- | --- |
| Python 抛出异常 | 记录失败运行；不保存新目标和状态；不提交交易。 |
| 返回字段缺失或类型错误 | 视为协议错误，本次运行按失败处理。 |
| 权重、标的或敞口校验失败 | 记录校验原因；不自动修正；不提交交易。 |
| worker 超时或崩溃 | 重启 worker，使用同一输入和幂等键重试。 |
| 同一 Bar 事件重复 | 返回已经保存的运行结果，不再次推进状态。 |
| 旧 `state_revision` 的响应晚到 | 丢弃响应，不能覆盖新状态。 |
| 同一 Bar 的数据晚到或修订 | 记录新 revision，但不自动改写已接受决策；显式回放使用新的运行命名空间。 |
| Trade 提交失败 | 保留已接受目标，执行层按同一意图恢复，不重新运行策略。 |

策略希望暂时不动时返回 `hold`。无法安全计算时应抛出异常或返回 `hold`，不要猜测数据，也不要返回部分目标。

V1 每个策略绑定在一个逻辑调度点只接受一次决策。晚到数据和历史修订不会自动触发实盘重算；显式回测或补算必须固定新的 `data_revision`，并按当时可见的 revision 序列重放，不能覆盖原实盘决策。

## 测试要求

至少覆盖以下情况：

1. 正常数据产生预期的完整目标组合。
2. 数据不足时返回明确的 `hold` 或失败，不会意外清仓。
3. 空 `rebalance` 明确表示全平。
4. 相同输入重复运行得到相同输出。
5. `next_state` 能被 JSON 编码，并在下一次运行中正确恢复。
6. 缺失因子、`NaN`、重复标的和极端参数得到明确结果。
7. 输出不包含未来 Bar 或账户执行状态。

内置策略合入仓库时必须包含测试。通过管理面上传的个人策略至少要通过框架的清单校验和确定性冒烟测试。

## 计划提供的本地工具

Strategy 模块实现后应提供以下 CLI。当前仓库尚无这些命令。

```bash
# 校验清单、入口函数、参数 schema 和依赖列
go run ./cmd/cli validate \
  --strategy-dir ./strategies/momentum_top_n

# 使用 Storage View 在指定决策时间执行一次，但不提交交易
go run ./cmd/cli run-once \
  --strategy-dir ./strategies/momentum_top_n \
  --space crypto \
  --view binance_spot_factor_view \
  --freq 1h \
  --trigger-bar-time 2026-07-11T08:00:00Z \
  --decision-time 2026-07-11T09:00:02Z \
  --params '{"top_n":5}'

# 将通过校验的策略包登记到本地 Strategy 仓库
go run ./cmd/cli import \
  --strategy-dir ./strategies/momentum_top_n
```

`run-once` 只打印标准化输入摘要、目标组合、状态和诊断信息，不调用 Trade。

## 上线前检查清单

- [ ] `strategy.yaml` 使用稳定 `strategy_id` 和新的 SemVer 版本。
- [ ] `required_columns` 包含 Python 实际读取的所有行情与因子列。
- [ ] `lookback_bars` 足以覆盖最长滚动窗口。
- [ ] Python 只实现 `run(context, data, params, state)`。
- [ ] `rebalance` 返回完整目标，不是相对增量。
- [ ] `hold` 与空组合全平的语义经过测试。
- [ ] 权重按策略资金预算表达，未依赖账户权益。
- [ ] `next_state` 小于限制且可被标准 JSON 编码。
- [ ] 策略不访问时间、网络、文件、交易所或账户。
- [ ] 相同输入重复执行得到相同目标和状态。
- [ ] 回测、模拟和实盘使用同一策略版本与参数。

## 一句话边界

策略 Python 负责回答“这一刻想持有什么”；Go 和 Trade 负责回答“数据是否可用、决定如何保存、账户如何到达目标，以及失败后如何恢复”。
