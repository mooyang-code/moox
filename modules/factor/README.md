# MooX Factor

> scalar `series_tag`、单任务单 Factor 与 `lookback_periods` 已完成代码切换；
> 生产发布与真实跨模块 E2E 验收仍按
> [实施计划](../../docs/superpowers/plans/2026-07-29-factor-runtime-correctness-hardening.md)
> 执行。

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
  --lookback-periods 200

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
  params_json/lookback_periods/status` 组成；`input_columns` 和 `outputs` 显式声明完整
  输入输出，框架不猜测源码依赖，也不隐式请求 OHLCV。
- `data_time` 与 `series_tag` 是框架注入的系统列，不属于 `input_columns` 或
  `outputs`；tag 是不透明字符串，时间按 RFC3339Nano 往返。
- Python 入口固定为 `compute(df, params)`；`params` 是 dict，返回 pandas DataFrame
  必须含 `data_time`、`series_tag` 和全部 `outputs`，且行身份唯一。
- `params_json` 必须是 JSON object，`lookback_periods` 按不同 `data_time` 计数，
  不受同一时间点 tag 数量影响。
- 同一完整 binding scope 的实时事件在固定窗口内合并为 `[min(data_time), max(data_time) + 1ns)` 半开范围。
- scope 不包含 tag；任一 tag 变化会读取该时间范围的全部 tag，支持同 Dataset 跨 tag
  计算。
- 手动补算使用 `[start_time, end_time)`；超过 2000 个目标 period 时自动分 chunk，
  不会拆开同一时间点的 tag cohort。
- `run-once` 和 `RecalcFactor` 都只执行适用于当前 source、freq、subject 的 enabled binding，
  并按 binding 的 `target_dataset` 分组写回。
- Python 输出可以产生与输入不同的 tag，例如 `venue_pair:binance-okx`；数值只能是
  有限数值或 `null`。
- `null` 会显式清除对应单元格的旧值，不影响同一行的其他因子列。
- `run-once --config /absolute/path/to/app.yaml` 复用服务配置中的 DB、Python worker、
  factors、Storage Gateway 和 timeout；run-once 固定使用一个 Python worker，CLI 显式
  `--db/--factors-dir` 优先。
  部署包应从干净 shell 使用 `bin/moox-factor-run-once`，由 wrapper 注入绝对路径和凭证。
- 新建和 CLI import 的 Factor 一律为 `disabled`；更新定义和再次 import 都保留现有
  状态。`SetFactorStatus` 是唯一启用/禁用入口，启用时会先完成 Storage metadata
  reconciliation，失败则保持 disabled。

```python
import pandas

def compute(df, params):
    left = df[df["series_tag"] == params["left_tag"]].set_index("data_time")
    right = df[df["series_tag"] == params["right_tag"]].set_index("data_time")
    joined = left[["close"]].join(right[["close"]], lsuffix="_left", rsuffix="_right")
    return pandas.DataFrame({
        "data_time": joined.index,
        "series_tag": params["output_tag"],
        "spread": joined["close_left"] - joined["close_right"],
    })
```

实时 Consumer 只消费 Storage View 发出的 `ViewSourcePeriodReady`，在本周期计算和
结果 marker 提交后才 ACK。事件本身是周期完成通知，Factor 不再等待额外的 settle
窗口或重复读取。进程在执行中断时由 durable consumer 重投；个人项目不追求跨服务
exactly-once，缺口可用 `run-once` 或同步 `RecalcFactor` 修复。

常驻服务把 View 读取与 Python 计算拆成两个独立的有界并发阶段：

- `engine.view_read_workers` 默认 `60`，控制不同 subject 的并行 View 读取；任意读取完成
  后立即补入下一个 subject，不等待固定批次。
- `engine.view_read_timeout_ms` 默认 `10000`，控制单次 View RPC；超时任务释放读取槽位并
  移到队尾重试一次，不阻塞其他 subject。
- `engine.python_workers` 默认 `40`，控制全局 Python 计算进程和数据就绪任务并发；启动
  时只预热一个 Python 进程，其余槽位按任务需要惰性启动。

相同 subject、period、lookback 和 trigger 的多个因子共享一次 View 读取，读取列为这些
因子输入列的并集；任务获得 Python worker 时再按因子投影。读取结果通过容量为
`2 * python_workers` 的内部队列形成背压，不增加可调批次、每 binding 并发或第三个队列
配置。同一 subject 的多个因子可以并行，Python worker 总是领取下一个数据已就绪任务。

Bias 和 CCI 是 K 线模板示例，不是运行时协议。核心模块不提供 `period`、
`Depends`、Dimensions Map、多 tag、Factor DAG、持久化 inbox 或 exactly-once。
