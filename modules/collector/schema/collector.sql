PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_collector_tasks (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_task_id TEXT NOT NULL,
    c_task_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_data_type TEXT NOT NULL DEFAULT '',
    c_definition_hash TEXT NOT NULL DEFAULT '',
    c_series_hash TEXT NOT NULL DEFAULT '',
    c_collect_params TEXT NOT NULL DEFAULT '{}',
    c_enabled INTEGER NOT NULL DEFAULT 1,
    c_creator TEXT NOT NULL DEFAULT '',
    c_prepare_state TEXT NOT NULL DEFAULT 'ready',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_result_dataset_id TEXT NOT NULL DEFAULT '',
    c_result_view_id TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_tasks_space_task ON t_collector_tasks (c_space_id, c_task_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_tasks_space_name ON t_collector_tasks (c_space_id, c_task_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_tasks_definition
ON t_collector_tasks (c_space_id, c_definition_hash)
WHERE c_definition_hash <> '';
CREATE INDEX IF NOT EXISTS idx_collector_tasks_space_result_view
ON t_collector_tasks (c_space_id, c_result_view_id)
WHERE c_result_view_id <> '';
CREATE INDEX IF NOT EXISTS idx_collector_tasks_space ON t_collector_tasks (c_space_id);

CREATE INDEX IF NOT EXISTS idx_collector_tasks_type ON t_collector_tasks (c_data_type);

CREATE INDEX IF NOT EXISTS idx_collector_tasks_enabled ON t_collector_tasks (c_enabled);

CREATE INDEX IF NOT EXISTS idx_collector_tasks_prepare ON t_collector_tasks (c_data_type, c_prepare_state, c_enabled);

CREATE TABLE IF NOT EXISTS t_collector_task_tags (
    c_space_id TEXT NOT NULL,
    c_task_id TEXT NOT NULL,
    c_tag_id TEXT NOT NULL,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_task_id, c_tag_id),
    FOREIGN KEY (c_space_id, c_task_id) REFERENCES t_collector_tasks (c_space_id, c_task_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_collector_task_tags_tag ON t_collector_task_tags (c_space_id, c_tag_id);

CREATE TABLE IF NOT EXISTS t_collector_task_series (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_task_id TEXT NOT NULL,
    c_series_index INTEGER NOT NULL,
    c_series_key TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_provider TEXT NOT NULL DEFAULT '',
    c_source_id TEXT NOT NULL DEFAULT '',
    c_market_type TEXT NOT NULL DEFAULT '',
    c_provider_symbol TEXT NOT NULL DEFAULT '',
    c_series_tag TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_task_id, c_series_key),
    UNIQUE (c_space_id, c_task_id, c_series_index),
    FOREIGN KEY (c_space_id, c_task_id) REFERENCES t_collector_tasks (c_space_id, c_task_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_collector_task_series_subject ON t_collector_task_series (c_space_id, c_subject_id);

CREATE TABLE IF NOT EXISTS t_collector_task_instances (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_instance_id TEXT NOT NULL,
    c_run_id TEXT NOT NULL DEFAULT '',
    c_request_key TEXT NOT NULL DEFAULT '',
    c_provider TEXT NOT NULL DEFAULT '',
    c_provider_symbol TEXT NOT NULL DEFAULT '',
    c_market_type TEXT NOT NULL DEFAULT '',
    c_data_type TEXT NOT NULL DEFAULT '',
    c_subject_id TEXT NOT NULL DEFAULT '',
    c_frequency TEXT NOT NULL DEFAULT '',
    c_target_data_time DATETIME,
    c_source_id TEXT NOT NULL DEFAULT '',
    c_series_tag TEXT NOT NULL DEFAULT '',
    c_function_name TEXT NOT NULL DEFAULT '',
    c_last_exec_status INTEGER NOT NULL DEFAULT 1,
    c_task_params TEXT NOT NULL DEFAULT '{}',
    c_last_exec_time DATETIME,
    c_result TEXT NOT NULL DEFAULT '{}',
    c_is_deleted INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_instances_space_instance
ON t_collector_task_instances (c_space_id, c_instance_id);

CREATE INDEX IF NOT EXISTS idx_collector_instances_subject
ON t_collector_task_instances (c_space_id, c_subject_id, c_frequency);

CREATE INDEX IF NOT EXISTS idx_collector_instances_storage_write
ON t_collector_task_instances (c_space_id, c_subject_id, c_frequency, c_series_tag, c_function_name, c_is_deleted);

CREATE INDEX IF NOT EXISTS idx_collector_instances_provider_status
ON t_collector_task_instances (c_space_id, c_provider, c_last_exec_status);

CREATE INDEX IF NOT EXISTS idx_collector_instances_market_subject
ON t_collector_task_instances (c_space_id, c_market_type, c_data_type, c_subject_id, c_frequency);

CREATE INDEX IF NOT EXISTS idx_collector_instances_function ON t_collector_task_instances (c_space_id, c_function_name);

CREATE INDEX IF NOT EXISTS idx_collector_instances_exec ON t_collector_task_instances (c_last_exec_status);

CREATE INDEX IF NOT EXISTS idx_collector_instances_deleted ON t_collector_task_instances (c_is_deleted);

CREATE INDEX IF NOT EXISTS idx_collector_instances_ctime ON t_collector_task_instances (c_ctime DESC);
CREATE INDEX IF NOT EXISTS idx_collector_instances_space_page
ON t_collector_task_instances (c_space_id, c_id DESC);
CREATE INDEX IF NOT EXISTS idx_collector_instances_list_page
ON t_collector_task_instances (c_space_id, c_is_deleted, c_id DESC);
CREATE INDEX IF NOT EXISTS idx_collector_instances_space_status_page
ON t_collector_task_instances (c_space_id, c_last_exec_status, c_id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_instances_run_request
ON t_collector_task_instances (c_space_id, c_run_id, c_request_key)
WHERE c_run_id <> '' AND c_request_key <> '';
CREATE INDEX IF NOT EXISTS idx_collector_instances_run
ON t_collector_task_instances (c_space_id, c_run_id, c_instance_id, c_last_exec_status);

CREATE INDEX IF NOT EXISTS idx_collector_instances_terminal_cleanup_space
ON t_collector_task_instances (c_space_id, c_mtime, c_id)
WHERE c_run_id <> '' AND c_last_exec_status IN (2, 3);

CREATE TABLE IF NOT EXISTS t_collector_instance_write_targets (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_write_target_id TEXT NOT NULL,
    c_instance_id TEXT NOT NULL,
    c_task_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_view_id TEXT NOT NULL DEFAULT '',
    c_output_fields_json TEXT NOT NULL DEFAULT '[]',
    c_series_index INTEGER NOT NULL DEFAULT 0,
    c_series_hash TEXT NOT NULL DEFAULT '',
    c_expected_count INTEGER NOT NULL DEFAULT 0,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_attempt INTEGER NOT NULL DEFAULT 0,
    c_next_retry_at DATETIME,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_write_target_id),
    UNIQUE (c_space_id, c_instance_id, c_task_id),
    FOREIGN KEY (c_space_id, c_instance_id) REFERENCES t_collector_task_instances (c_space_id, c_instance_id) ON DELETE CASCADE,
    FOREIGN KEY (c_space_id, c_task_id) REFERENCES t_collector_tasks (c_space_id, c_task_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_collector_write_targets_task
ON t_collector_instance_write_targets (c_space_id, c_task_id, c_status);

CREATE INDEX IF NOT EXISTS idx_collector_write_targets_dataset
ON t_collector_instance_write_targets (c_space_id, c_dataset_id, c_status);

CREATE INDEX IF NOT EXISTS idx_collector_write_targets_dataset_target
ON t_collector_instance_write_targets (c_space_id, c_dataset_id, c_write_target_id);

CREATE INDEX IF NOT EXISTS idx_collector_write_targets_dataset_instance
ON t_collector_instance_write_targets (c_space_id, c_dataset_id, c_instance_id);

CREATE INDEX IF NOT EXISTS idx_collector_write_targets_retention_space
ON t_collector_instance_write_targets (c_space_id, c_mtime, c_id);

CREATE TRIGGER IF NOT EXISTS update_collector_tasks_mtime
AFTER UPDATE ON t_collector_tasks
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_collector_tasks SET c_mtime = STRFTIME('%Y-%m-%d %H:%M:%f', 'now') WHERE rowid = NEW.rowid;
END;

CREATE TRIGGER IF NOT EXISTS update_collector_instances_mtime
AFTER UPDATE ON t_collector_task_instances
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_collector_task_instances SET c_mtime = STRFTIME('%Y-%m-%d %H:%M:%f', 'now') WHERE rowid = NEW.rowid;
END;

-- Period readiness is the Collector's durable answer to whether all
-- expected Storage writes for one market period have reached a terminal
-- state. The task identity/source columns make the expected assignment
-- immutable even when the next scheduler reconciliation moves a task.
CREATE TABLE IF NOT EXISTS t_period_readiness (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_work_type TEXT NOT NULL DEFAULT 'collection',
    c_period_time DATETIME NOT NULL,
    c_deadline_at DATETIME NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'waiting',
    c_report_state TEXT NOT NULL DEFAULT 'waiting',
    c_event_id TEXT NOT NULL DEFAULT '',
    c_collected_at DATETIME,
    c_payload_json TEXT NOT NULL DEFAULT '{}',
    c_committed_positions_json TEXT NOT NULL DEFAULT '[]',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('waiting', 'complete', 'degraded')),
    CHECK (c_report_state IN ('waiting', 'pending', 'reported')),
    UNIQUE (c_space_id, c_dataset_id, c_frequency, c_period_time)
);

CREATE INDEX IF NOT EXISTS idx_period_readiness_report ON t_period_readiness (c_report_state, c_deadline_at);

CREATE INDEX IF NOT EXISTS idx_period_readiness_pending_scope
ON t_period_readiness (c_space_id, c_report_state, c_dataset_id, c_frequency);

CREATE INDEX IF NOT EXISTS idx_period_readiness_waiting_scope
ON t_period_readiness (c_space_id, c_report_state, c_deadline_at, c_id);

CREATE INDEX IF NOT EXISTS idx_period_readiness_completed_scope
ON t_period_readiness (c_space_id, c_dataset_id, c_frequency, c_status, c_period_time DESC, c_id DESC);

CREATE INDEX IF NOT EXISTS idx_period_readiness_retention
ON t_period_readiness (c_space_id, c_report_state, c_collected_at, c_id);

CREATE INDEX IF NOT EXISTS idx_period_readiness_item_retention
ON t_period_readiness (c_space_id, c_report_state, c_dataset_id, c_frequency, c_period_time DESC, c_id DESC);

CREATE TABLE IF NOT EXISTS t_period_readiness_items (
    c_readiness_id INTEGER NOT NULL,
    c_instance_id TEXT NOT NULL,
    c_write_target_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_series_tag TEXT NOT NULL DEFAULT '',
    c_function_name TEXT NOT NULL DEFAULT '',
    c_write_source TEXT NOT NULL DEFAULT '',
    c_required_fields_json TEXT NOT NULL DEFAULT '[]',
    c_state TEXT NOT NULL DEFAULT 'pending',
    c_updated_at DATETIME NOT NULL,
    PRIMARY KEY (c_readiness_id, c_write_target_id),
    CHECK (c_state IN ('pending', 'success', 'timed_out')),
    FOREIGN KEY (c_readiness_id) REFERENCES t_period_readiness (c_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_period_readiness_items_state ON t_period_readiness_items (c_readiness_id, c_state);

CREATE INDEX IF NOT EXISTS idx_period_readiness_items_target
ON t_period_readiness_items (c_readiness_id, c_write_target_id, c_state);

CREATE INDEX IF NOT EXISTS idx_period_readiness_items_write_target
ON t_period_readiness_items (c_write_target_id, c_readiness_id);

-- SCF 定时采集的采集计划：每个空间同一时间最多一份未生效的计划，最近一份已生效的计划即“已生效状态”。
CREATE TABLE IF NOT EXISTS t_collector_fetch_plans (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_plan TEXT NOT NULL,
    c_progress TEXT NOT NULL DEFAULT '{}',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('pending', 'applying', 'applied'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_t_collector_fetch_plans_open
ON t_collector_fetch_plans (c_space_id)
WHERE c_status <> 'applied';
CREATE INDEX IF NOT EXISTS idx_t_collector_fetch_plans_space ON t_collector_fetch_plans (c_space_id, c_id);

CREATE TRIGGER IF NOT EXISTS trg_t_collector_fetch_plans_mtime
AFTER UPDATE ON t_collector_fetch_plans
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_collector_fetch_plans SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- K 线补采作业：一个作业拆成若干（数据集、标的、时间段）分块。
CREATE TABLE IF NOT EXISTS t_collector_backfill_jobs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_job_id TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_source TEXT NOT NULL DEFAULT 'manual',
    c_request TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_source IN ('manual', 'auto')),
    CHECK (c_status IN ('pending', 'running', 'succeeded', 'failed', 'canceled')),
    UNIQUE (c_job_id)
);

CREATE INDEX IF NOT EXISTS idx_t_collector_backfill_jobs_space ON t_collector_backfill_jobs (c_space_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_collector_backfill_jobs_mtime
AFTER UPDATE ON t_collector_backfill_jobs
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_collector_backfill_jobs SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_collector_backfill_chunks (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_job_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_from DATETIME NOT NULL,
    c_to DATETIME NOT NULL,
    c_chunk_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_attempts INTEGER NOT NULL DEFAULT 0,
    c_rows INTEGER NOT NULL DEFAULT 0,
    c_started_at DATETIME,
    c_latest_end_at DATETIME,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('pending', 'running', 'succeeded', 'failed', 'canceled')),
    UNIQUE (c_job_id, c_dataset_id, c_frequency, c_subject_id, c_from)
);

CREATE INDEX IF NOT EXISTS idx_t_collector_backfill_chunks_job ON t_collector_backfill_chunks (c_job_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_collector_backfill_chunks_mtime
AFTER UPDATE ON t_collector_backfill_chunks
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_collector_backfill_chunks SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- 自动补采的扫描水位：已扫描到哪个时间点。
CREATE TABLE IF NOT EXISTS t_collector_backfill_watermarks (
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_scanned_until DATETIME NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_dataset_id, c_frequency)
);
