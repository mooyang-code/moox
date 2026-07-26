PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_collector_task_rules (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_rule_id TEXT NOT NULL,
    c_data_type TEXT NOT NULL DEFAULT '',
    c_exchange TEXT NOT NULL DEFAULT '',
    c_collect_params TEXT NOT NULL DEFAULT '{}',
    c_enabled INTEGER NOT NULL DEFAULT 1,
    c_creator TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_rules_space_rule ON t_collector_task_rules (c_space_id, c_rule_id);
CREATE INDEX IF NOT EXISTS idx_collector_rules_space ON t_collector_task_rules (c_space_id);
CREATE INDEX IF NOT EXISTS idx_collector_rules_exchange ON t_collector_task_rules (c_exchange);
CREATE INDEX IF NOT EXISTS idx_collector_rules_type ON t_collector_task_rules (c_data_type);
CREATE INDEX IF NOT EXISTS idx_collector_rules_enabled ON t_collector_task_rules (c_enabled);

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
CREATE INDEX IF NOT EXISTS idx_collector_instances_job_item ON t_collector_task_instances (c_space_id, c_cloud_job_item_id);
CREATE INDEX IF NOT EXISTS idx_collector_instances_rule ON t_collector_task_instances (c_space_id, c_rule_id);
CREATE INDEX IF NOT EXISTS idx_collector_instances_subject ON t_collector_task_instances (c_space_id, c_dataset_id, c_subject_id);
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
