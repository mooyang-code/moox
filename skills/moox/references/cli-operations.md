# moox-cli 运维操作

本文记录 Agent 可以使用的 MooX 队列和 View 自助修复命令。Storage 维护命令用 `moox-cli storage …`，它按 `moox.toml` 经 SSH 在存储机上执行，
不需要自己登录主机；必须先预览（不带 `--yes` 就是预览）确认目标。水位停滞的分层诊断和生产路径见 [`view-catchup.md`](view-catchup.md)。

## 前置条件

- 命令在产物包（或仓库）根目录执行，读取当前目录的 `moox.toml`（`--file` 指定）；`moox-cli` 与存储机上部署包里的 `moox-storage-cli` 必须来自同一版本发布包。
- Storage 维护命令（`repair-view`、`reset-metadata`、`delete-view`）不调用 Admin、不连接 EventBus，不需要任何 EventBus 凭据。
- 不要把任何 token、HMAC 写进命令、脚本、日志或 Skill。
- `--yes` 才会执行修改；不带 `--yes` 时 `moox-cli storage …` 自动加 `--dry-run` 只预览（`delete-view` 没有预览，不带 `--yes` 会被拒绝）。
- 路径参数（`--storage-conf`、`--package-root`、`--metadata-db`）缺省时按部署目录补齐（`<storage_root>/current/…`），不要再手写路径。
- 命令输出 sanitized JSON，同时保存在存储机 `<storage_root>/maintenance/<动作>-<时间>.log`；应保留 `backup_path`、`view_indexes_removed` 和 `restarted` 等字段用于运维记录。退出码与存储机上的程序一致。
- 用 `--host <主机 ID>` 可以指定在哪台主机上执行（默认 `storage-primary` 所在主机）；`--help` 只显示本地说明，不连接主机。

## Factor 周期诊断与补算

Factor 的实时入口是 durable `factor_collector_period_v1`。积压时查看 JetStream pending、
最旧事件时间，以及 Factor 的 `factor_period_lag_seconds{task}`、
`factor_last_period_time{task}`、`factor_runner_backlog{task}`、
`factor_period_duration_seconds{task,stage}` 和 `factor_failures_total`。不要删除 durable
来丢弃尚未处理的周期；恢复服务后由 JetStream 重投，历史修正通过显式 Recalc job 完成。

```bash
./bin/moox-cli factor recalc submit --task-id ftask_spot_1m \
  --from 2026-10-04T00:00:00Z --to 2026-10-04T01:00:00Z
./bin/moox-cli factor recalc list --task-id ftask_spot_1m      # 查看作业
./bin/moox-cli factor recalc show --job-id <作业>
./bin/moox-cli factor recalc cancel --job-id <作业>
```

Recalc 范围为左闭右开，会按最多 2000 个完整周期分块执行；不给 `--request-id` 时命令自动生成（同一个请求重试要带同一个）。
因子定义与因子任务解耦：`moox-cli factor create` 只创建定义，`factor member add` 才把它作为 disabled 成员加入因子任务，
`factor member enable` 启用成员（见 [`factor-dsl.md`](factor-dsl.md)）。`moox-cli setup factors` 从 `moox.toml` 的 `[[factors.definitions]]` 与 `[[factors.tasks]]`（成员内嵌在任务的 `members` 里）依次补齐定义、任务和成员，
重复执行只补缺不删除，逐项报告已存在 / 新建 / 冲突并打印每个成员的成员键与列名；配置里已去掉的成员只报告，不自动移除。成员键与列名的规则见 [`factor-dsl.md`](factor-dsl.md)。
`moox-factor-mgr-cli`（因子管理主机上的本地工具）另有 `init`、`import`、`import-catalog`、`recalc`、`status` 和 `purge-dataset`，只在没有对应 `moox-cli` 命令时使用；
`purge-dataset --space <空间> --dataset <数据集> --yes` 用因子管理端自己的网关身份删除一个因子结果数据集（行和元数据，不可恢复）：
只用于清理没有因子任务引用的孤立结果数据集（例如重建因子库后遗留的旧结果），仍被因子任务引用的会被拒绝。

## 重建 Storage View

View 自己跟随 Primary 的变更日志，落后时先按 [`view-catchup.md`](view-catchup.md) 用 `moox-cli data view state` 和 `follow_lag` 指标确认卡在哪里，不要直接重建。
只有 View 物理索引已损坏、调大 `view_rows_per_series` 要回填到新根数，或要丢弃 View 的全部内容时，才重建指定 View。先预览（不带 `--yes` 就是预览），把目标给用户确认：

```bash
./bin/moox-cli storage repair-view \
  --space-id crypto \
  --view-id view_factor_spot_1m
```

确认目标后在同一条命令末尾加 `--yes` 才执行。流程：

1. 停止 `storage-view`。
2. 备份 Metadata SQLite（输出中的 `backup_path` 是回滚和问题复盘所需的关键证据）。
3. 清掉这个 View 的活动索引指针并删除物理索引。
4. 重启 `storage-view`，从 Primary 回填每个序列最近的行后接着跟随变更日志。

重建完成前 View 不可读；Primary 已不保留的行不可恢复；记录型 View（Bleve）没有可读的 Primary 历史，重建后为空，只跟随新写入。

主要参数（原样交给存储机上的程序，下列为程序的参数）：

| 参数 | 默认值 | 用途 |
| --- | --- | --- |
| `--space-id` | 无，必填 | View 所属 Space |
| `--view-id` | 无，必填 | 要重建的 View |
| `--storage-conf` | `<storage_root>/current/storage-view/config/storage.yaml` | Storage 配置 |
| `--package-root` | `<storage_root>/current` | `start.sh`/`stop.sh` 所在根目录 |
| `--timeout` | `2m` | 总操作超时 |
| `--restart` | `true` | 完成后重启 `storage-view` |
| `--yes` | `false` | 执行变更确认 |
| `--dry-run` | 不带 `--yes` 时自动加上 | 只检查，不停止服务、不改状态 |

不要手工删除 DuckDB/Bleve 文件；已删除 View 的索引由 Storage View 的清理定时器回收。
**新建 View、给 View 加标的、调大 `view_rows_per_series` 或放宽 `[storage_view.max_age]` 前要核对内存余量**：`storage-view` 没有代码配置的进程级总预算，单索引 `memory_limit` 也不是进程 RSS 的硬上界。
历史记录（2026-10-28，未在本次审查中重测）为文件 2.7GB、常驻 7.8GB、主机 15.5GB；稳态 2.5～3 倍、首次建索引额外预留最大索引的 1～1.5 倍，只是相近负载的粗估，不是源码保证。
可按“序列数 × `view_rows_per_series` 行”初估行数；每行 100～150 字节是历史压缩样本，实际取决于字段和数据。用 `docs/部署与运维.md`“Storage 主机内存要求”的经验公式起步，再测量目标负载的 RSS 和建索引峰值；余量不足先升配或缩小 View。
建索引按 `view_rows_per_series` 从 Primary 读取每个序列最近的行，再按 `max_age` 过滤；历史不足或行已超龄时，以实际留下的数据建成，不保证凑满 N 根。

## 下线单个 View

控制台没有删除 View 的入口。要下线某一个 View（例如已不再有读取方的 View），先暂停 `storage-view`
（直接 stop 会在一分钟内被主机守护拉起，用 `moox-runtime pause`，可经 `moox-cli host exec --host <存储机> -- …` 执行），再离线删除这一个 View 的元数据：

```bash
./bin/moox-cli storage delete-view --view <空间>/<View ID> --yes
```

命令只删指定的一个 View 及其列（外键级联），输出待回收的 `engine/index_id`；物理文件由 `storage-view` 的清理定时器在确认无引用后回收。
删除前先备份元数据库。在线删除（不需要暂停）也可以用 `moox-cli metadata view delete --view-id <View>`（默认只预览，`--yes` 执行），它走 Metadata 接口，物理索引同样由清理定时器回收。

Storage 服务的时序 View 每个序列保留最近 `10000` 根，由每分钟的定时任务删除更旧的行（偶数分钟第 10 秒执行，每批行数见 `storage.view.delete_oldest_batch_size`）。
可在根目录 `moox.toml` 的 `[storage_retention] view_rows_per_series` 统一调整；调大后已有索引的行不会变多，要 `repair-view` 重建才回填到新根数。
回填按游标分页，同一标的可以跨页；storage-primary 在每个序列内倒序读到目标根数即跳到下一个序列，扫描量按
「序列数 × 根数」增长，不扫描全部历史。Primary 历史不足配置根数，或回填行被 `[storage_view.max_age]` 过滤时，以实际留下的数据建成；之后跟随新写入继续更新，但不保证填满 N 根。
年龄过滤以处理批次时的时钟为准，早于截止点的行被丢弃，等于截止点保留；全部被过滤仍返回成功，不代表这些行已进入 View。此过滤不主动清理已有索引行，不改变 Primary 或因子机本地存储的保留行为。
记录型 View（Bleve）不按根数。

## Agent 处理顺序

1. 按 [`view-catchup.md`](view-catchup.md) 先确认卡在 Primary、View 跟随、还是 Factor 读超时；不要看到结果停滞就直接删除数据。
2. 对目标命令先预览（不带 `--yes`），确认 Space、View 和 package root。
3. Factor durable 积压时恢复服务并检查重投，不删除 consumer；历史数据修正使用有界 Recalc（`factor recalc submit`）。View 索引损坏或要按新根数回填时 `storage repair-view`。
4. 记录 JSON 中的删除结果、备份路径和重启状态。`repair-view` 在 stop 之后失败时必须把 `storage-view` 拉起来。
5. 等 View 重建完成并跟上后（`moox-cli data view state` 有活动索引、`moox-cli data view wait-applied` 返回），再查询 View/Factor 最新 `output_watermark`；不要用“进程已启动”代替数据已追赶的验收。
