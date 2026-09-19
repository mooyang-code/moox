# Collector 任务结果重置操作手册

该操作用于新项目上线前清理旧 Collector 任务运行记录，并重新建立“一个采集任务对应一个结果”的 Schema。它只处理 Collector 自己的数据库和任务结果；Storage 中的 Factor、Strategy、手工数据集不在清理范围内。

## 1. 发布前备份

先停止 Collector 的调度和写入，再备份 Collector SQLite 数据库和 Storage Metadata manifest。备份目录必须带时间戳，并保留到本次发布验收完成之后。

```bash
sudo systemctl stop moox-collector
backup_stamp=$(date -u +%Y%m%dT%H%M%SZ)
backup_dir="/data/moox/backups/collector-reset-${backup_stamp}"
mkdir -p "$backup_dir"
cp --preserve=all /data/moox/prod/data/moox_collector.db "$backup_dir/"
cp --preserve=all /data/moox/storage/data/metadata.db "$backup_dir/" 2>/dev/null || true
```

实际执行时使用同一个时间戳，避免两个备份目录不一致。

## 2. 预览清单

默认命令只读 Collector DB，不会删除任务、结果、View 或 Dataset：

```bash
moox-cli collector task purge \
  --file ./moox.toml \
  --db-path /data/moox/prod/data/moox_collector.db \
  --space-id crypto
```

重点核对空间、任务数、任务实例数、批次数、重试数以及每个任务对应的 Collector-owned View/Dataset。当前命令无法从 Collector SQLite 单独推断 Storage 中的孤儿结果，`unlinked_result_count` 只表示 Collector 任务行中缺少结果引用；Storage Metadata 仍需通过发布验收检查。

## 3. 执行重置

确认清单后才允许执行：

```bash
moox-cli collector task purge \
  --file ./moox.toml \
  --db-path /data/moox/prod/data/moox_collector.db \
  --space-id crypto \
  --control-url https://127.0.0.1:9527 \
  --service-access-key "$MOOX_GATEWAY_SERVICE_KEY_ID" \
  --service-secret-key "$MOOX_GATEWAY_SERVICE_SECRET_KEY" \
  --stop-command 'cd /data/moox/prod && ./stop.sh collector' \
  --backup-dir /data/moox/backups/collector-reset-20260919T000000Z \
  --collector-init-bin /data/moox/prod/bin/moox-collector-cli \
  --seed-file /data/moox/prod/config/collector/tasks/crypto.yaml \
  --apply --confirm
```

指定 `--space-id` 时，命令只通过控制面删除该空间的任务、运行记录和任务结果，不会重命名或重建整库；因此可以安全地处理单个空间。省略 `--space-id` 才会将原 DB 移入备份目录，然后调用 `moox-collector-cli init` 创建当前 Schema 并导入可选任务种子。没有同时指定 `--apply --confirm` 时不会发生文件变更。执行期间不得启动 Collector 写入；初始化失败时会保留已完成的备份阶段，禁止自动启动旧服务。

全空间重置示例：

```bash
moox-cli collector task purge \
  --file ./moox.toml \
  --db-path /data/moox/prod/data/moox_collector.db \
  --control-url https://127.0.0.1:9527 \
  --service-access-key "$MOOX_GATEWAY_SERVICE_KEY_ID" \
  --service-secret-key "$MOOX_GATEWAY_SERVICE_SECRET_KEY" \
  --stop-command 'cd /data/moox/prod && ./stop.sh collector' \
  --backup-dir /data/moox/backups/collector-reset-20260919T000000Z \
  --collector-init-bin /data/moox/prod/bin/moox-collector-cli \
  --seed-file /data/moox/prod/config/collector/tasks/crypto.yaml \
  --apply --confirm
```

如果还需要物理删除某个现有任务的结果，应优先在前端删除任务弹窗中选择“物理删除结果数据”。该路径会按任务所有权删除对应 View、Dataset 和 Dataset rows；保留结果则只删除任务运行记录，不触碰 Storage 结果。

## 4. 启动与验收

```bash
sudo systemctl start moox-collector
moox-cli setup status --file ./moox.toml
```

验收至少包括：

1. 任务列表能返回任务名称、任务 ID 和唯一结果摘要。
2. 采集结果页按任务展示结果 Tab，不暴露 Dataset ID。
3. 表格能查询真实数据、筛选和排序；时序结果能打开 K 线弹窗。
4. Timer/SCF 执行器数量满足配置，任务实例持续上报成功状态。
5. Storage Metadata 中 Collector-owned View 处于 active，结果页能够刷新到新写入数据。

## 5. 失败恢复

若初始化、启动或验收失败，先停止 Collector 和相关写入，再恢复 Collector DB 备份及 Storage Metadata manifest，回滚前端与 Collector 二进制，最后重新启动并检查任务列表。不要在未确认备份可读之前删除备份目录。
