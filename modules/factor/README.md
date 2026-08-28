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

# 清理历史 ViewSourcePeriodReady 积压（仅删除 durable consumer，不删除数据）
./bin/moox-factor-cli clear-queue \
  --package-root /home/ubuntu/moox/prod \
  --credential-file ~/.config/moox/eventbus/internal-admin.yaml \
  --yes
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

## 因子独立性与复合因子

系统中注册的每个 Factor 都必须是独立、无外部因子依赖的计算单元：

- Factor 只能读取当前 binding 的 Source View 中由 `input_columns` 声明的原始字段；
- Factor 不能引用、动态加载或等待另一个已注册 Factor，也不能读取其他 Factor 的结果
  Dataset；
- Factor 服务不解析因子之间的依赖关系，不提供 Factor DAG、拓扑调度、中间结果复用或
  跨因子版本协调；
- 同一批次中的多个 Factor 只是共享一次 Source View 读取和 Python 调用，计算、校验与
  写回仍彼此独立，不存在执行先后关系。

需要 MA、RSI 等基础算法的复合因子，应由业务在自己的 `compute(df, params)` 中展开完整
计算逻辑。即使相同基础算法已经作为另一个 Factor 注册，也不能直接引用其源码或结果。
系统接受由此产生的少量重复计算，以换取确定的输入快照、简单的并发模型和清晰的故障
边界。

```python
import pandas

def compute(df, params):
    close = df["close"]
    ma20 = close.rolling(20).mean()
    ma60 = close.rolling(60).mean()
    return pandas.DataFrame({
        "data_time": df["data_time"],
        "series_tag": df["series_tag"],
        "trend": (ma20 - ma60) / ma60,
    })
```

该示例应声明 `input_columns=["close"]`、`outputs=["trend"]` 和
`lookback_periods=60`。`lookback_periods` 必须覆盖复合逻辑实际需要的完整原始历史；
例如先计算 MA20，再对 MA20 做 60 周期滚动，至少需要 `20 + 60 - 1 = 79` 个周期。
Factor 服务不会从 Python 源码自动推断或补足这个值。

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

- `engine.view_read_workers` 默认 `64`，控制不同 subject 的并行 View 读取；任意读取完成
  后立即补入下一个 subject，不等待固定批次。
- `engine.view_read_timeout_ms` 默认 `10000`，控制单次 View RPC；超时任务释放读取槽位并
  移到队尾重试一次，不阻塞其他 subject。
- `engine.python_workers` 默认 `32`，控制全局 Python 计算进程和数据就绪任务并发；启动
  时只预热一个 Python 进程，其余槽位按任务需要惰性启动。
- `engine.batch_enabled` 默认 `true`，将同一 source View、frequency、period、subject
  下的多个因子合并为一次 Python 调用；结果仍按 binding 独立校验和写回。设为 `false`
  可回退到逐因子执行。

相同 subject、period、目标窗口和 trigger 的多个因子共享一次 View 读取；读取时使用组内
最大的 lookback，批量模式会在 Python 端为每个因子恢复其独立的 lookback 窗口。读取列为这些
因子输入列的并集，并在批量模式下共享一次 Python 执行。读取结果通过容量为
`2 * python_workers` 的内部队列形成背压，不增加可调批次、每 binding 并发或第三个队列
配置；不同 subject 仍可并行。

### CLS 运行日志

启用 CLS 的生产发布会在 Factor 配置中注入 `info` 级别的 CLS writer，保留本地
console writer；Topic ID 由发布前的 CLS 预检解析，不写入源码。以下记录用于定位
计算延迟、读取超时和写回失败：

- `factor_view_read_done`：一次共享 View 读取的 subject、period、attempt、耗时和列数。
- `factor_view_read_retry`：读取失败后移到队尾重试。
- `factor_task_start` / `factor_task_done`：因子任务的输入范围、输出 Dataset、状态和耗时。
- `factor_view_ready_done`：本周期全部任务与结果 Marker 已提交。
- `factor_view_ready_report_failed`：结果 Marker 上报失败，事件会保持待处理并重试。

当 Factor 输出持久化返回 SQLite 损坏错误（例如 `database disk image is malformed`）时，
服务会锁存写入故障并让 `/readyz` 返回未就绪；Monitor 的默认服务告警会触发，并将具体
数据库错误放入告警原因。修复方式是停止 Factor、删除并重新初始化 Factor 数据库，再按
现有流程重建相关 View 和结果。

日志不包含源码、凭证或请求体；可在 CLS 固定 Topic 中按 `service_name=factor` 和上述
事件名筛选。

Bias 和 CCI 是 K 线模板示例，不是运行时协议。核心模块不提供 `period`、
`Depends`、Dimensions Map、多 tag、Factor DAG、持久化 inbox 或 exactly-once。
