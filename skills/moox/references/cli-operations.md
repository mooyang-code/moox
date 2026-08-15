# moox-cli 运维操作

本文记录 Agent 可以使用的 MooX 队列和 View 自助修复命令。它们只应在对应服务所在的部署主机上执行，并且必须先用 `--dry-run` 确认目标。

## 前置条件

- `moox-cli`、`moox-factor-cli` 和 `moox-storage-cli` 必须来自同一版本发布包。
- 操作主机需要能访问本机或配置的 EventBus，并能读取 EventBus internal-admin 凭据。
- 不要把凭据写进命令、脚本、日志或 Skill。优先使用环境变量：
  `MOOX_EVENTBUS_INTERNAL_ADMIN_CREDENTIAL_FILE`，也可以使用命令的
  `--credential-file`。
- `--yes` 才会执行修改；没有 `--yes` 的命令只能用于查看帮助或失败退出。
- 所有命令输出 sanitized JSON；应保留 `backup_path`、`pending_before` 和 `deleted` 等字段用于运维记录。

## 清理 Factor 历史积压

当 Factor 结果长时间处理旧周期、`factor_view_ready_v1` 的 pending 持续增长时，使用：

```bash
moox-cli factor clear-queue \
  --package-root /home/<user>/moox/prod \
  --credential-file /home/<user>/.config/moox/eventbus/internal-admin.yaml \
  --yes
```

默认目标是 `MOOX_STORAGE/factor_view_ready_v1`。命令按以下顺序执行：

1. 停止 Factor，避免删除 durable consumer 时仍有旧连接竞争。
2. 读取 consumer 的 `pending`、`ack_pending`，删除 durable consumer。
3. 启动 Factor；Factor 会按 `DeliverNew` 重建 consumer，只接收清理完成后的新事件。

该操作不会删除 Factor 定义、结果 View 或 Storage 数据。若只想检查参数，不连接 EventBus：

```bash
moox-cli factor clear-queue --dry-run
```

可选参数：

| 参数 | 默认值 | 用途 |
| --- | --- | --- |
| `--stream` | `MOOX_STORAGE` | JetStream stream |
| `--consumer` | `factor_view_ready_v1` | 要删除的 durable consumer |
| `--credential-file` | 环境变量或 `~/.config/moox/eventbus/internal-admin.yaml` | NATS admin 凭据 |
| `--eventbus-url` | 凭据文件/环境配置 | 覆盖 EventBus 地址 |
| `--package-root` | `MOOX_FACTOR_PACKAGE_ROOT` 或可执行文件推导值 | `restart.sh` 所在部署根目录 |
| `--timeout` | `2m` | 停止、EventBus 操作和启动的总超时 |
| `--restart` | `true` | 是否自动停止并重启 Factor |
| `--yes` | `false` | 执行删除和重启的确认开关 |

如果使用 `moox-factor-cli` 直接调用，参数和默认值完全相同：

```bash
moox-factor-cli clear-queue --package-root /home/<user>/moox/prod --yes
```

`moox-cli` 查找 Factor CLI 的顺序是 `MOOX_FACTOR_CLI`、同目录的
`moox-factor-cli`、发布包中的 Factor bin 目录、最后是 `PATH`。无法自动定位时设置
`MOOX_FACTOR_CLI=/absolute/path/to/moox-factor-cli`。

## 清理 Storage View 积压并触发 A/B 重建

当 Storage View 的 `storage_view_period_v1` 积压、View 不再追赶 Source，或需要重新触发一次安全重建时，使用：

```bash
moox-cli storage repair-view \
  --storage-conf /home/<user>/moox/prod/storage-view/config/storage.yaml \
  --package-root /home/<user>/moox/prod \
  --space-id crypto_market \
  --view-id binance_spot_kline_1m_factor \
  --yes
```

默认流程：

1. 停止 `storage-view`。
2. 备份 Metadata SQLite。
3. 删除 `MOOX_STORAGE/storage_view_period_v1` durable consumer。
4. 递增 View desired revision，交由服务正常执行 A/B 构建和切换。
5. 重启 `storage-view`。

默认不会删除 active 物理索引；输出中的 `backup_path` 是回滚和问题复盘所需的关键证据。

先检查目标 View 和将要执行的动作：

```bash
moox-cli storage repair-view \
  --storage-conf /home/<user>/moox/prod/storage-view/config/storage.yaml \
  --package-root /home/<user>/moox/prod \
  --space-id crypto_market \
  --view-id binance_spot_kline_1m_factor \
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
| `--consumer` | `storage_view_period_v1` | Storage View durable consumer |
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

当一个可丢弃的高频运维 Dataset（例如 `moox_service_metrics`）占满共享 Storage View durable，
但业务 Dataset 的历史事件必须保留时，不要删除整个 consumer。先检查精确 subject：

```bash
/home/<user>/moox/storage/bin/moox-storage-cli purge-dataset-events \
  --space moox_system \
  --dataset moox_service_metrics \
  --credential-file /home/<user>/.config/moox/eventbus/internal-admin.yaml \
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
  --storage-conf /home/<user>/moox/storage/config/storage.yaml \
  --package-root /home/<user>/moox/storage \
  --space-id crypto_market \
  --view-id binance_spot_kline_1m_factor \
  --lookback 72h \
  --dry-run
```

确认目标后再执行同样命令并增加 `--yes`。该命令会停止 `storage-view`、备份 Metadata、
删除 durable consumer、清空 View active/build/period/sync 状态、删除 A/B 物理索引，并以 `DeliverAll`
重新消费 Source 事件；原 View 历史数据不可恢复。`--lookback` 是本次重建的最低历史覆盖要求，
新索引未覆盖该时长前不会被激活。

Storage 服务默认使用 `storage.view.rebuild_lookback: 72h`，适用于自动 A/B、启动恢复和手动
重建。若 View 的 `keep_duration` 更长，实际回溯窗口取两者较大值；若 Source 事件保留不足，
构建会保持未完成状态，不会发布一个短历史 View。

## Agent 处理顺序

1. 先确认是 Factor durable backlog，还是 Storage View backlog；不要看到结果停滞就直接删除数据。
2. 对目标命令执行 `--dry-run`，确认 stream、consumer、Space、View 和 package root。
3. 优先执行 `factor clear-queue`；只有 View 本身不追赶或需要重新构建时，再执行 `storage repair-view`。
4. 记录 JSON 中的 pending 数、删除结果、备份路径和重启状态。
5. 等待新周期事件进入后，再查询 View/Factor 最新 `data_time`；不要用“进程已启动”代替数据已追赶的验收。
6. 只有 Source 事件可完整重放、并已确认备份可用时，才升级到 `--reset-view-indexes --deliver-policy=all`。
