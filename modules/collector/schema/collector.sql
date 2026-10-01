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
    c_coverage_start_time DATETIME,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_tasks_space_task ON t_collector_tasks (c_space_id, c_task_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_tasks_space_name ON t_collector_tasks (c_space_id, c_task_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_tasks_definition
ON t_collector_tasks (c_space_id, c_definition_hash)
WHERE c_definition_hash <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_tasks_space_result_view
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

-- Period series freezes the Dataset/frequency snapshot for its market period.
CREATE TABLE IF NOT EXISTS t_collector_task_period_series (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_period_time DATETIME NOT NULL,
    c_series_index INTEGER NOT NULL,
    c_series_key TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_provider TEXT NOT NULL,
    c_source_id TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_provider_symbol TEXT NOT NULL,
    c_series_tag TEXT NOT NULL DEFAULT '',
    c_series_hash TEXT NOT NULL,
    c_expected_count INTEGER NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_series_index >= 0),
    CHECK (c_expected_count > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_task_period_series_lookup
ON t_collector_task_period_series (c_space_id, c_dataset_id, c_frequency, c_period_time, c_series_index);
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_task_period_series_key
ON t_collector_task_period_series (c_space_id, c_dataset_id, c_frequency, c_period_time, c_series_key);

CREATE INDEX IF NOT EXISTS idx_collector_task_period_series_retention
ON t_collector_task_period_series (c_period_time, c_space_id, c_dataset_id, c_frequency);

CREATE INDEX IF NOT EXISTS idx_collector_task_period_series_space_retention
ON t_collector_task_period_series (c_space_id, c_period_time, c_dataset_id, c_frequency);

CREATE TABLE IF NOT EXISTS t_collector_period_storage_states (
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_period_time DATETIME NOT NULL,
    c_series_hash TEXT NOT NULL,
    c_expected_count INTEGER NOT NULL,
    c_deadline_at DATETIME NOT NULL,
    c_status TEXT NOT NULL,
    c_confirmed_at DATETIME NOT NULL,
    CHECK (c_expected_count > 0),
    CHECK (c_status IN ('waiting', 'complete', 'degraded')),
    PRIMARY KEY (c_space_id, c_dataset_id, c_frequency, c_period_time)
);

-- A Timer period batch is the immutable claimable owner of one period shard.
-- Claim columns are populated only by the planned-to-dispatched claim CAS.
CREATE TABLE IF NOT EXISTS t_collector_timer_period_batches (
    c_key TEXT NOT NULL PRIMARY KEY,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_period_time DATETIME NOT NULL,
    c_task_id TEXT NOT NULL,
    c_first_run_id TEXT NOT NULL,
    c_series_hash TEXT NOT NULL,
    c_expected_count INTEGER NOT NULL,
    c_group_id INTEGER NOT NULL,
    c_group_count INTEGER NOT NULL,
    c_shard_index INTEGER NOT NULL,
    c_binding_hash TEXT NOT NULL,
    c_route_version TEXT NOT NULL,
    c_batch_id TEXT NOT NULL,
    c_function_name TEXT NOT NULL,
    c_node_id TEXT NOT NULL,
    c_region TEXT NOT NULL,
    c_claim_request_id TEXT NOT NULL DEFAULT '',
    c_claimed_at DATETIME,
    c_deadline_at DATETIME NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_expected_count > 0),
    CHECK (c_group_count > 0),
    CHECK (c_group_id < c_group_count),
    UNIQUE (c_space_id, c_dataset_id, c_frequency, c_period_time, c_shard_index),
    UNIQUE (c_space_id, c_batch_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_timer_period_batches_claim_request
ON t_collector_timer_period_batches (c_space_id, c_function_name, c_claim_request_id)
WHERE c_claim_request_id <> '';
CREATE INDEX IF NOT EXISTS idx_collector_timer_period_batches_candidate
ON t_collector_timer_period_batches (c_space_id, c_function_name, c_group_id, c_group_count, c_binding_hash, c_period_time, c_dataset_id, c_frequency, c_shard_index);

CREATE TABLE IF NOT EXISTS t_collector_runs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_run_id TEXT NOT NULL,
    c_run_key TEXT NOT NULL,
    c_run_type TEXT NOT NULL DEFAULT 'scheduled',
    c_frequency TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'planned',
    c_target_time DATETIME,
    c_error_summary TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_run_id),
    UNIQUE (c_space_id, c_run_key)
);

CREATE INDEX IF NOT EXISTS idx_collector_runs_status ON t_collector_runs (c_space_id, c_status);

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

CREATE INDEX IF NOT EXISTS idx_collector_instances_provider_status
ON t_collector_task_instances (c_space_id, c_provider, c_last_exec_status);

CREATE INDEX IF NOT EXISTS idx_collector_instances_market_subject
ON t_collector_task_instances (c_space_id, c_market_type, c_data_type, c_subject_id, c_frequency);

CREATE INDEX IF NOT EXISTS idx_collector_instances_function ON t_collector_task_instances (c_space_id, c_function_name);

CREATE INDEX IF NOT EXISTS idx_collector_instances_exec ON t_collector_task_instances (c_last_exec_status);

CREATE INDEX IF NOT EXISTS idx_collector_instances_deleted ON t_collector_task_instances (c_is_deleted);

CREATE INDEX IF NOT EXISTS idx_collector_instances_ctime ON t_collector_task_instances (c_ctime DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_instances_run_request
ON t_collector_task_instances (c_space_id, c_run_id, c_request_key)
WHERE c_run_id <> '' AND c_request_key <> '';
CREATE INDEX IF NOT EXISTS idx_collector_instances_run
ON t_collector_task_instances (c_space_id, c_run_id, c_instance_id, c_last_exec_status);

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

CREATE TABLE IF NOT EXISTS t_collector_fetch_batches (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_batch_id TEXT NOT NULL,
    c_parent_batch_id TEXT NOT NULL DEFAULT '',
    c_schedule_id TEXT NOT NULL,
    c_batch_kind TEXT NOT NULL,
    c_shard_index INTEGER NOT NULL,
    c_instance_id TEXT NOT NULL DEFAULT '',
    c_write_target_id TEXT NOT NULL DEFAULT '',
    c_retry_scope TEXT NOT NULL DEFAULT '',
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

CREATE TABLE IF NOT EXISTS t_collector_fetch_batch_items (
    c_space_id TEXT NOT NULL,
    c_batch_id TEXT NOT NULL,
    c_instance_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_error TEXT NOT NULL DEFAULT '',
    c_completed_at DATETIME,
    PRIMARY KEY (c_space_id, c_batch_id, c_instance_id)
);

CREATE INDEX IF NOT EXISTS idx_collector_batch_items_instance
ON t_collector_fetch_batch_items (c_space_id, c_instance_id);

CREATE INDEX IF NOT EXISTS idx_collector_batch_items_instance_batch
ON t_collector_fetch_batch_items (c_space_id, c_instance_id, c_batch_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_fetch_batch ON t_collector_fetch_batches (c_space_id, c_batch_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_fetch_schedule_shard
ON t_collector_fetch_batches (c_space_id, c_schedule_id, c_batch_kind, c_shard_index, c_attempt);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_batch_deadline ON t_collector_fetch_batches (c_status, c_deadline_at);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_batch_due_scope
ON t_collector_fetch_batches (c_space_id, c_status, c_deadline_at);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_batch_schedule ON t_collector_fetch_batches (c_space_id, c_schedule_id);

CREATE TABLE IF NOT EXISTS t_collector_fetch_retry_items (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_retry_key TEXT NOT NULL,
    c_source_batch_id TEXT NOT NULL,
    c_batch_kind TEXT NOT NULL DEFAULT 'realtime',
    c_instance_id TEXT NOT NULL DEFAULT '',
    c_write_target_id TEXT NOT NULL DEFAULT '',
    c_retry_scope TEXT NOT NULL DEFAULT '',
    c_subject_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_target_data_time DATETIME NOT NULL,
    c_task_json TEXT NOT NULL DEFAULT '{}',
    c_failure_targets_json TEXT NOT NULL DEFAULT '[]',
    c_attempt INTEGER NOT NULL DEFAULT 1,
    c_status TEXT NOT NULL,
    c_period_failure_report_state TEXT NOT NULL DEFAULT 'pending',
    c_period_failure_results_json TEXT NOT NULL DEFAULT '[]',
    c_period_failure_last_error TEXT NOT NULL DEFAULT '',
    c_period_failure_deadline_exceeded_at DATETIME,
    c_next_retry_at DATETIME,
    c_last_error_type TEXT NOT NULL DEFAULT '',
    c_last_error_summary TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_period_failure_report_state IN ('pending', 'acknowledged', 'missed_deadline'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_fetch_retry ON t_collector_fetch_retry_items (c_space_id, c_retry_key);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_retry_due ON t_collector_fetch_retry_items (c_status, c_next_retry_at);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_retry_target
ON t_collector_fetch_retry_items (c_space_id, c_write_target_id, c_status);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_retry_pending_scope
ON t_collector_fetch_retry_items (c_space_id, c_status, c_frequency, c_write_target_id, c_instance_id);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_retry_due_scope
ON t_collector_fetch_retry_items (c_space_id, c_status, c_next_retry_at);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_retry_period_failure
ON t_collector_fetch_retry_items (c_space_id, c_status, c_period_failure_report_state, c_retry_key);

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
