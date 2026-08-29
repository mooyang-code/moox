PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_collector_task_rules (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_rule_id TEXT NOT NULL,
    c_data_type TEXT NOT NULL DEFAULT '',
    c_provider TEXT NOT NULL DEFAULT '',
    c_market_type TEXT NOT NULL DEFAULT '',
    c_collect_params TEXT NOT NULL DEFAULT '{}',
    c_enabled INTEGER NOT NULL DEFAULT 1,
    c_creator TEXT NOT NULL DEFAULT '',
    c_prepare_state TEXT NOT NULL DEFAULT 'ready',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_rules_space_rule ON t_collector_task_rules (c_space_id, c_rule_id);
CREATE INDEX IF NOT EXISTS idx_collector_rules_space ON t_collector_task_rules (c_space_id);
CREATE INDEX IF NOT EXISTS idx_collector_rules_provider ON t_collector_task_rules (c_provider);
CREATE INDEX IF NOT EXISTS idx_collector_rules_market_type ON t_collector_task_rules (c_market_type);
CREATE INDEX IF NOT EXISTS idx_collector_rules_type ON t_collector_task_rules (c_data_type);
CREATE INDEX IF NOT EXISTS idx_collector_rules_enabled ON t_collector_task_rules (c_enabled);
CREATE INDEX IF NOT EXISTS idx_collector_rules_prepare ON t_collector_task_rules (c_data_type, c_prepare_state, c_enabled);

CREATE TABLE IF NOT EXISTS t_collector_task_instances (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_task_id TEXT NOT NULL,
    c_rule_id TEXT NOT NULL,
    c_provider TEXT NOT NULL DEFAULT '',
    c_market_type TEXT NOT NULL DEFAULT '',
    c_data_type TEXT NOT NULL DEFAULT '',
    c_dataset_id TEXT NOT NULL DEFAULT '',
    c_subject_id TEXT NOT NULL DEFAULT '',
    c_frequency TEXT NOT NULL DEFAULT '',
    c_function_name TEXT NOT NULL DEFAULT '',
    c_last_exec_node TEXT NOT NULL DEFAULT '',
    c_last_exec_status INTEGER NOT NULL DEFAULT 1,
    c_task_params TEXT NOT NULL DEFAULT '{}',
    c_last_exec_time DATETIME,
    c_result TEXT NOT NULL DEFAULT '{}',
    c_is_deleted INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_instances_space_task ON t_collector_task_instances (c_space_id, c_task_id);
CREATE INDEX IF NOT EXISTS idx_collector_instances_rule ON t_collector_task_instances (c_space_id, c_rule_id);
CREATE INDEX IF NOT EXISTS idx_collector_instances_subject ON t_collector_task_instances (c_space_id, c_dataset_id, c_subject_id);
CREATE INDEX IF NOT EXISTS idx_collector_instances_function ON t_collector_task_instances (c_space_id, c_function_name);
CREATE INDEX IF NOT EXISTS idx_collector_instances_exec ON t_collector_task_instances (c_last_exec_status);
CREATE INDEX IF NOT EXISTS idx_collector_instances_deleted ON t_collector_task_instances (c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_collector_instances_ctime ON t_collector_task_instances (c_ctime DESC);

CREATE TRIGGER IF NOT EXISTS update_collector_rules_mtime
AFTER UPDATE ON t_collector_task_rules
BEGIN
    UPDATE t_collector_task_rules SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;

CREATE TRIGGER IF NOT EXISTS update_collector_instances_mtime
AFTER UPDATE ON t_collector_task_instances
BEGIN
    UPDATE t_collector_task_instances SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;

CREATE TABLE IF NOT EXISTS t_collector_fetch_batches (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_batch_id TEXT NOT NULL,
    c_parent_batch_id TEXT NOT NULL DEFAULT '',
    c_schedule_id TEXT NOT NULL,
    c_batch_kind TEXT NOT NULL,
    c_shard_index INTEGER NOT NULL,
    c_rule_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_region TEXT NOT NULL,
    c_node_id TEXT NOT NULL,
    c_function_name TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_attempt INTEGER NOT NULL DEFAULT 1,
    c_request_id TEXT NOT NULL DEFAULT '',
    c_request_json TEXT NOT NULL DEFAULT '{}',
    c_planned_count INTEGER NOT NULL DEFAULT 0,
    c_success_count INTEGER NOT NULL DEFAULT 0,
    c_retry_count INTEGER NOT NULL DEFAULT 0,
    c_permanent_failed_count INTEGER NOT NULL DEFAULT 0,
    c_error_summary TEXT NOT NULL DEFAULT '',
    c_late_completion INTEGER NOT NULL DEFAULT 0,
    c_planned_at DATETIME,
    c_dispatched_at DATETIME,
    c_deadline_at DATETIME,
    c_completed_at DATETIME,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_fetch_batch
ON t_collector_fetch_batches (c_space_id, c_batch_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_fetch_schedule_shard
ON t_collector_fetch_batches
(c_space_id, c_schedule_id, c_batch_kind, c_shard_index, c_attempt);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_batch_deadline
ON t_collector_fetch_batches (c_status, c_deadline_at);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_batch_schedule
ON t_collector_fetch_batches (c_space_id, c_schedule_id);

CREATE TABLE IF NOT EXISTS t_collector_fetch_retry_items (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_retry_key TEXT NOT NULL,
  c_source_batch_id TEXT NOT NULL,
  c_batch_kind TEXT NOT NULL DEFAULT 'realtime',
    c_rule_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_target_data_time DATETIME NOT NULL,
    c_task_json TEXT NOT NULL DEFAULT '{}',
    c_attempt INTEGER NOT NULL DEFAULT 1,
    c_status TEXT NOT NULL,
    c_next_retry_at DATETIME,
    c_last_error_type TEXT NOT NULL DEFAULT '',
    c_last_error_summary TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_fetch_retry
ON t_collector_fetch_retry_items (c_space_id, c_retry_key);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_retry_due
ON t_collector_fetch_retry_items (c_status, c_next_retry_at);

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
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('waiting', 'complete', 'degraded')),
    CHECK (c_report_state IN ('waiting', 'pending', 'reported')),
    UNIQUE (c_space_id, c_dataset_id, c_frequency, c_period_time)
);

CREATE INDEX IF NOT EXISTS idx_period_readiness_report
ON t_period_readiness (c_report_state, c_deadline_at);

CREATE TABLE IF NOT EXISTS t_period_readiness_items (
    c_readiness_id INTEGER NOT NULL,
    c_task_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_function_name TEXT NOT NULL DEFAULT '',
    c_write_source TEXT NOT NULL DEFAULT '',
    c_required_fields_json TEXT NOT NULL DEFAULT '[]',
    c_state TEXT NOT NULL DEFAULT 'pending',
    c_updated_at DATETIME NOT NULL,
    PRIMARY KEY (c_readiness_id, c_task_id),
    UNIQUE (c_readiness_id, c_subject_id),
    CHECK (c_state IN ('pending', 'success', 'timed_out')),
    FOREIGN KEY (c_readiness_id) REFERENCES t_period_readiness(c_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_period_readiness_items_state
ON t_period_readiness_items (c_readiness_id, c_state);
