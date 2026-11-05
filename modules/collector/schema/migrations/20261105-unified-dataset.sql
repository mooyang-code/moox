-- 统一数据集（现货加永续）上线前升级已有 Collector 库。
-- 步骤：停 Collector → 备份 moox_collector.db → sqlite3 moox_collector.db < 本脚本 → 换二进制并启动。
-- 启动时只执行 CREATE TABLE IF NOT EXISTS，不会补列也不会重建表；新版本启动检查发现库没有升级会拒绝启动。
-- 脚本在一个事务里，只能对未升级的库执行一次（列已存在时 ALTER 会失败，整体回滚）。
-- 本文件不是启动 schema，不会被载入。
BEGIN;
ALTER TABLE t_collector_backfill_notices ADD COLUMN c_series_tag TEXT NOT NULL DEFAULT '';
ALTER TABLE t_collector_resample_snapshots ADD COLUMN c_members TEXT NOT NULL DEFAULT '{}';
ALTER TABLE t_collector_resample_jobs ADD COLUMN c_series_tags TEXT NOT NULL DEFAULT '[]';
CREATE TABLE t_collector_source_absences_new (
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_series_tag TEXT NOT NULL DEFAULT '',
    c_from DATETIME NOT NULL,
    c_to DATETIME NOT NULL,
    c_bars INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_dataset_id, c_frequency, c_subject_id, c_series_tag, c_from)
);
INSERT INTO t_collector_source_absences_new (c_space_id, c_dataset_id, c_frequency, c_subject_id, c_from, c_to, c_bars, c_ctime)
    SELECT c_space_id, c_dataset_id, c_frequency, c_subject_id, c_from, c_to, c_bars, c_ctime FROM t_collector_source_absences;
DROP TABLE t_collector_source_absences;
ALTER TABLE t_collector_source_absences_new RENAME TO t_collector_source_absences;
CREATE TABLE t_collector_kline_backfill_chunks_new (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_job_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_series_tag TEXT NOT NULL DEFAULT '',
    c_from DATETIME NOT NULL,
    c_to DATETIME NOT NULL,
    c_chunk_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_attempts INTEGER NOT NULL DEFAULT 0,
    c_rows INTEGER NOT NULL DEFAULT 0,
    c_missing TEXT NOT NULL DEFAULT '',
    c_started_at DATETIME,
    c_latest_end_at DATETIME,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('pending', 'running', 'succeeded', 'failed', 'canceled')),
    UNIQUE (c_job_id, c_dataset_id, c_frequency, c_subject_id, c_series_tag, c_from)
);
INSERT INTO t_collector_kline_backfill_chunks_new (c_id, c_job_id, c_dataset_id, c_frequency, c_subject_id, c_from, c_to, c_chunk_id, c_status, c_attempts, c_rows, c_missing, c_started_at, c_latest_end_at, c_last_error, c_mtime)
    SELECT c_id, c_job_id, c_dataset_id, c_frequency, c_subject_id, c_from, c_to, c_chunk_id, c_status, c_attempts, c_rows, c_missing, c_started_at, c_latest_end_at, c_last_error, c_mtime FROM t_collector_kline_backfill_chunks;
DROP TABLE t_collector_kline_backfill_chunks;
ALTER TABLE t_collector_kline_backfill_chunks_new RENAME TO t_collector_kline_backfill_chunks;
COMMIT;
