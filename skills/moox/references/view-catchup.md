# View / Factor 水位追平

行情或因子“停更”时先分清卡在哪一层，再用官方 CLI。不要用进程在跑代替数据在追；不要手工删 durable 或 DuckDB。

## 先量三层水位

在 **Storage 主机**看 Prometheus 和 metadata SQLite（南京机通常没有 `sqlite3` 命令，用 `python3` + `sqlite3` 只读打开）。

| 层 | 看什么 | 含义 |
|---|---|---|
| Primary | `moox_storage_dataset_output_watermark_timestamp_seconds` | 事实是否已写入。`data kline get` 走 Primary，不代表 View |
| View 索引 | `moox_storage_view_output_watermark_timestamp_seconds` | DuckDB 已提交的业务时间 |
| View period 表 | `t_view_period_dataset_states` 的 `MAX(c_period_time)` / `c_updated_at` | View 就绪 / Strategy 触发水位；Factor 直接订阅 Collector 周期事件 |
| Factor 计算 | `factor_period_total{task,status}`、`factor_period_lag_seconds{task}`、`factor_last_period_time{task}` | 各因子任务是否在算、是否在写 |
| JetStream | View：`moox_storage_view_consumer_partition_lag_messages` + `oldest_pending_event_age_seconds`；Factor durable：`factor_collector_period_v1` pending/redelivered | 积压还是处理挂死 |

`c_period_time` 是 unix 秒。`t_views.c_mtime` 不是 K 线水位。

每个采集任务独占一个结果 Dataset/View：初始化的 Binance 现货 1m 任务写 `dataset_dasftksvjhj2jom4vhd0` 与 `view_dasftksvjhj2jom4vhd0_kline_1m`，合约 1m 任务写 `dataset_dasftksvjhj2jom4vhdg` 与 `view_dasftksvjhj2jom4vhdg_kline_1m`。每个因子任务的结果 Dataset 与默认 View（`dataset_factor_<后缀>`、`view_factor_<后缀>`）由 factor-mgr 创建与维护（Storage 不托管 View），一个源 Dataset 可以有多个因子任务。

## 生产路径（不要混用）

原生 Storage 单元的路径按部署清单解析，常见示例：

- 包根 `--package-root /data/moox/storage/current`（`moox-cli storage …` 缺省自动补上），其中包含 `start.sh` / `stop.sh` / `status.sh`。
- 业务配置 `--storage-conf /data/moox/storage/current/storage-view/config/storage.yaml`（同样缺省自动补上）；不要传 `trpc_go.yaml`。
- View 的消费者是按数据集动态创建的 `view_<12 位十六进制>`（每个数据集一个）；并发参数是 View 的 `storage.view.fetch_batch` / `max_workers` / `max_ack_pending`（全局，默认都是 1）
- 元数据库与 Pebble 分别属于 `storage-primary` 与 `storage-node` 组件。不要把旧平铺目录或操作机的路径当作当前主机路径。

控制面：`/data/moox/prod`；Storage 单元在 Storage 主机的 `/data/moox/storage/current`，不要往其他主机发 Storage。

## Storage View 容量

时序 View 建索引时，每个 `(subject_id, freq, series_tag)` 默认从 Primary 读取最近 **10000 根**，再过滤超过 `[storage_view.max_age]` 的行，实际根数可少于 10000。之后由定时任务每 2 分钟删除每个序列超出保留根数的最旧行，不删除 Primary 历史。文件达到默认 **4GiB** 只告警（Monitor 检查“View 索引文件大小”），不动数据。实际阈值以部署 policy 为准。

实时、补采、导入与因子重算写入也受同一年龄过滤：`data_time` 严格早于“处理时刻 − 期限”时丢弃，等于截止点保留，整批超期仍按处理成功返回。默认 crypto 1m 只写近 7d、5m 近 35d；Primary 1m 默认保留 35d，两层范围不同。先看 `moox_storage_view_expired_rows_dropped_total{space,freq}` 与 `storage view 丢弃超过最长保留期的行` 日志，避免把符合策略的历史缺行判成消费者停滞；重试或重新建索引不会恢复超期行。已有索引不按年龄清理，重新建索引时才重新过滤；`retained_rows_per_series` 也不保证实际行数或历史覆盖范围。

## 禁止

- 对行情 / 交易 / 因子 Dataset 执行 `purge-dataset-events`
- 不 dry-run 就 `--yes`；不带 `--yes` 的修改命令
- 把 EventBus token、HMAC 打进命令行、日志或聊天（Storage 维护命令不需要 EventBus 凭据，见 [`cli-operations.md`](cli-operations.md)）
- 用 `--reset-view-indexes` / `force-rebuild-view` 当常规追平
- 只重启 View 就宣布 kline 已追平（InProgress 心跳僵尸重启后仍可能立刻再占满 ACK 窗口）
- 给首次建索引补发行级就绪事件。曾把每行 fsync 落盘，拖死同进程全部 View（含 kline）。Factor 的实时入口是周期完成事件（`DatasetPeriodCompleted`），计算窗口来自因子机本地时序副本，由 PrimaryStore 增量同步；历史结果需要修正时使用显式 Recalc

## Storage View：单数据集消费停滞

Primary 水位前进而某个 View 长时间不动时，定位该数据集实际的 `view_<hash>`，核对 pending、redelivery、ACK、最老待处理事件年龄及 `storage-view` 写入错误。当前没有共享 `storage_view_kline` durable。ACK 窗口占满只能说明投递未完成，还需区分索引写失败、处理慢、首次建索引占用与进程问题。

确需修复索引时，在操作机用 `moox-cli storage repair-view`（经 SSH 在存储机上执行同版本程序；路径参数自动补齐），先只读预演（不带 `--yes` 就是预演）：

```bash
moox-cli storage repair-view \
  --space-id crypto \
  --view-id view_dasftksvjhj2jom4vhd0_kline_1m \
  --consumer <该数据集的实际消费者名>
```

核对目标与备份路径后，按既有授权执行对应 `--yes` 命令。默认删除所指定的消费者并重启 storage-view；`deliver_policy=new`，不动索引。每个数据集有自己的消费者，不能套用“第一个 View 重置共享 kline durable，其余跳过”的旧流程；不要把 `--reset-view-indexes` 当普通追平选项。

### EventBus 管理权限

删除 consumer 等 EventBus 管理操作由 Admin 代为执行：`moox-cli storage ...` 用 storage-view 的网关身份调用 Admin，Storage 主机上不需要 EventBus 管理员凭据，也就没有“凭据里的地址是控制机回环”这类陷阱。
命令在停止 View 之前就会通过 `InspectEventBus` 确认网关、Admin 与 EventBus 都可用（`--dry-run` 同样会走一遍），不会出现 View 已被停止才发现连不上。

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

- 1m：View `output_watermark` 与 Primary 相差分钟级；`c_updated_at` 继续前进
- 1h：对齐到**已收盘**小时（与 Primary 1h 相同），不要用当前未结束小时当缺口
- Factor：每个 enabled 因子任务的 `factor_last_period_time` 接近最新已收盘周期；`factor_period_total` 持续增长且 lag 回落
- 不要只看 `storage-view` / `moox-factor-mgr` 的 pid
