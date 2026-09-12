# View / Factor 水位追平

行情或因子“停更”时先分清卡在哪一层，再用官方 CLI。不要用进程在跑代替数据在追；不要手工删 durable 或 DuckDB。

## 先量三层水位

在 **Storage 主机**看 Prometheus 和 metadata SQLite（南京机通常没有 `sqlite3` 命令，用 `python3` + `sqlite3` 只读打开）。

| 层 | 看什么 | 含义 |
|---|---|---|
| Primary | `moox_storage_dataset_output_watermark_timestamp_seconds` | 事实是否已写入。`data kline get` 走 Primary，不代表 View |
| View 索引 | `moox_storage_view_output_watermark_timestamp_seconds` | DuckDB 已提交的业务时间 |
| View period 表 | `t_view_period_dataset_states` 的 `MAX(c_period_time)` / `c_updated_at` | period-ready / Factor 触发水位；可与索引水位短暂不一致 |
| Factor 计算 | 控制面 `moox_factor_dataset_output_watermark_timestamp_seconds` 与 `last_success` | 是否在算、是否在写 |
| JetStream | View：`moox_storage_view_consumer_partition_lag_messages` + `oldest_pending_event_age_seconds`；Factor：`clear-queue --yes` 才会打印 `pending_before` | 积压还是处理挂死 |

`c_period_time` 是 unix 秒。`t_views.c_mtime` 不是 K 线水位。

crypto 行情 View 至少有：`view_crypto_spot_kline_1m`、`view_crypto_swap_kline_1m`、`view_crypto_spot_kline_1h`、`view_crypto_swap_kline_1h`，以及 `view_crypto_spot_kline_1m_factor`。

## 生产路径（不要混用）

独立 Storage 主机（常见）：

- 包根：`/data/moox/storage`
- `repair-view` 的 `--storage-conf`：`/data/moox/storage/storage/config/storage.yaml`（不要传 `storage-view/config/trpc_go.yaml`，那是 View 进程配置）
- 分区 durable 在 **View** 的 `trpc_go.yaml`：`storage_view_kline` / `storage_view_metrics` / `storage_view_factor` / `storage_view_misc*`
- Primary 的 `storage.yaml` 可以很短、没有 `consumer_partitions`，这不代表 View 没用分区

控制面：`/data/moox/prod`。Trade 可能在 `/home/ubuntu/moox/trade-move`，不要往香港机发 Storage。

## 禁止

- 对行情 / 交易 / 因子 Dataset 执行 `purge-dataset-events`
- 不 dry-run 就 `--yes`；不带 `--yes` 的修改命令
- 把 EventBus token、HMAC、`internal-admin.yaml` 打进命令行、日志或聊天
- 用 `--reset-view-indexes` / `force-rebuild-view` 当常规追平
- 只重启 View 就宣布 kline 已追平（InProgress 心跳僵尸重启后仍可能立刻再占满 ACK 窗口）

## Storage View：kline 分区挂死

信号：

- Primary kline 水位是分钟级，View 停在数小时前
- `storage_view_kline` 的 `partition_lag_messages` 等于 `max_ack_pending`（常见 64）
- `oldest_pending_event_age_seconds` 与水位停滞时长同量级
- ACK 计数不涨，但 `in_progress` 仍在增加
- misc / metrics 分区仍在刷 `mooxsys`（分区隔离，不能把 kline 停滞归因于 host metrics）

这是 durable 被未完成投递占满，不是“没数据”。四个 crypto kline View **共用** `storage_view_kline`。

在 Storage 主机、用**该机同版本** `moox-cli`：

```bash
# 先 dry-run 四个 View，确认 db_path / active_index / 下一档 desired revision
moox-cli storage repair-view \
  --storage-conf /data/moox/storage/storage/config/storage.yaml \
  --package-root /data/moox/storage \
  --space-id crypto \
  --view-id view_crypto_spot_kline_1m \
  --consumer storage_view_kline \
  --credential-file ~/.config/moox/eventbus/internal-admin.yaml \
  --eventbus-url tls://<EventBus公网IP>:4222 \
  --dry-run
```

执行时：第一个 View 删除 kline durable 并 bump revision；其余三个 `--reset-consumer=false` 只 bump；最后一次再 `--restart=true`。用 `trap` 保证失败后仍 `start.sh storage-view`。默认 `deliver_policy=new`，缺口靠 A/B 从 Primary 回溯（`rebuild_lookback_periods`，默认 1000 根），不要为了追平改成 `--reset-view-indexes`。

### EventBus admin 地址

控制机上的 `internal-admin.yaml` 的 `urls` 是 `tls://127.0.0.1:4222`。拷到 Storage 后若不覆盖地址，删除 consumer 会 `nats: no servers available for connection`，而 View 可能已被 stop。必须加 `--eventbus-url tls://<EventBus公网IP>:4222`，CA 用 Storage 上已有的 `ca.pem`。凭据 mode `0600`，只打印字节数和权限。

## Factor：队列积压还是把 View 打满

先看控制面 Factor 日志和指标，再决定清队列：

| 现象 | 判断 | 动作 |
|---|---|---|
| `factor_view_ready_v1` `pending` 很大，且在算很旧的 `period_time` | durable 积压 | `moox-cli factor clear-queue --dry-run` 后 `--yes`（在 **控制面** `/data/moox/prod`） |
| `pending` 很小（甚至 1），`period_time` 已是当前分钟，但大量 `11003` `i/o timeout` / `factor_view_read_retry` | View 读并发把 DuckDB 打满 | **不要**指望再清一次队列就能好。降 `MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS`（默认 8）后重启 Factor |
| Factor Primary/View 水位随 `last_success` 前进 | 已在追 | 等当前 period 跑完；一根 1m × 数百 subject 在 8 并发下可能要几分钟 |

`clear-queue --dry-run` **不连 EventBus**，JSON 里的 `pending_before` 恒为 0，不能当积压证据。`--yes` 的 summary 才有真实 pending。

Factor 操作主机是跑 `moox-factor` 的控制面，不是 Storage：

```bash
moox-cli factor clear-queue \
  --package-root /data/moox/prod \
  --credential-file ~/.config/moox/eventbus/internal-admin.yaml \
  --dry-run
```

控制面连本机 EventBus，一般不必 `--eventbus-url`。

## 验收

- 1m：View `output_watermark` 与 Primary 相差分钟级；`c_updated_at` 继续前进
- 1h：对齐到**已收盘**小时（与 Primary 1h 相同），不要用当前未结束小时当缺口
- Factor：`last_success` 接近墙钟；输出水位随正在计算的 `period_time` 前进；日志出现 `status=complete` 的 `factor_batch_done`，而不再是整批 `i/o timeout`
- 不要只看 `storage-view` / `moox-factor` 的 pid
