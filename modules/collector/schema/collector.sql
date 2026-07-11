PRAGMA foreign_keys = ON;

DROP TABLE IF EXISTS t_collector_execution_logs;

CREATE TABLE IF NOT EXISTS t_collector_task_rules (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_rule_id TEXT NOT NULL,
    c_data_type TEXT NOT NULL DEFAULT '',
    c_exchange TEXT NOT NULL DEFAULT '',
    c_market_id TEXT NOT NULL DEFAULT '',
    c_feed TEXT NOT NULL DEFAULT '',
    c_instrument_types TEXT NOT NULL DEFAULT '[]',
    c_frequencies TEXT NOT NULL DEFAULT '[]',
    c_history_start TEXT NOT NULL DEFAULT '',
    c_history_end TEXT NOT NULL DEFAULT '',
    c_subject_filters TEXT NOT NULL DEFAULT '[]',
    c_exchange_filters TEXT NOT NULL DEFAULT '[]',
    c_schedule_spec TEXT NOT NULL DEFAULT '{}',
    c_collect_params TEXT NOT NULL DEFAULT '{}',
    c_assignment_type TEXT NOT NULL DEFAULT 'auto',
    c_assigned_nodes TEXT NOT NULL DEFAULT '[]',
    c_node_pattern TEXT NOT NULL DEFAULT '',
    c_node_tags TEXT NOT NULL DEFAULT '[]',
    c_enabled INTEGER NOT NULL DEFAULT 1,
    c_creator TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_rules_space_rule ON t_collector_task_rules(c_space_id, c_rule_id);
CREATE INDEX IF NOT EXISTS idx_collector_rules_space ON t_collector_task_rules(c_space_id);
CREATE INDEX IF NOT EXISTS idx_collector_rules_exchange ON t_collector_task_rules(c_exchange);
CREATE INDEX IF NOT EXISTS idx_collector_rules_type ON t_collector_task_rules(c_data_type);
CREATE INDEX IF NOT EXISTS idx_collector_rules_enabled ON t_collector_task_rules(c_enabled);

CREATE TABLE IF NOT EXISTS t_collector_task_instances (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_task_id TEXT NOT NULL,
    c_cloud_job_item_id TEXT NOT NULL DEFAULT '',
    c_rule_id TEXT NOT NULL,
    c_exchange TEXT NOT NULL DEFAULT '',
    c_market TEXT NOT NULL DEFAULT '',
    c_data_type TEXT NOT NULL DEFAULT '',
    c_dataset_id TEXT NOT NULL DEFAULT '',
    c_subject_id TEXT NOT NULL DEFAULT '',
    c_symbol TEXT NOT NULL DEFAULT '',
    c_interval TEXT NOT NULL DEFAULT 'default',
    c_planned_exec_node TEXT NOT NULL DEFAULT '',
    c_last_exec_node TEXT NOT NULL DEFAULT '',
    c_last_exec_status INTEGER NOT NULL DEFAULT 1,
    c_task_params TEXT NOT NULL DEFAULT '{}',
    c_last_exec_time DATETIME,
    c_result TEXT NOT NULL DEFAULT '{}',
    c_is_deleted INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_instances_space_task ON t_collector_task_instances(c_space_id, c_task_id);
CREATE INDEX IF NOT EXISTS idx_collector_instances_job_item ON t_collector_task_instances(c_space_id, c_cloud_job_item_id);
CREATE INDEX IF NOT EXISTS idx_collector_instances_rule ON t_collector_task_instances(c_space_id, c_rule_id);
CREATE INDEX IF NOT EXISTS idx_collector_instances_subject ON t_collector_task_instances(c_space_id, c_dataset_id, c_subject_id);
CREATE INDEX IF NOT EXISTS idx_collector_instances_exec ON t_collector_task_instances(c_planned_exec_node, c_last_exec_status);
CREATE INDEX IF NOT EXISTS idx_collector_instances_deleted ON t_collector_task_instances(c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_collector_instances_ctime ON t_collector_task_instances(c_ctime DESC);

CREATE TRIGGER IF NOT EXISTS update_collector_rules_mtime AFTER UPDATE ON t_collector_task_rules BEGIN
    UPDATE t_collector_task_rules SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;
CREATE TRIGGER IF NOT EXISTS update_collector_instances_mtime AFTER UPDATE ON t_collector_task_instances BEGIN
    UPDATE t_collector_task_instances SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

CREATE TABLE IF NOT EXISTS t_collector_market_leases (
    c_lease_id TEXT NOT NULL PRIMARY KEY,
    c_lease_type TEXT NOT NULL,
    c_lease_key TEXT NOT NULL,
    c_epoch INTEGER NOT NULL,
    c_owner_id TEXT NOT NULL,
    c_expires_at DATETIME NOT NULL,
    c_quarantine_until DATETIME,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_market_lease_key ON t_collector_market_leases(c_lease_type, c_lease_key);

CREATE TABLE IF NOT EXISTS t_collector_provider_quota_windows (
    c_provider_id TEXT NOT NULL,
    c_scope_key TEXT NOT NULL,
    c_endpoint_class TEXT NOT NULL,
    c_window_seconds INTEGER NOT NULL,
    c_window_start DATETIME NOT NULL,
    c_consumed INTEGER NOT NULL DEFAULT 0,
    c_limit_value INTEGER NOT NULL,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(c_provider_id, c_scope_key, c_endpoint_class, c_window_seconds, c_window_start)
);

CREATE TABLE IF NOT EXISTS t_collector_provider_permits (
    c_execution_nonce TEXT NOT NULL,
    c_request_index INTEGER NOT NULL,
    c_provider_id TEXT NOT NULL,
    c_permit_id TEXT NOT NULL,
    c_lease_epoch INTEGER NOT NULL,
    c_allowed INTEGER NOT NULL,
    c_not_before DATETIME,
    c_expires_at DATETIME NOT NULL,
    c_denial_reason TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(c_execution_nonce, c_request_index)
);

CREATE TABLE IF NOT EXISTS t_collector_task_attempts (
    c_job_item_id TEXT NOT NULL, c_attempt_no INTEGER NOT NULL, c_plan_id TEXT NOT NULL DEFAULT '',
    c_market_id TEXT NOT NULL DEFAULT '', c_space_id TEXT NOT NULL DEFAULT '', c_provider_id TEXT NOT NULL DEFAULT '',
    c_feed TEXT NOT NULL DEFAULT '', c_phase TEXT NOT NULL DEFAULT '', c_window_start DATETIME, c_window_end DATETIME,
    c_cursor TEXT NOT NULL DEFAULT '', c_status TEXT NOT NULL DEFAULT '', c_summary TEXT NOT NULL DEFAULT '{}',
    c_error_class TEXT NOT NULL DEFAULT '', c_finalized INTEGER NOT NULL DEFAULT 0, c_finalized_at DATETIME,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(c_job_item_id, c_attempt_no)
);
CREATE INDEX IF NOT EXISTS idx_market_attempt_space_status ON t_collector_task_attempts(c_space_id, c_status, c_mtime);

CREATE TABLE IF NOT EXISTS t_collector_attempt_subjects (
    c_job_item_id TEXT NOT NULL, c_attempt_no INTEGER NOT NULL, c_task_id TEXT NOT NULL, c_subject_id TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT '', c_next_candidate_index INTEGER NOT NULL DEFAULT 0, c_rows INTEGER NOT NULL DEFAULT 0,
    c_error_class TEXT NOT NULL DEFAULT '', PRIMARY KEY(c_job_item_id, c_attempt_no, c_task_id)
);

CREATE TABLE IF NOT EXISTS t_collector_attempt_outbox (
    c_outbox_id TEXT NOT NULL PRIMARY KEY, c_parent_job_item_id TEXT NOT NULL, c_parent_attempt_no INTEGER NOT NULL,
    c_kind TEXT NOT NULL, c_payload TEXT NOT NULL DEFAULT '{}', c_status TEXT NOT NULL DEFAULT 'pending',
    c_next_attempt_at DATETIME NOT NULL, c_published_job_item_id TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_market_outbox_pending ON t_collector_attempt_outbox(c_status, c_next_attempt_at);

CREATE TABLE IF NOT EXISTS t_collector_provider_runtime (
    c_provider_id TEXT NOT NULL, c_scope_key TEXT NOT NULL, c_circuit_state TEXT NOT NULL DEFAULT 'closed',
    c_consecutive_errors INTEGER NOT NULL DEFAULT 0, c_opened_at DATETIME, c_probe_in_flight INTEGER NOT NULL DEFAULT 0,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(c_provider_id, c_scope_key)
);
CREATE TABLE IF NOT EXISTS t_collector_market_generations (
    c_generation_key TEXT NOT NULL PRIMARY KEY, c_epoch INTEGER NOT NULL, c_generation DATETIME NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'active', c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS t_collector_control_leader (
    c_name TEXT NOT NULL PRIMARY KEY, c_owner_id TEXT NOT NULL, c_epoch INTEGER NOT NULL,
    c_expires_at DATETIME NOT NULL, c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);
