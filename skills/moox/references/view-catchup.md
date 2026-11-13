# View / Factor 水位追平

行情或因子“停更”时先分清卡在哪一层，再用官方 CLI。不要用进程在跑代替数据在追；不要手工删 durable 或 DuckDB。

## 先量三层水位

在 **Storage 主机**看 Prometheus；View 的索引状态在操作机用 `moox-cli data view state` 查。

| 层 | 看什么 | 含义 |
|---|---|---|
| Primary | `moox_storage_dataset_output_watermark_timestamp_seconds` | 事实是否已写入。`data kline get` 走 Primary，不代表 View |
| View 索引 | `moox_storage_view_output_watermark_timestamp_seconds` | DuckDB 已提交的业务时间 |
| View 跟随 | `moox_storage_view_follow_lag_seconds` / `moox_storage_view_follow_lag_changes`；`moox-cli data view state --space-id <空间> --view-id <View>` 的 `index_id`（空表示还在建索引）、`applied_cursor`、`indexed_to` | View 是否跟上变更日志；跟上之前的完成标记才会发 `ViewDataReady`（Strategy 触发） |
| Factor 计算 | `factor_period_total{task,status}`、`factor_period_lag_seconds{task}`、`factor_last_period_time{task}` | 各因子任务是否在算、是否在写 |
| JetStream | Factor durable：`factor_collector_period_v1` pending/redelivered | 积压还是处理挂死 |

`t_views.c_mtime` 不是 K 线水位。

每个采集任务独占一个结果 Dataset/View：初始化的 Binance 现货 1m 任务写 `dataset_dasftksvjhj2jom4vhd0` 与 `view_dasftksvjhj2jom4vhd0_kline_1m`，合约 1m 任务写 `dataset_dasftksvjhj2jom4vhdg` 与 `view_dasftksvjhj2jom4vhdg_kline_1m`。每个因子任务的结果 Dataset 与默认 View（`dataset_factor_<后缀>`、`view_factor_<后缀>`）由 factor-mgr 创建与维护（Storage 不托管 View），一个源 Dataset 可以有多个因子任务。

## 生产路径（不要混用）

原生 Storage 单元的路径按部署清单解析，常见示例：

- 包根 `--package-root /data/moox/storage/current`（`moox-cli storage …` 缺省自动补上），其中包含 `start.sh` / `stop.sh` / `status.sh`。
- 业务配置 `--storage-conf /data/moox/storage/current/storage-view/config/storage.yaml`（同样缺省自动补上）；不要传 `trpc_go.yaml`。
- 每个 View 一个跟随协程读 Primary 的变更日志，没有 View 的 JetStream 消费者，也没有消费并发参数。
- 元数据库与 Pebble 都属于 `storage-primary` 组件，View 索引属于 `storage-view`。不要把旧平铺目录或操作机的路径当作当前主机路径。

控制面：`/data/moox/prod`；Storage 单元在 Storage 主机的 `/data/moox/storage/current`，不要往其他主机发 Storage。

## Storage View 容量

时序 View 建索引时，每个 `(subject_id, freq, series_tag)` 默认从 Primary 读取最近 **10000 根**，再过滤超过 `[storage_view.max_age]` 的行，实际根数可少于 10000。之后由定时任务每 2 分钟删除每个序列超出保留根数的最旧行，不删除 Primary 历史。文件达到默认 **4GiB** 只告警（Monitor 检查“View 索引文件大小”），不动数据。实际阈值以部署 policy 为准。

跟随到的实时、补采、导入与因子重算写入也受同一年龄过滤：`data_time` 严格早于“处理时刻 − 期限”时丢弃，等于截止点保留，整批超期仍按处理成功返回。默认 crypto 1m 只写近 7d、5m 近 35d；Primary 1m 默认保留 35d，两层范围不同。先看 `moox_storage_view_expired_rows_dropped_total{space,freq}` 与 `storage view 丢弃超过最长保留期的行` 日志，避免把符合策略的历史缺行判成跟随停滞；重试或重新建索引不会恢复超期行。已有索引不按年龄清理，重新建索引时才重新过滤；`retained_rows_per_series` 也不保证实际行数或历史覆盖范围。

## 禁止

- 不 dry-run 就 `--yes`；不带 `--yes` 的修改命令
- 把 EventBus token、HMAC 打进命令行、日志或聊天（Storage 维护命令不需要 EventBus 凭据，见 [`cli-operations.md`](cli-operations.md)）
- 用 `repair-view` 当常规追平
- 只重启 View 就宣布 kline 已追平（要看 `follow_lag` 回落、View 输出水位追上 Primary）
- 给首次建索引补发就绪事件。Factor 的实时入口是周期完成事件（`DatasetPeriodCompleted`），计算窗口来自因子机本地时序副本，由 PrimaryStore 增量同步；历史结果需要修正时使用显式 Recalc

## Storage View：单个 View 跟随停滞

Primary 水位前进而某个 View 长时间不动时，用 `moox-cli data view state` 看它的 `index_id` 与 `applied_cursor` 是否前进，对照该 View 的 `moox_storage_view_follow_lag_seconds` / `follow_lag_changes`（Monitor 检查 `view_follow:storage_view` 落后超过 2 分钟告警），再看 `storage-view` 日志里的索引写入错误或就绪事件发布失败。`index_id` 为空表示首次建索引或断档重建中，等它建完即可；就绪事件发不出去时该 View 停在那条完成标记上重试，先修 EventBus。

只有索引损坏、要按新的 `view_rows_per_series` 回填，或要丢弃 View 的全部内容时，才在操作机用 `moox-cli storage repair-view` 重建指定 View（经 SSH 在存储机上执行同版本程序；路径参数自动补齐），先只读预演（不带 `--yes` 就是预演）：

```bash
moox-cli storage repair-view \
  --space-id crypto \
  --view-id view_dasftksvjhj2jom4vhd0_kline_1m
```

核对目标与备份路径后，按既有授权执行对应 `--yes` 命令。它暂停 storage-view、备份元数据、清掉该 View 的活动索引指针并删除物理索引，恢复后从 Primary 重新建索引，期间 View 不可读；记录型 View 重建后为空，只跟随新写入。

## Factor 周期诊断

Factor 消费 `DatasetPeriodCompleted`，durable 名为 `factor_collector_period_v1`；
计算窗口来自本地 Pebble / 内存窗口，PrimaryStore 提供增量、变更与核对读取，不读取 View。先看该 durable 的 pending 与
redelivery，再按 `task` 查看 `factor_period_lag_seconds`、`factor_last_period_time`、
`factor_runner_backlog` 和 `factor_period_total`。检查 `factor_failures_total` 中的失败因子
与原因；`factor_compile_errors_total` 非零说明管理端与引擎的 expr 版本不一致。

| 现象 | 判断 | 动作 |
|---|---|---|
| durable pending 持续增长，最旧事件周期远早于当前时间 | 输入周期积压 | 检查 Factor 是否运行、因子任务是否 enabled、Storage Gateway 可达性和 `AckWait`；不要删除 consumer，恢复后由 JetStream 重投 |
| pending 低但 lag 或任务执行器 backlog 高 | 单个或多个因子任务处理较慢 | 对照 `factor_period_duration_seconds{task,stage}` 定位 read、compute、write 或 report 阶段 |
| `factor_failures_total` 增长 | 因子或标的数据失败 | 检查对应因子错误、输入列和上游缺失 subject；基础设施错误会重试，业务失败会进入周期终态 |
| pending 低但水位停住，`blocked_since` 非零 | 连续水位被零提交或超阈值缺失阻塞 | 看任务水位与缺失标的，先补采或确认行情源缺失，不能以 pending 低判健康 |
| 实时已追平但历史值需修正 | 历史补算 | 提交有界 Recalc 范围；已完成分块不会回滚 |


## 验收

- 1m：View `output_watermark` 与 Primary 相差分钟级；`follow_lag_seconds` 回落到 0 附近
- 1h：对齐到**已收盘**小时（与 Primary 1h 相同），不要用当前未结束小时当缺口
- Factor：每个 enabled 因子任务的 `factor_last_period_time` 接近最新已收盘周期；`factor_period_total` 持续增长且 lag 回落
- 不要只看 `storage-view` / `moox-factor-mgr` 的 pid
