-- Archive worker 暂存库固定状态表（动态暂存表 staged_<hash> 不在此声明）。
-- 时间列统一存 Unix 纳秒整数；task_state 保存任务绑定与变更日志位点，partition_state 保存永久分区状态。

CREATE TABLE IF NOT EXISTS task_state (
    space_id TEXT NOT NULL,
    dataset_id TEXT NOT NULL,
    task_id TEXT NOT NULL DEFAULT '',
    purpose TEXT NOT NULL DEFAULT '',
    dir_name TEXT NOT NULL DEFAULT '',
    meta_remote_sha256 TEXT NOT NULL DEFAULT '',
    change_store_id TEXT NOT NULL DEFAULT '',
    change_sequence INTEGER NOT NULL DEFAULT 0,
    rescan_from INTEGER,
    rescan_until INTEGER,
    partitions_synced INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (space_id, dataset_id)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS partition_state (
    space_id TEXT NOT NULL,
    dataset_id TEXT NOT NULL,
    partition_key TEXT NOT NULL,
    range_start INTEGER NOT NULL,
    range_end INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    last_changed_at INTEGER NOT NULL,
    exported_at INTEGER,
    file_path TEXT NOT NULL DEFAULT '',
    file_sha256 TEXT NOT NULL DEFAULT '',
    export_count INTEGER NOT NULL DEFAULT 0,
    row_count INTEGER NOT NULL DEFAULT 0,
    subject_count INTEGER NOT NULL DEFAULT 0,
    min_data_time INTEGER,
    max_data_time INTEGER,
    finalized_at INTEGER,
    report_pending INTEGER NOT NULL DEFAULT 1,
    remote_key TEXT NOT NULL DEFAULT '',
    remote_sha256 TEXT NOT NULL DEFAULT '',
    remote_etag TEXT NOT NULL DEFAULT '',
    remote_uploaded_at INTEGER,
    remote_verified_at INTEGER,
    remote_error TEXT NOT NULL DEFAULT '',
    remote_attempts INTEGER NOT NULL DEFAULT 0,
    remote_retry_at INTEGER,
    CHECK (status IN ('open', 'exported', 'final')),
    PRIMARY KEY (space_id, dataset_id, partition_key)
) WITHOUT ROWID;
