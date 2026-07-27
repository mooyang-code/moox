// Package jobhistory maintains CloudNode JobItem terminal-state day databases.
package jobhistory

const SchemaSQL = `
CREATE TABLE IF NOT EXISTS t_cloud_job_items (
    c_space_id TEXT NOT NULL DEFAULT '',
    c_job_id TEXT NOT NULL DEFAULT '',
    c_job_item_id TEXT NOT NULL,
    c_job_type TEXT NOT NULL DEFAULT '',
    c_params TEXT NOT NULL DEFAULT '{}',
    c_priority INTEGER NOT NULL DEFAULT 0,
    c_execute_at DATETIME,
    c_status TEXT NOT NULL DEFAULT '',
    c_result_summary TEXT NOT NULL DEFAULT '{}',
    c_last_error_kind TEXT NOT NULL DEFAULT '',
    c_last_error_code TEXT NOT NULL DEFAULT '',
    c_last_error_message TEXT NOT NULL DEFAULT '',
    c_duration_ms INTEGER NOT NULL DEFAULT 0,
    c_execution_node TEXT NOT NULL DEFAULT '',
    c_finish_time DATETIME,
    c_ctime DATETIME,
    c_mtime DATETIME,
    PRIMARY KEY (c_space_id, c_job_item_id)
);
`
