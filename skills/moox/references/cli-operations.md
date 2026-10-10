# moox-cli 运维操作

本文记录 Agent 可以使用的 MooX 队列和 View 自助修复命令。它们只应在对应服务所在的部署主机上执行，并且必须先用 `--dry-run` 确认目标。水位停滞的分层诊断、生产路径和 EventBus 地址陷阱见 [`view-catchup.md`](view-catchup.md)。

## 前置条件

- `moox-cli`、`moox-factor-mgr-cli` 和 `moox-storage-cli` 必须来自同一版本发布包。
- 操作主机需要能访问本机或配置的 EventBus，并能读取 EventBus internal-admin 凭据。
- 不要把凭据写进命令、脚本、日志或 Skill。优先使用环境变量：
  `MOOX_EVENTBUS_INTERNAL_ADMIN_CREDENTIAL_FILE`，也可以使用命令的
  `--credential-file`。
- `--yes` 才会执行修改；没有 `--yes` 的命令只能用于查看帮助或失败退出。
- 所有命令输出 sanitized JSON；应保留 `backup_path`、`pending_before` 和 `deleted` 等字段用于运维记录。

## Factor 周期诊断与补算

Factor 的实时入口是 durable `factor_collector_period_v1`。积压时查看 JetStream pending、
最旧事件时间，以及 Factor 的 `factor_period_lag_seconds{set}`、
`factor_last_period_time{set}`、`factor_lane_backlog{set}`、
`factor_period_duration_seconds{set,stage}` 和 `factor_failures_total`。不要删除 durable
来丢弃尚未处理的周期；恢复服务后由 JetStream 重投，历史修正通过显式 Recalc job 完成。

```bash
moox-factor-mgr-cli recalc --set fset_dasftksvjhj2jom4vhd0_1m \
  --start 2026-10-04T00:00:00Z --end 2026-10-04T01:00:00Z
```

Recalc 范围为左闭右开，会按最多 2000 个完整周期分块执行。查看或提交任务请使用
FactorMgr 接口。因子定义与因子集解耦：`moox-factor-mgr-cli import` 只创建定义，加 `--set` 才会
把它作为 disabled 成员加入因子集；启用成员走 FactorMgr `SetFactorMemberStatus`，不是
CLI 离线操作。`moox-cli setup factors` 从 `moox.toml` 的 `[[factors.sets]]`、
`[[factors.definitions]]`、`[[factors.members]]` 依次补齐因子集、定义和成员，重复执行只补缺
不删除；旧的 `[[factors.items]]` 已移除。
Factor CLI 的支持命令为 `init`、`import`、`import-catalog`、`recalc`、
和 `status`；单个周期的诊断用引擎自己的 `moox-factor-engine run-once`。

## 清理 Storage View 积压并触发 A/B 重建

当某个 Storage View 分区积压、View 不再追赶 Source，或需要重新触发一次安全重建时，使用：

```bash
moox-cli storage repair-view \
  --storage-conf /data/moox/storage/current/storage-primary/config/storage.yaml \
  --package-root /data/moox/storage \
  --space-id crypto \
  --view-id view_dasftksvjhj2jom4vhd0_kline_1m \
  --consumer storage_view_kline \
  --credential-file <从 control 复制来的 internal-admin.yaml，mode 0600> \
  --eventbus-url tls://<EventBus公网IP>:4222 \
  --yes
```

独立 Storage 主机用上面的 `storage.yaml` 和包根，不要传 `storage-view/config/trpc_go.yaml`。控制机上的 `internal-admin.yaml` 默认 `tls://127.0.0.1:4222`，在 Storage 上必须 `--eventbus-url` 指到 EventBus 公网。三个 crypto kline View 共用 `storage_view_kline`：只在第一个 View 删除 durable，其余 `--reset-consumer=false`。因子 View 用 `--consumer storage_view_factor`。

默认流程：

1. 停止 `storage-view`。
2. 备份 Metadata SQLite。
3. 删除目标分区对应的 durable consumer（Kline/metrics/other 之一）。
4. 递增 View desired revision，交由服务正常执行 A/B 构建和切换。
5. 重启 `storage-view`。

默认不会删除 active 物理索引；输出中的 `backup_path` 是回滚和问题复盘所需的关键证据。

先检查目标 View 和将要执行的动作：

```bash
moox-cli storage repair-view \
  --storage-conf /data/moox/storage/current/storage-primary/config/storage.yaml \
  --package-root /data/moox/storage \
  --space-id crypto \
  --view-id view_dasftksvjhj2jom4vhd0_kline_1m \
  --consumer storage_view_kline \
  --credential-file <从 control 复制来的 internal-admin.yaml，mode 0600> \
  --eventbus-url tls://<EventBus公网IP>:4222 \
  --dry-run
```

主要参数：

| 参数 | 默认值 | 用途 |
| --- | --- | --- |
| `--space-id` | 无，必填 | View 所属 Space |
| `--view-id` | 无，必填 | 要修复的 View |
| `--storage-conf` | `MOOX_STORAGE_CONFIG` 或 `config/storage.yaml` | Storage 配置 |
| `--package-root` | `MOOX_STORAGE_PACKAGE_ROOT` 或配置路径推导值 | 存储部署根目录，其下 `current/` 是当前发布，内含 `start.sh`/`stop.sh`/`status.sh` |
| `--stream` | `MOOX_STORAGE` | JetStream stream |
| `--consumer` | `storage_view_kline` | 分区 durable；kline 三个行情 View 共用，因子 View 用 `storage_view_factor` |
| `--deliver-policy` | `new` | 重建 consumer 的投递策略；重放时才使用 `all` |
| `--credential-file` | Storage/EventBus admin 环境变量 | NATS admin 凭据 |
| `--eventbus-url` | 凭据文件/环境配置 | 覆盖 EventBus 地址 |
| `--timeout` | `2m` | 总操作超时 |
| `--reset-consumer` | `true` | 删除 durable consumer |
| `--force-rebuild` | `true` | 递增 desired revision |
| `--restart` | `true` | 重启 `storage-view` |
| `--purge-inactive-index` | `false` | 只删除 inactive 物理索引 |
| `--reset-view-indexes` | `false` | 删除 A/B 两个物理索引并从事件重建 |
| `--yes` | `false` | 执行变更确认 |
| `--dry-run` | `false` | 只检查，不停止服务、不改状态 |

## 高风险选项

`--reset-view-indexes` 不是常规积压清理。它会清空 Metadata active 指针并删除 A/B 物理索引，只有在确认 JetStream 仍保留完整 Source 事件时才可以使用，并且必须同时指定：

```bash
moox-cli storage repair-view ... \
  --reset-view-indexes --deliver-policy=all --yes
```

`--purge-inactive-index` 只清理 inactive 槽位；不要手工删除 active DuckDB/Bleve 文件。A/B 切换、双写和旧索引延迟清理由 Storage View 自己完成。

## 精确清理单个 Dataset 的历史事件

当一个可丢弃的高频运维 Dataset（例如 `dataset_mooxsys_service_metrics`）占满共享 Storage View durable，
但业务 Dataset 的历史事件必须保留时，不要删除整个 consumer。先检查精确 subject：

```bash
/data/moox/storage/current/bin/moox-storage-cli purge-dataset-events \
  --space mooxsys \
  --dataset dataset_mooxsys_service_metrics \
  --credential-file <从 control 复制来的 internal-admin.yaml，mode 0600> \
  --dry-run
```

确认后增加 `--yes`。该命令只从 `MOOX_STORAGE` stream 删除这个 Space/Dataset 的
rows、period、factor-computed 和 sync-point 事件；不会删除 durable consumer，也不会删除
其他 Dataset 的历史事件。只允许用于已确认可由后续采样重新生成的运维数据，行情、交易和
因子业务 Dataset 不得使用。

## View 强制从头重建

当 View 物理索引已经损坏、结果历史必须完全丢弃，或需要清理整个 View 的消费状态时，使用：

```bash
moox-cli storage force-rebuild-view \
  --storage-conf /data/moox/storage/current/storage-primary/config/storage.yaml \
  --package-root /data/moox/storage \
  --space-id crypto \
	--view-id view_factor_binance_kline_1m \
  --dry-run
```

确认目标后再执行同样命令并增加 `--yes`。该命令会停止整个 Storage 生命周期、备份 Metadata、
删除 durable consumer、清空 View active/build/period/sync 状态、删除 A/B 物理索引，并以 `DeliverAll`
重新消费 Source 事件；原 View 历史数据不可恢复。重建按 `view_bars` 从 Primary 回填每个序列最近的
K 线，历史不足时以现有数据激活。

## 收敛默认 View 集合

新项目只保留三个行情 View 和五个系统监控 View。需要删除其他 View 定义时，先暂停
`storage-view`（`/data/moox/storage/pause.sh storage-view`，写暂停标记并停止进程；只停止不够，健康检查会在一分钟内
把它重新拉起来），再执行一次性元数据收敛命令，完成后用 `resume.sh storage-view` 恢复：

```bash
/data/moox/storage/current/bin/moox-storage-cli retain-views \
  --metadata-db /data/moox/storage/data/storage/metadata/storage_metadata.db \
  --package-root /data/moox/storage \
  --keep-view crypto/view_dasftksvjhj2jom4vhd0_kline_1m \
  --keep-view crypto/view_crypto_swap_kline_1h \
  --keep-view crypto/view_crypto_spot_kline_1h \
  --keep-view mooxsys/view_mooxsys_host_resource \
  --keep-view mooxsys/view_mooxsys_host_fs \
  --keep-view mooxsys/view_mooxsys_host_disk \
  --keep-view mooxsys/view_mooxsys_host_net \
  --keep-view mooxsys/view_mooxsys_service_metrics \
  --yes
```

命令要求精确传入八个活动 View，并且 `storage-view` 必须已经暂停。它只删除 SQLite
中的非保留 View、列、构建和日志记录，输出待清理的 `engine/index_id`；不会直接删除
物理 A/B 文件。随后由 Storage View 的 Cleanup Timer 在确认无引用后清理文件。

Storage 服务的时序 View 默认按所有频率回溯 `5000` 根；任一序列超过 `6000`（ceil(1.2 × 5000)）根时触发安全重建。
昂贵的序列容量扫描默认每个 Storage View 进程、View 和 active index 每小时至多执行一次，首次扫描
在一小时范围内随机错峰；轻量 View Maintainer 仍按 `maintenance_check_interval` 默认每分钟运行。可在根目录
`moox.toml` 的 `[storage_retention] view_bars` 统一调整，适用于自动 A/B、启动恢复和手动
重建。回填对每个标的只读一次：DataNode 在每个序列内倒序读到目标根数即跳到下一个序列，耗时只与
「标的数 × 根数」有关、与历史长度无关。Primary 历史不足配置根数时以现有数据激活，之后由实时事件补齐。
记录型 View（Bleve）不按根数，整体重建。

## Agent 处理顺序

1. 按 [`view-catchup.md`](view-catchup.md) 先确认卡在 Primary、View durable、还是 Factor 读超时；不要看到结果停滞就直接删除数据。
2. 对目标命令执行 `--dry-run`，确认 stream、consumer、Space、View 和 package root。
3. Factor durable 积压时恢复服务并检查重投，不删除 consumer；历史数据修正使用有界 Recalc。View kline 不追赶时 `repair-view`。
4. 记录 JSON 中的 pending 数、删除结果、备份路径和重启状态。`repair-view` 在 stop 之后失败时必须把 `storage-view` 拉起来。
5. 等待新周期事件进入后，再查询 View/Factor 最新 `c_period_time` / `output_watermark`；不要用“进程已启动”代替数据已追赶的验收。
6. 只有 Source 事件可完整重放、并已确认备份可用时，才升级到 `--reset-view-indexes --deliver-policy=all`。
