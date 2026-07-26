# Factor Realtime Verification

## Preconditions

- Storage 与 Factor 使用同一 EventBus，`factor_calc` Consumer 已由拓扑初始化。
- `moox-factor` 能访问 `factors/`、SQLite 和 Storage Gateway。
- source Dataset/freq 存在 enabled Factor 与 enabled Binding。

## Live Check

1. 启动 EventBus、Storage、Factor 与 Collector。
2. 写入一条新的 1m K 线。
3. 确认 Factor 收到 `DatasetRowsUpserted`，日志出现 `factor_task_done`。
4. 在目标 Factor Dataset 查询同一 subject/data_time，确认目标列已写入。
5. 查看 `GetEngineStatus` 的 `queue_depth` 回到 0。
6. 制造超过 `queue_capacity` 的不同 scope，确认
   `queue_overflow_count` 增加并记录 task lost 日志。

## Recovery Check

在事件已 ACK、batch 尚未执行时停止 Factor。重启后该 task 不会自动恢复，这是预期的
best-effort 行为。使用 `moox-factor-cli run-once` 对相同半开范围补算，并确认结果写回。

自动化 `TestRealtimeEventToPythonWritebackE2E` 使用真实 embedded JetStream、
EventMessage、Factor Consumer、EventBatcher、Scheduler 和 PythonExecutor，但
StorageIO 是确定性的 fake；真实 Storage RPC 仍按以上步骤人工验证。
