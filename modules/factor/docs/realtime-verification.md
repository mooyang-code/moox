# Factor Realtime Verification

## Preconditions

- Storage 与 Factor 使用同一 EventBus，`factor_calc` Consumer 已由拓扑初始化。
- `moox-factor` 能访问 `factors/`、SQLite 和 Storage Gateway。
- source time-series Dataset/freq 存在 enabled Factor 与 enabled Binding；Factor 显式
  声明完整 `input_columns`，不要求 OHLCV。

## Live Check

1. 启动 EventBus、Storage、Factor 与 Collector。
2. 向 source 写入一个含两行的 `DatasetRowsUpserted`，例如同一组合的
   `nav/benchmark_return`，时间分别为整秒和 `+1ns`。
3. 确认 Factor 收到 `DatasetRowsUpserted`，日志出现 `factor_task_done`。
4. 在目标 Factor Dataset 查询两个 `data_time`，确认半开范围
   `[first, second+1ns)` 的声明输出列均已写入。
5. 查看 `GetEngineStatus` 的 `queue_depth` 回到 0。
6. 制造超过 `queue_capacity` 的不同 scope，确认
   `queue_overflow_count` 增加并记录 task lost 日志。

## Recovery Check

在事件已 ACK、batch 尚未执行时停止 Factor。重启后该 task 不会自动恢复，这是预期的
best-effort 行为。使用 `moox-factor-cli run-once` 对相同半开范围补算，并确认结果写回。

自动化 `TestRealtimeEventToPythonWritebackE2E` 使用真实 embedded JetStream、
EventMessage、Factor Consumer、EventBatcher、Scheduler 和 PythonExecutor，但
StorageIO 是确定性的 fake，用于精确断言只请求 `nav/benchmark_return`、一次 Python
调用返回两个 output 且两行均写回。

真实部署验收使用：

```bash
MOOX_DEPLOY_ROOT=/absolute/path/to/running/moox \
  ./scripts/test-factor-storage-e2e.sh
```

脚本要求 Gateway、Metadata/Primary、View、DataNode 和 Factor 全部运行，缺服务或凭证
直接失败。`TestFactorRealStorageE2E` 只走公共 RPC 和真实 `storageio.Client`，创建临时
非 K 线 source，调用部署 `moox-factor-run-once`，再通过
`DataView.QueryTimeSeriesRows` 验证双输出、纳秒时间和重算 `null` 清旧值。
测试清理先调用 `DeleteBinding`、`DeleteFactor`，再通过 `DataNodeRuntime` 清空
source/target Dataset 的物理桶，最后 `DeleteSpace`；任一步失败都会令验收失败。

Realtime 仍是 best-effort：Consumer 在加入内存 batcher 后 ACK，进程退出可丢尚未
执行的任务。没有持久化 inbox、自动 replay 或 exactly-once；修复方式仍是对明确的
半开范围执行同步 `run-once`/`RecalcFactor`。
