# Collector 任务结果重置操作手册

该操作用于新项目上线前清理旧 Collector 任务运行记录，并重新建立“一个采集任务对应一个结果”的 Schema。它只处理 Collector 自己的数据库，以及明确标记为 `owner_module=collector` 且带有 `collector_task_id` 的任务结果；Storage 中的 Factor、Strategy、手工数据集不在清理范围内。

## 1. 停止与备份边界

`purge --apply` 会先对 Collector SQLite 做一致性快照，再执行仓库部署包提供的 `stop.sh collector`。只有停止成功后，才删除本地运行记录，并按 dry-run 清单删除 Collector-owned View/Dataset。停止失败时不得删除任务或结果；本流程只使用部署包生命周期脚本。

```bash
collector_root=/data/moox/prod
stop_command="cd ${collector_root} && ./stop.sh collector"
cd "$collector_root"
./stop.sh collector
cd - >/dev/null
backup_stamp=$(date -u +%Y%m%dT%H%M%SZ)
backup_dir="/data/moox/backups/collector-reset-${backup_stamp}"
mkdir -p "$backup_dir"
cp --preserve=all "${collector_root}/data/moox_collector.db" "$backup_dir/"
cp --preserve=all "${collector_root}/data/moox_collector.db-wal" "$backup_dir/" 2>/dev/null || true
cp --preserve=all "${collector_root}/data/moox_collector.db-shm" "$backup_dir/" 2>/dev/null || true
cp --preserve=all /data/moox/storage/data/metadata.db "$backup_dir/" 2>/dev/null || true
```

命令全空间 apply 时还会在停止成功后自动备份 Collector DB 并执行新 Schema 初始化；手工备份仍应保留到发布验收完成之后。实际执行时使用同一个时间戳，避免两个备份目录不一致。

## 2. 预览清单

默认命令只读 Collector DB，不会删除任务、结果、View 或 Dataset。建议显式传 `--dry-run`，便于审计命令意图：

```bash
moox-cli collector task purge \
  --file ./moox.toml \
  --db-path /data/moox/prod/data/moox_collector.db \
  --space-id crypto \
  --dry-run
```

若要读取真实 Storage inventory，可提供 Metadata target/URL 和 Storage Metadata 鉴权；省略时命令会尝试读取 `--file` manifest 中匹配空间的 `StorageRPCGatewayTarget`：

```bash
moox-cli collector task purge \
  --file ./moox.toml \
  --db-path /data/moox/prod/data/moox_collector.db \
  --space-id crypto \
  --metadata-target ip://<storage-gateway>:11003 \
  --metadata-service-key storage-metadata \
  --metadata-service-secret "$MOOX_STORAGE_NODE_AUTH_SECRET" \
  --dry-run
```

重点核对空间、任务数、任务实例数、批次数、重试数，以及结果清单中的
`space_id/task_id/result_view_id/result_dataset_id`。`storage_inventory_status=available` 时再核对
Collector-owned Dataset/View 与 orphan 数量；若为 `unavailable`，必须人工通过 Storage 管理台或
Metadata API 核对，不能把本地任务引用数量当作真实 Storage-owned 数量。

## 3. 执行重置

确认清单、Storage inventory（或人工核对）后才允许执行。`--apply` 不得与 `--dry-run` 同时指定，
且必须同时指定 `--confirm`：

```bash
moox-cli collector task purge \
  --file ./moox.toml \
  --db-path /data/moox/prod/data/moox_collector.db \
  --space-id crypto \
  --stop-command 'cd /data/moox/prod && ./stop.sh collector' \
  --backup-dir /data/moox/backups/collector-reset-20260919T000000Z \
  --collector-init-bin /data/moox/prod/bin/moox-collector-cli \
  --seed-file /data/moox/prod/config/collector/tasks/crypto.yaml \
  --apply --confirm
```

执行阶段固定为：一致性备份 → `stop.sh collector` 成功 → 删除对应空间的本地任务运行记录 → 删除 dry-run 中全部
Collector-owned View/Dataset 及其物理行数据；
任何删除失败都会返回已完成阶段，保留数据库和可重试清单。指定 `--space-id` 时不会重命名或重建整库。
省略 `--space-id` 才会执行全空间数据库备份，再调用 `moox-collector-cli init` 创建当前 Schema 并导入可选任务种子。
执行期间不得启动 Collector 写入；初始化失败时会保留已完成的备份阶段，禁止自动启动旧服务。

全空间重置示例：

```bash
moox-cli collector task purge \
  --file ./moox.toml \
  --db-path /data/moox/prod/data/moox_collector.db \
  --stop-command 'cd /data/moox/prod && ./stop.sh collector' \
  --backup-dir /data/moox/backups/collector-reset-20260919T000000Z \
  --collector-init-bin /data/moox/prod/bin/moox-collector-cli \
  --seed-file /data/moox/prod/config/collector/tasks/crypto.yaml \
  --apply --confirm
```

如果还需要物理删除某个现有任务的结果，应优先在前端删除任务弹窗中选择“物理删除结果数据”。该路径会按任务所有权删除对应 View、Dataset 和 Dataset rows；保留结果则只删除任务运行记录，不触碰 Storage 结果。

## 4. 启动与验收

```bash
cd /data/moox/prod
./start.sh collector
moox-cli setup status --file ./moox.toml
```

验收至少包括：

1. 任务列表能返回任务名称、任务 ID 和唯一结果摘要。
2. 采集结果页按任务展示结果 Tab，不暴露 Dataset ID。
3. 表格能查询真实数据、筛选和排序；时序结果能打开 K 线弹窗。
4. Timer/SCF 执行器数量满足配置，任务实例持续上报成功状态。
5. Storage Metadata 中 Collector-owned View 处于 active，结果页能够刷新到新写入数据。

## 5. 失败恢复

若命令失败，先查看 JSON 中的 `completed_stages`：

1. 没有 `collector_database_backed_up`：没有开始远端删除，先修复备份权限或 SQLite 状态。
2. 没有 `collector_writes_stopped`：Collector 尚未被本命令确认停止，先修复 `stop.sh collector` 后重试；此时不得认为结果已被删除。
3. 已有 `collector_writes_stopped` 但没有 `collector_owned_views_deleted` / `collector_owned_datasets_deleted`：本地运行记录可能已清，按 dry-run 清单核对 Storage 后重试元数据删除。
4. 已有 `collector_database_reset_for_init` 但没有 `collector_schema_initialized`：保留原始备份，修复初始化程序或种子后重试；不要自动启动旧服务。

若初始化、启动或验收失败，先使用 `cd /data/moox/prod && ./stop.sh collector` 停止 Collector 和相关写入，再恢复
Collector DB 备份及 Storage Metadata manifest，回滚前端与 Collector 二进制，最后执行
`cd /data/moox/prod && ./start.sh collector` 并检查任务列表。不要在未确认备份可读之前删除备份目录。
