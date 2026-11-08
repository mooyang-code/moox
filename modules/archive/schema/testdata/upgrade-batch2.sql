-- 归档批 2 原地升级（路 B）：在批 1 的 archive-mgr 库上执行，结果与新 schema 建出的库一致（见 schema_test.go）。
BEGIN;
ALTER TABLE t_archive_partitions ADD COLUMN c_remote_key TEXT NOT NULL DEFAULT '';
ALTER TABLE t_archive_partitions ADD COLUMN c_remote_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE t_archive_partitions ADD COLUMN c_remote_etag TEXT NOT NULL DEFAULT '';
ALTER TABLE t_archive_partitions ADD COLUMN c_remote_uploaded_at DATETIME;
ALTER TABLE t_archive_partitions ADD COLUMN c_remote_verified_at DATETIME;
ALTER TABLE t_archive_partitions ADD COLUMN c_remote_error TEXT NOT NULL DEFAULT '';
DROP INDEX IF EXISTS idx_t_archive_partitions_worker;
ALTER TABLE t_archive_runs ADD COLUMN c_partitions_uploaded INTEGER NOT NULL DEFAULT 0;
ALTER TABLE t_archive_workers ADD COLUMN c_object_store_json TEXT NOT NULL DEFAULT '{}';
COMMIT;
PRAGMA integrity_check;
PRAGMA wal_checkpoint(TRUNCATE);
