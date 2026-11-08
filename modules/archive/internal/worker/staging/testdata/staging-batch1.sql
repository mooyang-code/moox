-- 批 1 的暂存库两张永久表（git 2f56e11e 的 staging.go），用于原地升级的比对测试。
CREATE TABLE IF NOT EXISTS task_state (
    space_id TEXT NOT NULL,
    dataset_id TEXT NOT NULL,
    scan_watermark INTEGER,
    first_start INTEGER,
    resume_from INTEGER,
    task_id TEXT NOT NULL DEFAULT '',
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
    CHECK (status IN ('open', 'exported', 'final')),
    PRIMARY KEY (space_id, dataset_id, partition_key)
) WITHOUT ROWID;
