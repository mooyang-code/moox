# View / Factor 水位追平

行情或因子“停更”时先分清卡在哪一层，再用官方 CLI。不要用进程在跑代替数据在追；不要手工删 durable 或 DuckDB。

## 先量三层水位

在 **Storage 主机**看 Prometheus 和 metadata SQLite（南京机通常没有 `sqlite3` 命令，用 `python3` + `sqlite3` 只读打开）。

| 层 | 看什么 | 含义 |
|---|---|---|
| Primary | `moox_storage_dataset_output_watermark_timestamp_seconds` | 事实是否已写入。`data kline get` 走 Primary，不代表 View |
| View 索引 | `moox_storage_view_output_watermark_timestamp_seconds` | DuckDB 已提交的业务时间 |
| View period 表 | `t_view_period_dataset_states` 的 `MAX(c_period_time)` / `c_updated_at` | period-ready / Factor 触发水位；可与索引水位短暂不一致 |
| Factor 计算 | `factor_period_total{task,status}`、`factor_period_lag_seconds{task}`、`factor_last_period_time{task}` | 各因子任务是否在算、是否在写 |
| JetStream | View：`moox_storage_view_consumer_partition_lag_messages` + `oldest_pending_event_age_seconds`；Factor durable：`factor_collector_period_v1` pending/redelivered | 积压还是处理挂死 |

`c_period_time` 是 unix 秒。`t_views.c_mtime` 不是 K 线水位。

每个采集任务独占一个结果 Dataset/View：初始化的 Binance 现货 1m 任务写 `dataset_dasftksvjhj2jom4vhd0` 与 `view_dasftksvjhj2jom4vhd0_kline_1m`，合约 1m 任务写 `dataset_dasftksvjhj2jom4vhdg` 与 `view_dasftksvjhj2jom4vhdg_kline_1m`。每个因子任务的结果 Dataset 与默认 View（`dataset_factor_<后缀>`、`view_factor_<后缀>`）由 Storage 自动维护，一个源 Dataset 可以有多个因子任务。

## 生产路径（不要混用）

独立 Storage 主机（常见）：

- 包根：`/data/moox/storage`
- `repair-view` 的 `--storage-conf`：`/data/moox/storage/storage/config/storage.yaml`（不要传 `storage-view/config/trpc_go.yaml`，那是 View 进程配置）
- 分区 durable 在 **View** 的 `trpc_go.yaml`：`storage_view_factor` / `storage_view_misc`（模板），实际消费者是按数据集动态创建的 `storage_view_factor_<hash>` / `storage_view_misc_<hash>`
- Primary 的 `storage.yaml` 可以很短、没有 `consumer_partitions`，这不代表 View 没用分区

控制面：`/data/moox/prod`；Storage 单元在 Storage 主机的 `/data/moox/storage/current`，不要往其他主机发 Storage。

## Storage View 容量检查

有界保留的时序 View 重建后，每个 `(subject_id, frequency, series_tag)` 默认只保留最近 **5,000 根**完整 K 线；任一序列达到 **6,001 根**（严格超过 6,000）触发 A/B 安全重建。新索引完成回溯并追平后才切换，旧索引继续服务到无引用清理；此操作不删除 Primary 历史。1 GiB 文件阈值可独立触发有界保留 View 的重建；永久 View 超限时只产生 `moox_storage_view_permanent_view_capacity_over_limit` 告警，不自动截断历史。

容量统计由 Storage View Maintainer 执行。轻量维护仍每分钟检查，但对每个 View 的 active index，昂贵的逐序列容量扫描最多每小时一次。进程首次看到该 View/index 时先独立随机等待 `[0, 1h)`；进程重启或 active index 切换后重新抽取首次偏移。扫描之后按扫描开始时间加一小时调度，不按整点对齐。失败或超时也占用本次周期，不会被每分钟维护循环热重试。

## 禁止

- 对行情 / 交易 / 因子 Dataset 执行 `purge-dataset-events`
- 不 dry-run 就 `--yes`；不带 `--yes` 的修改命令
- 把 EventBus token、HMAC 打进命令行、日志或聊天（Storage 维护命令不需要 EventBus 凭据，见 [`cli-operations.md`](cli-operations.md)）
- 用 `--reset-view-indexes` / `force-rebuild-view` 当常规追平
- 只重启 View 就宣布 kline 已追平（InProgress 心跳僵尸重启后仍可能立刻再占满 ACK 窗口）
- 给重建补发行级就绪事件或往 `pending-subjects` 落 journal。from-scratch 重建曾把每行 fsync 到该目录，拖死同进程全部 View（含 kline）。Factor 的实时入口是 Collector 周期完成事件，源数据从 Storage PrimaryStore 读取；重建窗口漏掉的周期使用显式 Recalc

## Storage View：kline 分区挂死

信号：

- Primary kline 水位是分钟级，View 停在数小时前
- `storage_view_kline` 的 `partition_lag_messages` 等于 `max_ack_pending`（常见 64）
- `oldest_pending_event_age_seconds` 与水位停滞时长同量级
- ACK 计数不涨，但 `in_progress` 仍在增加
- misc / metrics 分区仍在刷 `mooxsys`。分区隔离挡不住同进程的 `pending-subjects` fsync；旧二进制 from-scratch 重建 metrics 时会把 kline ACK 窗口一起占满

这是 durable 被未完成投递占满，不是“没数据”。静态路由的 crypto kline View 共用 `storage_view_kline`；采集任务结果各自使用动态的按 Dataset durable。

在 Storage 主机、用**该机同版本** `moox-cli`：

```bash
# 先 dry-run 三个行情 View，确认 db_path / active_index / 下一档 desired revision
moox-cli storage repair-view \
  --storage-conf /data/moox/storage/storage/config/storage.yaml \
  --package-root /data/moox/storage \
  --space-id crypto \
  --view-id view_dasftksvjhj2jom4vhd0_kline_1m \
  --consumer <View 消费者名> \
  --dry-run
```

执行时：第一个 View 删除 kline durable 并 bump revision；其余三个 `--reset-consumer=false` 只 bump；最后一次再 `--restart=true`。用 `trap` 保证失败后仍 `start.sh --components storage-view`。默认 `deliver_policy=new`，缺口靠 A/B 从 Primary 回溯（`[storage_retention] view_rows_per_series`，默认 5000 根），不要为了追平改成 `--reset-view-indexes`。

### EventBus 管理权限

删除 consumer 等 EventBus 管理操作由 Admin 代为执行：`moox-cli storage ...` 用 storage-view 的网关身份调用 Admin，Storage 主机上不需要 EventBus 管理员凭据，也就没有“凭据里的地址是控制机回环”这类陷阱。
命令在停止 View 之前就会通过 `InspectEventBus` 确认网关、Admin 与 EventBus 都可用（`--dry-run` 同样会走一遍），不会出现 View 已被停止才发现连不上。

## Factor 周期诊断

Factor 消费 `CollectorPeriodCompleted`，durable 名为 `factor_collector_period_v1`；对
Storage 的计算输入读取走 PrimaryStore，不读取 View。先看该 durable 的 pending 与
redelivery，再按 `task` 查看 `factor_period_lag_seconds`、`factor_last_period_time`、
`factor_runner_backlog` 和 `factor_period_total`。检查 `factor_failures_total` 中的失败因子
与原因；`factor_compile_errors_total` 非零说明管理端与引擎的 expr 版本不一致。

| 现象 | 判断 | 动作 |
|---|---|---|
| durable pending 持续增长，最旧事件周期远早于当前时间 | 输入周期积压 | 检查 Factor 是否运行、因子任务是否 enabled、Storage Gateway 可达性和 `AckWait`；不要删除 consumer，恢复后由 JetStream 重投 |
| pending 低但 lag 或任务执行器 backlog 高 | 单个或多个因子任务处理较慢 | 对照 `factor_period_duration_seconds{task,stage}` 定位 read、compute、write 或 report 阶段 |
| `factor_failures_total` 增长 | 因子或标的数据失败 | 检查对应因子错误、输入列和上游缺失 subject；基础设施错误会重试，业务失败会进入周期终态 |
| 实时已追平但历史值需修正 | 历史补算 | 提交有界 Recalc 范围；已完成分块不会回滚 |


## 验收

- 1m：View `output_watermark` 与 Primary 相差分钟级；`c_updated_at` 继续前进
- 1h：对齐到**已收盘**小时（与 Primary 1h 相同），不要用当前未结束小时当缺口
- Factor：每个 enabled 因子任务的 `factor_last_period_time` 接近最新已收盘周期；`factor_period_total` 持续增长且 lag 回落
- 不要只看 `storage-view` / `moox-factor-mgr` 的 pid
