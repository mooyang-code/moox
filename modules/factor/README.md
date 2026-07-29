# MooX Factor

Factor 是面向个人量化的单实例时序因子服务。它只持久化因子定义与数据集绑定；
实时触发和计算任务都保存在进程内，不提供持久化调度、运行历史或异步进度。

## Build And Run

```bash
./scripts/build.sh factor

# 服务端启动方式保持不变
./bin/moox-factor

./bin/moox-factor-cli init --db ./data/factor/factor.db
./bin/moox-factor-cli import \
  --db ./data/factor/factor.db \
  --factors-dir ./factors \
  --file ./factors/Bias.py \
  --factor-id bias \
  --input-columns close \
  --outputs bias_20,bias_96 \
  --params-json '{"windows":[20,96]}' \
  --lookback-rows 200

./bin/moox-factor-cli run-once \
  --config ./factor/config/app.yaml \
  --space quant \
  --dataset portfolio_nav \
  --subject fund-a \
  --freq 1m \
  --start-time 2026-07-26T00:00:00Z \
  --end-time 2026-07-27T00:00:00Z
```

## Runtime Contract

- 当前 schema 不兼容早期实验数据库；检测到旧表或旧列时会拒绝启动，请新建数据库。
- `FactorDef` 由 `factor_id/name/source_code/source_hash/input_columns/outputs/
  params_json/lookback_rows/status` 组成；`input_columns` 和 `outputs` 显式声明完整
  输入输出，框架不猜测源码依赖，也不隐式请求 OHLCV。
- `data_time` 是框架注入的 UTC 系统列，不属于 `input_columns` 或 `outputs`；整秒与
  纳秒时间均按 RFC3339Nano 往返。
- Python 入口固定为 `compute(df, params)`；`params` 是 dict，返回 key 必须与
  `outputs` 完全一致。`outputs` 创建后不可修改，变更输出需创建新的 Factor。
- `params_json` 必须是 JSON object，`lookback_rows` 是每个目标 chunk 前的输入上下文。
- 同一完整 binding scope 的实时事件在固定窗口内合并为 `[min(data_time), max(data_time) + 1ns)` 半开范围。
- 手动补算使用 `[start_time, end_time)`；超过 2000 个目标 bar 时自动分 chunk。
- `run-once` 和 `RecalcFactor` 都只执行适用于当前 source、freq、subject 的 enabled binding，
  并按 binding 的 `target_dataset` 分组写回。
- Python 因子仅实现 `compute(df, params)`，每个结果列必须为每个目标行返回有限数值或 `null`。
- `null` 会显式清除对应单元格的旧值，不影响同一行的其他因子列。
- `run-once --config /absolute/path/to/app.yaml` 复用服务配置中的 DB、Python worker、
  factors、Storage Gateway、timeout 和 retry；CLI 显式 `--db/--factors-dir` 优先。
  部署包应从干净 shell 使用 `bin/moox-factor-run-once`，由 wrapper 注入绝对路径和凭证。
- 新建和 CLI import 的 Factor 一律为 `disabled`；更新定义和再次 import 都保留现有
  状态。`SetFactorStatus` 是唯一启用/禁用入口，启用时会先完成 Storage metadata
  reconciliation，失败则保持 disabled。

```python
def compute(df, params):
    excess = df["nav"] - df["benchmark_return"]
    return {
        "excess_return": excess,
        "rolling_rank": excess.rolling(int(params["window"]), min_periods=1).rank(),
    }
```

实时 Consumer 在事件加入内存 `EventBatcher` 后 ACK。ACK 后若进程退出，尚未计算的
任务可能丢失；这是个人项目为简洁性接受的边界。使用 `run-once` 或同步
`RecalcFactor` 修复缺口。补算期间没有持久化进度；失败后可以直接重跑整个范围，
已完成的字段写回会被覆盖。

Bias 和 CCI 是 K 线模板示例，不是运行时协议。核心模块不提供 `period`、
`Depends`、`signal`、Factor DAG、持久化 inbox 或 exactly-once。
