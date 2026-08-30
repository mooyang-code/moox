# Strategy Python 策略接入手册（V1 历史参考）

本文仅保留旧版 `moox.strategy/v1` Python `run-once + quantity` 契约，供迁移和历史数据
解释使用，不定义当前 Strategy 的新接入方式。新策略应遵循
[MooX 选币策略执行框架设计](选币策略执行框架设计.md)，使用声明式 `Manifest`、
`ViewFactorPeriodReady` 驱动和 `target_weight` 输出；对应施工步骤见
[MooX 选币策略执行框架实施计划](superpowers/plans/2026-08-29-moox-coin-selection-strategy.md)。

> **禁止新增依赖：** 不要根据本页创建 Python entrypoint、`RunOnce`、`quantity` 或
> V1 schema。若需要兼容旧策略，必须在独立迁移方案中明确转换范围和下线时间。

## 最小策略包

一个策略包包含清单和 Python 源码：

```text
strategy.yaml
strategy.py
```

`strategy.yaml`：

```yaml
api_version: moox.strategy/v1
entrypoint: strategy.py:run
input:
  history_bars: 200
```

V1 只接受这三个清单字段：

| 字段 | 含义 |
| --- | --- |
| `api_version` | 固定为 `moox.strategy/v1` |
| `entrypoint` | `<文件>:<函数>`，例如 `strategy.py:run` |
| `input.history_bars` | 每次必须提供的完整、严格有序历史窗口行数，必须大于 0 |

清单是不可变 Strategy 制品的一部分。源码、清单或输入契约变化时使用新的
`strategy_id`，不要在原 ID 下修改内容。

`strategy.py` 只导出三参数入口：

```python
def run(context, data, params):
    return {
        "action": "hold",
        "targets": [],
        "debug_info": {},
    }
```

入口必须恰好接收 `context`、`data`、`params` 三个位置参数。Strategy 是无状态纯计算：
不接收上一轮内部状态，也不返回下一轮内部状态。

## 输入

### `context`

当前上下文只包含：

```python
{
    "strategy_id": "trend-v1",
    "runner_id": "runner-paper",
    "trigger_bar_time": "2026-07-30T08:00:00Z",
}
```

- `strategy_id`：本次执行的不可变 Strategy ID。
- `runner_id`：独立配置与调度实例。
- `trigger_bar_time`：本窗口最后一行的决策时点，使用 RFC3339。

上下文不包含账户、Secret、交易模式或上一轮目标。需要的计算记忆必须从完整历史窗口
重建，不能依赖进程内全局变量、系统时钟或文件。

### `data`

`data` 是由 Go 传入的 `pandas.DataFrame`。输入 JSON 必须：

- 是非空对象数组。
- 数组长度恰好等于 `input.history_bars`。
- 每行包含 RFC3339 字符串字段 `time`。
- `time` 严格递增。
- 最后一行 `time` 恰好等于 `trigger_bar_time`，既不能陈旧也不能包含未来行。

worker 会把 `time`、`candle_begin_time`、`candle_end_time`、`available_at` 等已存在的
时间列转换为 UTC pandas 时间。其他行情、因子和标的字段由 Runner 所配置的 View
输入决定；策略只读取提供的数据，不自行访问 Storage、Factor、Trade、交易所、网络或
本地文件。

“完整历史窗口”是清单声明并以触发时点结束的完整窗口，不是从市场起点开始的所有历史。
例如冷却期、连续信号计数和滚动指标，都应由这 200 行重新计算：

```python
def run(context, data, params):
    close = data["close"].astype(float)
    fast = close.rolling(20).mean()
    slow = close.rolling(60).mean()
    signal = fast.iloc[-1] > slow.iloc[-1]
    recent_crosses = (fast > slow).tail(params["confirm_bars"]).all()

    if not signal or not recent_crosses:
        return {"action": "hold", "targets": []}

    return {
        "action": "rebalance",
        "targets": [{
            "instrument_id": params["instrument_id"],
            "quantity": params["quantity"],
        }],
    }
```

### `params`

`params` 是 StrategyRunner 保存的 JSON 参数。策略应把它视为只读值：

```python
{
    "instrument_id": "BTC-USDT-SPOT",
    "quantity": "0.1",
    "confirm_bars": 3,
}
```

不要从环境变量、全局配置或文件补齐业务参数，否则相同输入无法复现。

## 输出

输出只能包含：

```python
{
    "action": "hold | rebalance",
    "targets": [
        {
            "instrument_id": "BTC-USDT-SPOT",
            "quantity": "0.1",
        }
    ],
    "debug_info": {},
}
```

未知字段会被拒绝。

### `action`

- `hold`：保留 Runner 的 `current_targets`，不增加 `command_sequence`，不向 Trade 发
  事件。此时 `targets` 不参与更新，建议返回空列表。
- `rebalance`：用本次 `targets` 完整替换 Runner 的理论目标。执行型 Runner 增加
  sequence，并在同一事务写入 `LogicalAccountTargetRequested` outbox；观察型 Runner
  只保存理论结果。

### `targets`

`targets` 是 FULL 快照，不是 patch：

- 每个 `instrument_id` 只能出现一次。
- `quantity` 必须是最多 256 字符的规范十进制字符串，例如 `"0"`、`"0.25"`、`"-2"`。
- `quantity` 是带符号绝对目标持仓量，不是订单量、变化量或权重。
- 遗漏旧标的表示其目标归零。
- `rebalance` 配合空 `targets` 表示全部目标归零。
- SPOT 不允许负目标；SWAP 的正负号表示方向。

Strategy 不选择执行账户或交易所原生 symbol。Trade 将一个组合总目标动态分配到
LogicalAccount（组合账户）的同质执行账户。

### `debug_info`

`debug_info` 可选，必须是可 JSON 序列化的对象，编码后最多 16 KiB。它用于解释策略
结果，不参与下一次输入和交易决策。

## 运行与持久化

Go 使用常驻 Python worker：

1. 按 Strategy `source_hash` 加载不可变源码。
2. 校验历史窗口和三参数入口。
3. 把输入转换为 DataFrame 并调用 `run(context, data, params)`。
4. 严格校验输出。
5. 计算 `input_hash` 并提交 StrategyResult。

`input_hash` 来自 Strategy 身份、namespace、完整输入、参数和触发上下文；不依赖随机
结果 ID 或运行时间。`(runner_id, strategy_id, namespace, trigger_bar_time)` 是逻辑
触发幂等键：相同键、相同输入重复提交稳定返回已有结果；相同键但不同输入是冲突。

只有校验并接受成功的运行写入 `t_strategy_results`。失败只更新 Runner 的
`last_error` 和运行日志。一次成功提交会原子更新：

- StrategyResult。
- Runner 的 `last_result_id`、`last_success_at` 和当前理论目标。
- 执行型 rebalance 对应的一条 outbox 消息。

## FULL 与组合账户

执行型 StrategyRunner 关联一个 `logical_account_id`。启用前，Strategy 与 Trade 会校验
Runner ID 和 LogicalAccount owner 双向一致。一个 LogicalAccount 最多由一个 Runner
控制，但它可以包含多个不同交易所的同质执行账户。

Trade 接收：

```text
LogicalAccountTargetRequested
  target_id = result_id
  runner_id
  logical_account_id
  command_sequence
  targets[] InstrumentTarget(instrument_id, quantity)
```

组合账户 PAUSED 时，新结果仍保存为当前目标，但不会自动下单。人工 Resume 后会
向最新目标收敛；人工逐账户清仓不删除目标，因此 Resume 可能重新开仓。

## 本地校验

校验策略包：

```bash
go run ./modules/strategy/cmd/cli strategy validate \
  modules/strategy/strategies/example/strategy.yaml \
  modules/strategy/strategies/example/strategy.py
```

使用完整历史 JSON 执行一次：

```bash
go run ./modules/strategy/cmd/cli strategy run-once \
  --data '[{"time":"2026-07-30T08:00:00Z","close":"118000"}]' \
  modules/strategy/strategies/example/strategy.yaml \
  modules/strategy/strategies/example/strategy.py
```

`--trigger` 默认取历史窗口最后一行的 `time`。

测试：

```bash
(cd modules/strategy && go test -count=1 ./...)
(cd modules/strategy && python3 -m unittest discover -s pyworker)
```

## 上线检查

- 清单只有 V1 支持字段，`history_bars` 与实际窗口完全一致。
- 入口恰好三个参数，不使用隐藏可变状态。
- 输入时间严格递增，最后一行等于触发时点。
- 输出只有 `action`、`targets`、`debug_info`。
- 数量使用规范十进制字符串，目标标的唯一。
- 明确验证 `hold`、普通 FULL、遗漏标的归零和空 FULL 清仓。
- StrategyRunner 与 LogicalAccount owner 已双向关联。
- 在 TESTNET 验证暂停、Resume 和逐账户清仓后再启用 live。
