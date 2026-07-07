PRAGMA foreign_keys = ON;

DROP TABLE IF EXISTS t_cloud_invocation_results;
DROP TABLE IF EXISTS t_cloud_invocations;
DROP TABLE IF EXISTS t_cloud_job_item_attempts;
DROP TABLE IF EXISTS t_cloud_job_items;

CREATE TABLE IF NOT EXISTS t_cloud_nodes (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_node_id TEXT NOT NULL,
    c_provider TEXT NOT NULL DEFAULT 'tencent-scf',
    c_cloud_account_id TEXT NOT NULL DEFAULT '',
    c_package_id TEXT NOT NULL DEFAULT '',
    c_package_version TEXT NOT NULL DEFAULT '',
    c_deployment_id TEXT NOT NULL DEFAULT '',
    c_node_type TEXT NOT NULL DEFAULT 'scf-event',
    c_region TEXT NOT NULL DEFAULT '',
    c_namespace TEXT NOT NULL DEFAULT '',
    c_function_name TEXT NOT NULL DEFAULT '',
    c_running_version TEXT NOT NULL DEFAULT '',
    c_supported_workloads TEXT NOT NULL DEFAULT '[]',
    c_metadata TEXT NOT NULL DEFAULT '{}',
    c_status TEXT NOT NULL DEFAULT 'unknown',
    c_last_heartbeat_at DATETIME,
    c_is_deleted INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_nodes_space_node ON t_cloud_nodes(c_space_id, c_node_id);
CREATE INDEX IF NOT EXISTS idx_cloud_nodes_status ON t_cloud_nodes(c_status);
CREATE INDEX IF NOT EXISTS idx_cloud_nodes_deleted ON t_cloud_nodes(c_is_deleted);

CREATE TABLE IF NOT EXISTS t_cloud_accounts (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_account_id TEXT NOT NULL,
    c_account_name TEXT NOT NULL DEFAULT '',
    c_provider TEXT NOT NULL DEFAULT 'tencent',
    c_secret_id TEXT NOT NULL DEFAULT '',
    c_secret_key TEXT NOT NULL DEFAULT '',
    c_app_id TEXT NOT NULL DEFAULT '',
    c_cos_region TEXT NOT NULL DEFAULT '',
    c_cos_bucket TEXT NOT NULL DEFAULT '',
    c_extra_config TEXT NOT NULL DEFAULT '{}',
    c_is_deleted INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_accounts_account ON t_cloud_accounts(c_account_id, c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_cloud_accounts_provider ON t_cloud_accounts(c_provider);

CREATE TABLE IF NOT EXISTS t_cloud_function_packages (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_package_id TEXT NOT NULL,
    c_package_name TEXT NOT NULL DEFAULT '',
    c_version TEXT NOT NULL DEFAULT '',
    c_description TEXT NOT NULL DEFAULT '',
    c_runtime TEXT NOT NULL DEFAULT '',
    c_package_type TEXT NOT NULL DEFAULT '',
    c_workload_type TEXT NOT NULL DEFAULT '',
    c_original_filename TEXT NOT NULL DEFAULT '',
    c_file_size INTEGER NOT NULL DEFAULT 0,
    c_file_md5 TEXT NOT NULL DEFAULT '',
    c_cloud_account_id TEXT NOT NULL DEFAULT '',
    c_cos_region TEXT NOT NULL DEFAULT '',
    c_cos_bucket TEXT NOT NULL DEFAULT '',
    c_cos_path TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_error_message TEXT NOT NULL DEFAULT '',
    c_is_deleted INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_packages_space_package ON t_cloud_function_packages(c_space_id, c_package_id);
CREATE INDEX IF NOT EXISTS idx_cloud_packages_workload ON t_cloud_function_packages(c_workload_type);
CREATE INDEX IF NOT EXISTS idx_cloud_packages_deleted ON t_cloud_function_packages(c_is_deleted);

CREATE TRIGGER IF NOT EXISTS update_cloud_nodes_mtime AFTER UPDATE ON t_cloud_nodes BEGIN
    UPDATE t_cloud_nodes SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;
CREATE TRIGGER IF NOT EXISTS update_cloud_accounts_mtime AFTER UPDATE ON t_cloud_accounts BEGIN
    UPDATE t_cloud_accounts SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;
CREATE TRIGGER IF NOT EXISTS update_cloud_packages_mtime AFTER UPDATE ON t_cloud_function_packages BEGIN
    UPDATE t_cloud_function_packages SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;
