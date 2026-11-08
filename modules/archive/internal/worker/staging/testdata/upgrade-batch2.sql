-- 归档批 2 原地升级（路 B）：在批 1 的 worker 暂存库上执行，结果与新建的暂存库一致（见 upgrade_test.go）。
BEGIN;
ALTER TABLE partition_state ADD COLUMN remote_key TEXT NOT NULL DEFAULT '';
ALTER TABLE partition_state ADD COLUMN remote_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE partition_state ADD COLUMN remote_etag TEXT NOT NULL DEFAULT '';
ALTER TABLE partition_state ADD COLUMN remote_uploaded_at INTEGER;
ALTER TABLE partition_state ADD COLUMN remote_verified_at INTEGER;
ALTER TABLE partition_state ADD COLUMN remote_error TEXT NOT NULL DEFAULT '';
ALTER TABLE partition_state ADD COLUMN remote_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE partition_state ADD COLUMN remote_retry_at INTEGER;
ALTER TABLE task_state ADD COLUMN meta_remote_sha256 TEXT NOT NULL DEFAULT '';
COMMIT;
PRAGMA integrity_check;
PRAGMA wal_checkpoint(TRUNCATE);
