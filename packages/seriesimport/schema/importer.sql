-- 导入任务的断点：每个 (任务, 文件, 目标) 记录已经写完的批数。
-- 整段替换天然幂等，断点只用来跳过已经写完的批，重跑不会重复写。
CREATE TABLE IF NOT EXISTS t_import_checkpoints (
    c_job_id TEXT NOT NULL,
    c_file TEXT NOT NULL,
    c_target TEXT NOT NULL,
    c_batches_done INTEGER NOT NULL DEFAULT 0,
    c_rows_done INTEGER NOT NULL DEFAULT 0,
    c_status TEXT NOT NULL DEFAULT 'running',
    c_error TEXT NOT NULL DEFAULT '',
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('running', 'done', 'failed')),
    PRIMARY KEY (c_job_id, c_file, c_target)
);
