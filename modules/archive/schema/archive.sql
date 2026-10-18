-- Archive manager SQLite schema（库以 PRAGMA auto_vacuum = INCREMENTAL 创建，见 store.Open）。
-- 时间列统一存 UTC 文本 "2006-01-02 15:04:05.000"，按字典序比较即按时间比较。

-- 归档任务：一个时序数据集最多一个任务；频率、分区粒度和执行间隔在保存时确定，保留期与 DataNode 由维护定时器刷新
CREATE TABLE IF NOT EXISTS t_archive_tasks (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_freq TEXT NOT NULL,
    c_partition_unit TEXT NOT NULL,
    c_data_node_id TEXT NOT NULL DEFAULT '',
    c_retention TEXT NOT NULL DEFAULT '',
    c_file_prefix TEXT NOT NULL DEFAULT '',
    c_meta_json TEXT NOT NULL DEFAULT '',
    c_schedule_cron TEXT NOT NULL,
    c_schedule_tz TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    c_interval_seconds INTEGER NOT NULL,
    c_start_at DATETIME NOT NULL,
    c_backfill_from DATETIME,
    c_status TEXT NOT NULL DEFAULT 'enabled',
    c_next_run_at DATETIME,
    c_consecutive_skips INTEGER NOT NULL DEFAULT 0,
    c_last_success_at DATETIME,
    c_final_until DATETIME,
    c_creator TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_partition_unit IN ('day', 'month')),
    CHECK (c_interval_seconds >= 300),
    CHECK (c_status IN ('enabled', 'paused')),
    CHECK (c_consecutive_skips >= 0),
    UNIQUE (c_space_id, c_dataset_id)
);

CREATE INDEX IF NOT EXISTS idx_t_archive_tasks_due ON t_archive_tasks (c_status, c_next_run_at);

CREATE TRIGGER IF NOT EXISTS trg_t_archive_tasks_mtime
AFTER UPDATE ON t_archive_tasks
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_archive_tasks SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- 归档运行：一次定时、手动或补拉区间的执行；running 期间由租约标识持有的 worker
CREATE TABLE IF NOT EXISTS t_archive_runs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_run_id TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_kind TEXT NOT NULL DEFAULT 'scheduled',
    c_range_start DATETIME,
    c_range_end DATETIME,
    c_status TEXT NOT NULL DEFAULT 'queued',
    c_scheduled_at DATETIME NOT NULL,
    c_started_at DATETIME,
    c_finished_at DATETIME,
    c_worker_id TEXT NOT NULL DEFAULT '',
    c_lease_token TEXT NOT NULL DEFAULT '',
    c_lease_until DATETIME,
    c_subjects_total INTEGER NOT NULL DEFAULT 0,
    c_subjects_done INTEGER NOT NULL DEFAULT 0,
    c_request_count INTEGER NOT NULL DEFAULT 0,
    c_bytes_read INTEGER NOT NULL DEFAULT 0,
    c_rows_read INTEGER NOT NULL DEFAULT 0,
    c_rows_added INTEGER NOT NULL DEFAULT 0,
    c_rows_changed INTEGER NOT NULL DEFAULT 0,
    c_partitions_exported INTEGER NOT NULL DEFAULT 0,
    c_partitions_finalized INTEGER NOT NULL DEFAULT 0,
    c_unrecoverable_json TEXT NOT NULL DEFAULT '[]',
    c_message TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_kind IN ('scheduled', 'manual', 'range')),
    CHECK (c_status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    CHECK (c_kind != 'range' OR (c_range_start IS NOT NULL AND c_range_end IS NOT NULL)),
    FOREIGN KEY (c_space_id, c_dataset_id) REFERENCES t_archive_tasks (c_space_id, c_dataset_id) ON DELETE CASCADE,
    UNIQUE (c_run_id)
);

CREATE INDEX IF NOT EXISTS idx_t_archive_runs_task ON t_archive_runs (c_space_id, c_dataset_id, c_scheduled_at);
CREATE INDEX IF NOT EXISTS idx_t_archive_runs_active
ON t_archive_runs (c_status, c_scheduled_at)
WHERE c_status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS idx_t_archive_runs_retention ON t_archive_runs (c_kind, c_status, c_finished_at);

CREATE TRIGGER IF NOT EXISTS trg_t_archive_runs_mtime
AFTER UPDATE ON t_archive_runs
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_archive_runs SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- 归档分区：每个任务每个本地日（1d 及以上为月，归月规则见 8.1）一行；open 只在 worker 暂存区，exported 已有文件且可能重写，final 已定稿
CREATE TABLE IF NOT EXISTS t_archive_partitions (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_partition_key TEXT NOT NULL,
    c_range_start DATETIME NOT NULL,
    c_range_end DATETIME NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'open',
    c_worker_id TEXT NOT NULL,
    c_row_count INTEGER NOT NULL DEFAULT 0,
    c_subject_count INTEGER NOT NULL DEFAULT 0,
    c_min_data_time DATETIME,
    c_max_data_time DATETIME,
    c_file_path TEXT NOT NULL DEFAULT '',
    c_file_bytes INTEGER NOT NULL DEFAULT 0,
    c_file_sha256 TEXT NOT NULL DEFAULT '',
    c_columns_json TEXT NOT NULL DEFAULT '[]',
    c_preview TEXT NOT NULL DEFAULT '',
    c_export_count INTEGER NOT NULL DEFAULT 0,
    c_last_changed_at DATETIME,
    c_exported_at DATETIME,
    c_finalized_at DATETIME,
    c_last_run_id TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('open', 'exported', 'final')),
    CHECK (c_status = 'open' OR c_file_path != ''),
    CHECK (c_row_count >= 0),
    CHECK (c_subject_count >= 0),
    CHECK (c_file_bytes >= 0),
    CHECK (c_export_count >= 0),
    FOREIGN KEY (c_space_id, c_dataset_id) REFERENCES t_archive_tasks (c_space_id, c_dataset_id),
    UNIQUE (c_space_id, c_dataset_id, c_partition_key)
);

CREATE INDEX IF NOT EXISTS idx_t_archive_partitions_worker ON t_archive_partitions (c_worker_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_archive_partitions_mtime
AFTER UPDATE ON t_archive_partitions
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_archive_partitions SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- 归档 worker：同一时刻只有一个 worker 持有活动租约（c_lease_until 未过期）；心跳刷新磁盘、暂存库与正在执行的运行
CREATE TABLE IF NOT EXISTS t_archive_workers (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_worker_id TEXT NOT NULL,
    c_instance_id TEXT NOT NULL DEFAULT '',
    c_version TEXT NOT NULL DEFAULT '',
    c_root_dir TEXT NOT NULL DEFAULT '',
    c_disk_total_bytes INTEGER NOT NULL DEFAULT 0,
    c_disk_free_bytes INTEGER NOT NULL DEFAULT 0,
    c_staging_bytes INTEGER NOT NULL DEFAULT 0,
    c_maintaining INTEGER NOT NULL DEFAULT 0,
    c_pacing_json TEXT NOT NULL DEFAULT '{}',
    c_running_json TEXT NOT NULL DEFAULT '[]',
    c_lease_until DATETIME,
    c_last_heartbeat_at DATETIME,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_disk_total_bytes >= 0),
    CHECK (c_disk_free_bytes >= 0),
    CHECK (c_staging_bytes >= 0),
    CHECK (c_maintaining IN (0, 1)),
    UNIQUE (c_worker_id)
);

CREATE TRIGGER IF NOT EXISTS trg_t_archive_workers_mtime
AFTER UPDATE ON t_archive_workers
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_archive_workers SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;
