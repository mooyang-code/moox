-- Archive manager SQLite schema（库以 PRAGMA auto_vacuum = INCREMENTAL 创建，见 store.Open）。
-- 时间列统一存 Unix 毫秒整数（INTEGER NOT NULL），0 表示没有。

-- 归档任务：一个时序数据集最多一个任务；分区粒度由频率推出，线上保留期与元数据由内存缓存向 Storage 读取
-- 日常跟随由 worker 本地循环执行并上报到任务行；c_backfill_from 是归档起点（创建时确定，之后不改）
-- c_purpose（collect 采集结果、factor 因子结果、data 其他）与 c_dir_name 决定分区目录 <空间>/<用途>/<目录名>/，建好后不改
CREATE TABLE IF NOT EXISTS t_archive_tasks (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_purpose TEXT NOT NULL,
    c_dir_name TEXT NOT NULL,
    c_freq TEXT NOT NULL,
    c_backfill_from INTEGER NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'enabled',
    c_source_missing INTEGER NOT NULL DEFAULT 0,
    c_last_follow_at INTEGER NOT NULL DEFAULT 0,
    c_last_success_at INTEGER NOT NULL DEFAULT 0,
    c_last_follow_error TEXT NOT NULL DEFAULT '',
    c_last_follow_message TEXT NOT NULL DEFAULT '',
    c_rescan_from INTEGER NOT NULL DEFAULT 0,
    c_rescan_until INTEGER NOT NULL DEFAULT 0,
    c_final_until INTEGER NOT NULL DEFAULT 0,
    c_unrecoverable_json TEXT NOT NULL DEFAULT '[]',
    c_blocked_reason TEXT NOT NULL DEFAULT '',
    c_ctime INTEGER NOT NULL DEFAULT (CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)),
    c_mtime INTEGER NOT NULL DEFAULT (CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)),
    CHECK (c_status IN ('enabled', 'disabled')),
    CHECK (c_source_missing IN (0, 1)),
    CHECK (c_purpose IN ('collect', 'factor', 'data')),
    CHECK (c_dir_name != ''),
    CHECK ((c_rescan_from = 0 AND c_rescan_until = 0) OR (c_rescan_from > 0 AND c_rescan_until > 0)),
    UNIQUE (c_space_id, c_dataset_id),
    UNIQUE (c_space_id, c_purpose, c_dir_name)
);

CREATE INDEX IF NOT EXISTS idx_t_archive_tasks_status ON t_archive_tasks (c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_archive_tasks_mtime
AFTER UPDATE ON t_archive_tasks
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_archive_tasks SET c_mtime = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER) WHERE c_id = OLD.c_id;
END;

-- 归档运行：仅记录 manual（整段重读线上保留期）、range（补拉区间）、import（moox-cli import 写入暂存区后统一导出）
-- running 期间靠运行租约（c_lease_token、c_lease_until）发现 worker 失联
CREATE TABLE IF NOT EXISTS t_archive_runs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_run_id TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_kind TEXT NOT NULL,
    c_range_start INTEGER NOT NULL DEFAULT 0,
    c_range_end INTEGER NOT NULL DEFAULT 0,
    c_status TEXT NOT NULL DEFAULT 'queued',
    c_scheduled_at INTEGER NOT NULL,
    c_started_at INTEGER NOT NULL DEFAULT 0,
    c_finished_at INTEGER NOT NULL DEFAULT 0,
    c_lease_token TEXT NOT NULL DEFAULT '',
    c_lease_until INTEGER NOT NULL DEFAULT 0,
    c_subjects_done INTEGER NOT NULL DEFAULT 0,
    c_request_count INTEGER NOT NULL DEFAULT 0,
    c_bytes_read INTEGER NOT NULL DEFAULT 0,
    c_rows_read INTEGER NOT NULL DEFAULT 0,
    c_rows_added INTEGER NOT NULL DEFAULT 0,
    c_rows_changed INTEGER NOT NULL DEFAULT 0,
    c_partitions_exported INTEGER NOT NULL DEFAULT 0,
    c_partitions_finalized INTEGER NOT NULL DEFAULT 0,
    c_partitions_uploaded INTEGER NOT NULL DEFAULT 0,
    c_unrecoverable_json TEXT NOT NULL DEFAULT '[]',
    c_message TEXT NOT NULL DEFAULT '',
    c_ctime INTEGER NOT NULL DEFAULT (CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)),
    c_mtime INTEGER NOT NULL DEFAULT (CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)),
    CHECK (c_kind IN ('manual', 'range', 'import')),
    CHECK (c_status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    CHECK (c_kind != 'range' OR (c_range_start > 0 AND c_range_end > 0)),
    FOREIGN KEY (c_space_id, c_dataset_id) REFERENCES t_archive_tasks (c_space_id, c_dataset_id) ON DELETE CASCADE,
    UNIQUE (c_run_id)
);

CREATE INDEX IF NOT EXISTS idx_t_archive_runs_task ON t_archive_runs (c_space_id, c_dataset_id, c_scheduled_at);
CREATE INDEX IF NOT EXISTS idx_t_archive_runs_active
ON t_archive_runs (c_status, c_scheduled_at)
WHERE c_status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS idx_t_archive_runs_retention ON t_archive_runs (c_status, c_finished_at);

CREATE TRIGGER IF NOT EXISTS trg_t_archive_runs_mtime
AFTER UPDATE ON t_archive_runs
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_archive_runs SET c_mtime = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER) WHERE c_id = OLD.c_id;
END;

-- 归档分区：每个任务每个本地日（1d 及以上为月，归月规则见 8.1）一行；open 只在 worker 暂存区，exported 已有文件且可能重写，final 已定稿
-- 这份登记是 worker 本地暂存库丢失后的重建来源；c_remote_* 是对象存储副本，c_remote_key 相对对象存储的 prefix，
-- c_remote_sha256 = c_file_sha256 且 c_remote_verified_at > 0 时远端是当前版本（“已复制”）
CREATE TABLE IF NOT EXISTS t_archive_partitions (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_partition_key TEXT NOT NULL,
    c_range_start INTEGER NOT NULL,
    c_range_end INTEGER NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'open',
    c_row_count INTEGER NOT NULL DEFAULT 0,
    c_subject_count INTEGER NOT NULL DEFAULT 0,
    c_min_data_time INTEGER NOT NULL DEFAULT 0,
    c_max_data_time INTEGER NOT NULL DEFAULT 0,
    c_file_path TEXT NOT NULL DEFAULT '',
    c_file_bytes INTEGER NOT NULL DEFAULT 0,
    c_file_sha256 TEXT NOT NULL DEFAULT '',
    c_columns_json TEXT NOT NULL DEFAULT '[]',
    c_preview TEXT NOT NULL DEFAULT '',
    c_export_count INTEGER NOT NULL DEFAULT 0,
    c_last_changed_at INTEGER NOT NULL DEFAULT 0,
    c_exported_at INTEGER NOT NULL DEFAULT 0,
    c_finalized_at INTEGER NOT NULL DEFAULT 0,
    c_last_run_id TEXT NOT NULL DEFAULT '',
    c_remote_key TEXT NOT NULL DEFAULT '',
    c_remote_sha256 TEXT NOT NULL DEFAULT '',
    c_remote_etag TEXT NOT NULL DEFAULT '',
    c_remote_uploaded_at INTEGER NOT NULL DEFAULT 0,
    c_remote_verified_at INTEGER NOT NULL DEFAULT 0,
    c_remote_error TEXT NOT NULL DEFAULT '',
    c_ctime INTEGER NOT NULL DEFAULT (CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)),
    c_mtime INTEGER NOT NULL DEFAULT (CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)),
    CHECK (c_status IN ('open', 'exported', 'final')),
    CHECK (c_status = 'open' OR c_file_path != ''),
    CHECK (c_row_count >= 0),
    CHECK (c_subject_count >= 0),
    CHECK (c_file_bytes >= 0),
    CHECK (c_export_count >= 0),
    FOREIGN KEY (c_space_id, c_dataset_id) REFERENCES t_archive_tasks (c_space_id, c_dataset_id),
    UNIQUE (c_space_id, c_dataset_id, c_partition_key)
);

CREATE TRIGGER IF NOT EXISTS trg_t_archive_partitions_mtime
AFTER UPDATE ON t_archive_partitions
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_archive_partitions SET c_mtime = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER) WHERE c_id = OLD.c_id;
END;

-- 归档 worker：部署里只有一个 worker，表里只留最近心跳的那一行；心跳刷新磁盘、暂存库、正在执行的运行与对象存储摘要
-- 磁盘取自 statfs：c_disk_total_bytes 总量、c_disk_used_bytes 已用、c_disk_free_bytes 可用（不含 root 保留空间）；
-- c_min_free_bytes 是 worker 配置的下限，可用低于它时 worker 停止跟随与领取新运行
CREATE TABLE IF NOT EXISTS t_archive_workers (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_worker_id TEXT NOT NULL,
    c_version TEXT NOT NULL DEFAULT '',
    c_root_dir TEXT NOT NULL DEFAULT '',
    c_disk_total_bytes INTEGER NOT NULL DEFAULT 0,
    c_disk_free_bytes INTEGER NOT NULL DEFAULT 0,
    c_disk_used_bytes INTEGER NOT NULL DEFAULT 0,
    c_min_free_bytes INTEGER NOT NULL DEFAULT 0,
    c_staging_bytes INTEGER NOT NULL DEFAULT 0,
    c_maintaining INTEGER NOT NULL DEFAULT 0,
    c_pacing_json TEXT NOT NULL DEFAULT '{}',
    c_running_json TEXT NOT NULL DEFAULT '[]',
    c_last_heartbeat_at INTEGER NOT NULL DEFAULT 0,
    c_object_store_json TEXT NOT NULL DEFAULT '{}',
    c_ctime INTEGER NOT NULL DEFAULT (CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)),
    c_mtime INTEGER NOT NULL DEFAULT (CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)),
    CHECK (c_disk_total_bytes >= 0),
    CHECK (c_disk_free_bytes >= 0),
    CHECK (c_disk_used_bytes >= 0),
    CHECK (c_min_free_bytes >= 0),
    CHECK (c_staging_bytes >= 0),
    CHECK (c_maintaining IN (0, 1)),
    UNIQUE (c_worker_id)
);

CREATE TRIGGER IF NOT EXISTS trg_t_archive_workers_mtime
AFTER UPDATE ON t_archive_workers
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_archive_workers SET c_mtime = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER) WHERE c_id = OLD.c_id;
END;
