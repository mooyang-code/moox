# moox-cli 运维操作

本文记录 Agent 可以使用的 MooX 队列和 View 自助修复命令。它们只应在对应服务所在的部署主机上执行，并且必须先用 `--dry-run` 确认目标。水位停滞的分层诊断、生产路径和 EventBus 地址陷阱见 [`view-catchup.md`](view-catchup.md)。

## 前置条件

- `moox-cli`、`moox-factor-mgr-cli` 和 `moox-storage-cli` 必须来自同一版本发布包。
- Storage 维护命令（`repair-view`、`force-rebuild-view`、`reset-view-consumers`、`purge-dataset-events`）不需要任何 EventBus 凭据：
  它们用 `--storage-conf` 指向的 storage-view 配置里的网关身份调用 Admin，由 Admin 代为连接 EventBus 执行；Storage 主机上不要放 EventBus 管理员凭据，也没有 `--credential-file`、`--eventbus-url` 参数。
- 不要把任何 token、HMAC 写进命令、脚本、日志或 Skill。
- `--yes` 才会执行修改；没有 `--yes` 的命令只能用于查看帮助或失败退出。
- 所有命令输出 sanitized JSON；应保留 `backup_path`、`pending_before` 和 `deleted` 等字段用于运维记录。

## Factor 周期诊断与补算

Factor 的实时入口是 durable `factor_collector_period_v1`。积压时查看 JetStream pending、
最旧事件时间，以及 Factor 的 `factor_period_lag_seconds{task}`、
`factor_last_period_time{task}`、`factor_runner_backlog{task}`、
`factor_period_duration_seconds{task,stage}` 和 `factor_failures_total`。不要删除 durable
来丢弃尚未处理的周期；恢复服务后由 JetStream 重投，历史修正通过显式 Recalc job 完成。

```bash
moox-factor-mgr-cli recalc --task ftask_spot_1m \
  --start 2026-10-04T00:00:00Z --end 2026-10-04T01:00:00Z
```

Recalc 范围为左闭右开，会按最多 2000 个完整周期分块执行。查看或提交任务请使用
FactorMgr 接口。因子定义与因子任务解耦：`moox-factor-mgr-cli import` 只创建定义，加 `--task` 才会
把它作为 disabled 成员加入因子任务；启用成员走 FactorMgr `SetTaskMemberStatus`，不是
CLI 离线操作。`moox-cli setup factors` 从 `moox.toml` 的 `[[factors.definitions]]` 与 `[[factors.tasks]]`（成员内嵌在任务的 `members` 里）依次补齐定义、任务和成员，
重复执行只补缺不删除，逐项报告已存在 / 新建 / 冲突并打印每个成员的成员键与列名；配置里已去掉的成员只报告，不自动移除。成员键与列名的规则见 [`factor-dsl.md`](factor-dsl.md)。
Factor CLI 的支持命令为 `init`、`import`、`import-catalog`、`recalc`、`status` 和 `purge-dataset`。
`purge-dataset --space <空间> --dataset <数据集> --yes` 用因子管理端自己的网关身份删除一个因子结果数据集（行和元数据，不可恢复）：
只用于清理没有因子任务引用的孤立结果数据集（例如重建因子库后遗留的旧结果），仍被因子任务引用的会被拒绝。

## 清理 Storage View 积压

当某个 Storage View 的消费积压、View 不再追赶 Source 时，使用：

```bash
moox-cli storage repair-view \
  --storage-conf /data/moox/storage/current/storage-view/config/storage.yaml \
  --package-root /data/moox/storage/current \
  --space-id crypto \
  --view-id view_dasftksvjhj2jom4vhd0_kline_1m \
  --consumer <View 消费者名> \
  --yes
```

原生 Storage 部署显式指定上面的 `current/storage-view/config/storage.yaml` 和 `current` 包根（默认配置搜索仍包含旧平铺路径假设，不要依赖自动推导），不要传 `trpc_go.yaml`。`--consumer` 只接受 Storage View 的消费者：按数据集动态创建的 `view_<12 位十六进制>`（每个数据集一个，其他名称在发出请求前就会被拒绝）。同一数据集的多个 View 共用同一个消费者时，只在第一个 View 删除 durable，其余 `--reset-consumer=false`；不同数据集的 View 各用各的消费者。

默认流程：

1. 停止 `storage-view`。
2. 备份 Metadata SQLite。
3. 删除目标数据集对应的 durable consumer（`view_<12 位十六进制>`）。
4. 重启 `storage-view`。

默认不会删除物理索引；输出中的 `backup_path` 是回滚和问题复盘所需的关键证据。

先检查目标 View 和将要执行的动作：

```bash
moox-cli storage repair-view \
  --storage-conf /data/moox/storage/current/storage-view/config/storage.yaml \
  --package-root /data/moox/storage/current \
  --space-id crypto \
  --view-id view_dasftksvjhj2jom4vhd0_kline_1m \
  --consumer <View 消费者名> \
  --dry-run
```

主要参数：

| 参数 | 默认值 | 用途 |
| --- | --- | --- |
| `--space-id` | 无，必填 | View 所属 Space |
| `--view-id` | 无，必填 | 要修复的 View |
| `--storage-conf` | `MOOX_STORAGE_CONFIG` 或 `config/storage.yaml` | Storage 配置 |
| `--package-root` | `MOOX_STORAGE_PACKAGE_ROOT` 或配置路径推导值 | `start.sh`/`stop.sh` 所在根目录 |
| `--stream` | `MOOX_STORAGE` | JetStream stream |
| `--consumer` | 无，必填 | View 的 durable 消费者名（见上文允许的名称） |
| `--deliver-policy` | `new` | 重建 consumer 的投递策略；重放时才使用 `all` |
| `--timeout` | `2m` | 总操作超时 |
| `--reset-consumer` | `true` | 删除 durable consumer |
| `--restart` | `true` | 重启 `storage-view` |
| `--reset-view-indexes` | `false` | 删除物理索引，重启后从 Primary 重新建，期间 View 不可读 |
| `--yes` | `false` | 执行变更确认 |
| `--dry-run` | `false` | 只检查，不停止服务、不改状态 |

## 高风险选项

`--reset-view-indexes` 不是常规积压清理。它会清空 Metadata active 指针并删除物理索引，View 在重新建好之前不可读，并且必须同时指定：

```bash
moox-cli storage repair-view ... \
  --reset-view-indexes --deliver-policy=all --yes
```

不要手工删除 DuckDB/Bleve 文件；已删除 View 的索引由 Storage View 的清理定时器回收。

## 精确清理单个 Dataset 的历史事件

当一个可丢弃的运维 Dataset（例如主机资源快照 `dataset_mooxsys_host_resource`）的积压事件需要清掉，
但业务 Dataset 的历史事件必须保留时，不要删除整个 stream 或 consumer。先检查精确 subject：

```bash
/data/moox/storage/current/bin/moox-storage-cli purge-dataset-events \
  --space mooxsys \
  --dataset dataset_mooxsys_host_resource \
  --storage-conf /data/moox/storage/current/storage-view/config/storage.yaml \
  --dry-run
```

确认后增加 `--yes`。该命令只从 `MOOX_STORAGE` stream 删除这个 Space/Dataset 的
rows、period 和 sync-point 事件；不会删除 durable consumer，也不会删除
其他 Dataset 的历史事件。只允许用于已确认可由后续采样重新生成的运维数据，行情、交易和
因子业务 Dataset 不得使用。

## View 强制从头重建

当 View 物理索引已经损坏、结果历史必须完全丢弃，或需要清理整个 View 的消费状态时，使用：

```bash
moox-cli storage force-rebuild-view \
  --storage-conf /data/moox/storage/current/storage-view/config/storage.yaml \
  --package-root /data/moox/storage/current \
  --space-id crypto \
	--view-id view_factor_spot_1m \
  --dry-run
```

确认目标后再执行同样命令并增加 `--yes`。该命令会停止整个 Storage 生命周期、备份 Metadata、
删除 durable consumer、清空 View active/period/sync 状态、删除物理索引，重启后从 Primary 回填每个序列最近的行，
并以 `DeliverAll` 重新消费 Source 事件；Primary 已不保留的行不可恢复。记录型 View（Bleve）没有可读的 Primary 历史，重新建后为空。
**新建 View、给 View 加标的、调大 `view_rows_per_series` 或放宽 `[storage_view.max_age]` 前要核对内存余量**：`storage-view` 没有代码配置的进程级总预算，单索引 `memory_limit` 也不是进程 RSS 的硬上界。
历史记录（2026-10-28，未在本次审查中重测）为文件 2.7GB、常驻 7.8GB、主机 15.5GB；稳态 2.5～3 倍、首次建索引额外预留最大索引的 1～1.5 倍，只是相近负载的粗估，不是源码保证。
可按“序列数 × `view_rows_per_series` 行”初估行数；每行 100～150 字节是历史压缩样本，实际取决于字段和数据。用 `docs/部署与运维.md`“Storage 主机内存要求”的经验公式起步，再测量目标负载的 RSS 和建索引峰值；余量不足先升配或缩小 View。
建索引按 `view_rows_per_series` 从 Primary 读取每个序列最近的行，再按 `max_age` 过滤；历史不足或行已超龄时，以实际留下的数据建成，不保证凑满 N 根。

## 下线单个 View

控制台没有删除 View 的入口。要下线某一个 View（例如不再有读取方的 `view_mooxsys_service_metrics`），先暂停 `storage-view`
（直接 stop 会在一分钟内被主机守护拉起，用 `moox-runtime pause`），再离线删除这一个 View 的元数据：

```bash
/data/moox/storage/current/bin/moox-storage-cli delete-view \
  --metadata-db /data/moox/storage/current/storage-primary/var/storage/metadata/storage_metadata.db \
  --package-root /data/moox/storage/current \
  --view mooxsys/view_mooxsys_service_metrics \
  --yes
```

命令只删指定的一个 View 及其列（外键级联），输出待回收的 `engine/index_id`；物理文件由 `storage-view` 的清理定时器在确认无引用后回收。
**不要用 `retain-views` 删单个 View**：它会删除所有不在保留清单里的 View，生产上的因子结果视图也会被删掉。删除前先备份元数据库。

## 收敛默认 View 集合

`retain-views` 是面向固定七个 View 布局的一次性收敛工具：参数仍要求恰好七个活动 View，所有未列出的 View 都会删除。当前采集任务与因子任务还会创建自己的 View，因此下面这份历史七项清单不能用于已有动态业务 View 的环境。只有确认环境确实只需要这七项时，先暂停 `storage-view` 防止守护重新拉起，再执行：

```bash
/data/moox/storage/current/bin/moox-storage-cli retain-views \
  --metadata-db /data/moox/storage/current/storage-primary/var/storage/metadata/storage_metadata.db \
  --package-root /data/moox/storage/current \
  --keep-view crypto/view_dasftksvjhj2jom4vhd0_kline_1m \
  --keep-view crypto/view_crypto_swap_kline_1h \
  --keep-view crypto/view_crypto_spot_kline_1h \
  --keep-view mooxsys/view_mooxsys_host_resource \
  --keep-view mooxsys/view_mooxsys_host_fs \
  --keep-view mooxsys/view_mooxsys_host_disk \
  --keep-view mooxsys/view_mooxsys_host_net \
  --yes
```

命令要求精确传入七个活动 View，并且 `storage-view` 必须已经停止。它只删除 SQLite
中的非保留 View 和列，输出待清理的 `engine/index_id`；不会直接删除
物理文件。随后由 Storage View 的 Cleanup Timer 在确认无引用后清理文件。

Storage 服务的时序 View 每个序列保留最近 `10000` 根，由每分钟的定时任务删除更旧的行（偶数分钟第 10 秒执行，每批行数见 `storage.view.delete_oldest_batch_size`）。
可在根目录 `moox.toml` 的 `[storage_retention] view_rows_per_series` 统一调整；调大后已有索引的行不会变多，要 `force-rebuild-view` 才回填到新根数。
回填按游标分页，同一标的可以跨页；DataNode 在每个序列内倒序读到目标根数即跳到下一个序列，扫描量按
「序列数 × 根数」增长，不扫描全部历史。Primary 历史不足配置根数，或回填行被 `[storage_view.max_age]` 过滤时，以实际留下的数据建成；后续实时事件继续更新，但不保证填满 N 根。
年龄过滤以处理批次时的时钟为准，早于截止点的行被丢弃，等于截止点保留；全部被过滤仍返回成功，不代表这些行已进入 View。此过滤不主动清理已有索引行，不改变 Primary 或因子机本地存储的保留行为。
记录型 View（Bleve）不按根数。

## Agent 处理顺序

1. 按 [`view-catchup.md`](view-catchup.md) 先确认卡在 Primary、View durable、还是 Factor 读超时；不要看到结果停滞就直接删除数据。
2. 对目标命令执行 `--dry-run`，确认 stream、consumer、Space、View 和 package root。
3. Factor durable 积压时恢复服务并检查重投，不删除 consumer；历史数据修正使用有界 Recalc。View kline 不追赶时 `repair-view`。
4. 记录 JSON 中的 pending 数、删除结果、备份路径和重启状态。`repair-view` 在 stop 之后失败时必须把 `storage-view` 拉起来。
5. 等待新周期事件进入后，再查询 View/Factor 最新 `c_period_time` / `output_watermark`；不要用“进程已启动”代替数据已追赶的验收。
6. 只有 Source 事件可完整重放、并已确认备份可用时，才升级到 `--reset-view-indexes --deliver-policy=all`。
