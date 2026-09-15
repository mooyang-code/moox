# Factor Realtime Verification

## Preconditions

- Storage、Merge 与 Factor Engine 使用同一 EventBus。
- 时序 durable 为 `factor_dataset_rows_v1`，截面 durable 为 `factor_view_ready_v1`。
- `moox-factor-engine` 能访问 `factors/`、引擎 SQLite、Python worker 和 Storage Gateway。
- 控制面 `moox-factor` 不启动实时消费者。
- 目标 mdataset/freq 存在 enabled Factor 与 enabled Binding；Factor 显式声明完整
  `input_columns`，不要求 OHLCV。

## Live Check

1. 启动 EventBus、Storage、Collector、`moox-merge` 与 `moox-factor-engine`。
2. 等待基础 Dataset 行到达后 Merge 提交完整输入，Storage 发布 `DatasetRowsUpserted`
   （`write_kind=input_commit`）。
3. 确认时序任务完成；`factor_patch` 写回不得再次触发时序。
4. 等待关联 `MergePeriodCompleted` 的 `ViewDataReady`，确认截面任务运行。
5. 关联 `FactorPeriodComputed` 的结果 `ViewDataReady` 不得再开新截面；策略只在此时读取。
6. 在目标 mdataset 查询两个 `data_time`，确认半开范围
   `[first, second+1ns)` 的声明输出列均已写入。
7. 查看 `GetEngineStatus`：`python_workers` 等于配置；周期结束后 `active_tasks`
   与 `pending_tasks` 都回到 0。

## Recovery Check

在组合任务执行中停止引擎。该输入变更或 ViewDataReady 尚未 ACK，重启后 durable
consumer 会重投；确认所有组合终态并提交结果 Marker 后才 ACK。无法自动恢复的历史
范围使用 `moox-factor-cli run-once` 或控制面受理、引擎异步执行的 Recalc。

真实部署验收使用：

```bash
MOOX_DEPLOY_ROOT=/absolute/path/to/running/moox \
  ./scripts/test/e2e/test-factor-storage-e2e.sh
```

脚本要求 Gateway、Metadata/Primary、View、DataNode、Merge 和 Factor Engine 全部运行，
缺服务或凭证直接失败。
