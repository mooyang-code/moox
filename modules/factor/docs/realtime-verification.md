# Factor Realtime Verification

## Preconditions

- Storage 与 Factor 使用同一 EventBus，`factor_view_ready_v1` Consumer 已由拓扑初始化。
- `moox-factor` 能访问 `factors/`、SQLite 和 Storage Gateway。
- source time-series Dataset/freq 存在 enabled Factor 与 enabled Binding；Factor 显式
  声明完整 `input_columns`，不要求 OHLCV。

## Live Check

1. 启动 EventBus、Storage、Factor 与 Collector。
2. 等待 View 为 source period 发布 `ViewSourcePeriodReady`。
3. 确认 Factor 收到 ready 事件，日志依次出现 `factor_view_ready_done` 和
   `factor_task_done`；启用 CLS 的部署中这两类 info 日志会进入固定 Topic。
4. 在目标 Factor Dataset 查询两个 `data_time`，确认半开范围
   `[first, second+1ns)` 的声明输出列均已写入。
5. 查看 `GetEngineStatus`：`python_workers` 等于配置；View RPC 等待期间不增加
   `active_tasks`，周期结束后 `active_tasks` 与 `pending_tasks` 都回到 0。
6. 为同一周期配置多个 subject 和多个 Factor，确认日志中的 View read 并发不超过
   `view_read_workers`，Python 最大并发不超过 `python_workers`，且批量开启时同一
   subject 的多个 Factor 只有一次 Python batch 调用。
7. 确认相同 subject/period/lookback/trigger 的 Factor 只有一次
   `factor_view_read_done`，其 `column_count` 是输入列并集；制造一次读取超时后，日志先
   出现 `retry_position=tail`，其他 subject 仍继续读取和计算。写回/Marker 失败时应分别
   出现 `factor_task_done ... status=failed` 或 `factor_view_ready_report_failed`。

## Recovery Check

在组合任务执行中停止 Factor。该 source-ready 尚未 ACK，重启后 durable consumer 会
重投；确认所有组合终态并提交结果 Marker 后才 ACK。无法自动恢复的历史范围使用
`moox-factor-cli run-once` 或 Recalc 补算。

自动化 Factor 测试使用 embedded JetStream、`ViewSourcePeriodReady`、Factor Consumer、
TaskRunner、独立 View read pool 和 PythonWorkerPool；StorageIO fake 用于精确断言共享
读取、滑动窗口、超时队尾重试、列投影和写回。

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

Realtime 由 durable View-ready consumer 在计算和结果 marker 提交后 ACK；失败消息按
consumer policy 重投。系统仍不承诺跨服务 exactly-once；修复方式是对明确的半开范围
执行同步 `run-once`/`RecalcFactor`。
