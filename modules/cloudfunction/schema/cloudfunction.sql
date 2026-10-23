PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_cloud_function_instances (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_instance_id TEXT NOT NULL,
    c_lifecycle_id TEXT NOT NULL DEFAULT '',
    c_lifecycle_operation_id TEXT NOT NULL DEFAULT '',
    c_provider TEXT NOT NULL DEFAULT 'tencent-scf',
    c_cloud_account_id TEXT NOT NULL DEFAULT '',
    c_package_id TEXT NOT NULL DEFAULT '',
    c_package_version TEXT NOT NULL DEFAULT '',
    c_deployment_id TEXT NOT NULL DEFAULT '',
    c_function_type TEXT NOT NULL DEFAULT 'scf-event',
    c_trigger_type TEXT NOT NULL DEFAULT '',
    c_region TEXT NOT NULL DEFAULT '',
    c_namespace TEXT NOT NULL DEFAULT '',
    c_function_name TEXT NOT NULL DEFAULT '',
    c_metadata TEXT NOT NULL DEFAULT '{}',
    c_is_deleted INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_t_cloud_function_instances_space_instance ON t_cloud_function_instances (c_space_id, c_instance_id);
CREATE INDEX IF NOT EXISTS idx_t_cloud_function_instances_deleted ON t_cloud_function_instances (c_is_deleted);

CREATE TABLE IF NOT EXISTS t_cloud_accounts (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_account_id TEXT NOT NULL,
    c_account_name TEXT NOT NULL DEFAULT '',
    c_provider TEXT NOT NULL DEFAULT 'tencent',
    c_credential_secret_id TEXT NOT NULL,
    c_app_id TEXT NOT NULL DEFAULT '',
    c_cos_region TEXT NOT NULL DEFAULT '',
    c_cos_bucket TEXT NOT NULL DEFAULT '',
    c_extra_config TEXT NOT NULL DEFAULT '{}',
    c_is_deleted INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- account_id 全局唯一：软删除后同名重建复用旧行，避免 (id,0)/(id,1) 两行并存时再次删除违反联合唯一约束
CREATE UNIQUE INDEX IF NOT EXISTS idx_t_cloud_accounts_account_id ON t_cloud_accounts (c_account_id);
CREATE INDEX IF NOT EXISTS idx_t_cloud_accounts_provider ON t_cloud_accounts (c_provider);

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

CREATE UNIQUE INDEX IF NOT EXISTS idx_t_cloud_function_packages_space_package ON t_cloud_function_packages (c_space_id, c_package_id);
CREATE INDEX IF NOT EXISTS idx_t_cloud_function_packages_workload ON t_cloud_function_packages (c_workload_type);
CREATE INDEX IF NOT EXISTS idx_t_cloud_function_packages_deleted ON t_cloud_function_packages (c_is_deleted);

CREATE TABLE IF NOT EXISTS t_cloud_function_jobs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_job_id TEXT NOT NULL,
    c_operation TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_total_count INTEGER NOT NULL,
    c_completed_at DATETIME,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_t_cloud_function_jobs_space_job
ON t_cloud_function_jobs (c_space_id, c_job_id);

CREATE TABLE IF NOT EXISTS t_cloud_function_job_items (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_job_id TEXT NOT NULL,
    c_item_id TEXT NOT NULL,
    c_item_index INTEGER NOT NULL,
    c_instance_id TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_request_json TEXT NOT NULL,
    c_publish_lease_recovery_owned INTEGER NOT NULL DEFAULT 0,
    c_result_summary TEXT NOT NULL DEFAULT '',
    c_error_message TEXT NOT NULL DEFAULT '',
    c_started_at DATETIME,
    c_completed_at DATETIME,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (c_space_id, c_job_id)
        REFERENCES t_cloud_function_jobs (c_space_id, c_job_id)
        ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_t_cloud_function_job_items_job_index
ON t_cloud_function_job_items (c_space_id, c_job_id, c_item_index);
CREATE INDEX IF NOT EXISTS idx_t_cloud_function_job_items_status_id ON t_cloud_function_job_items (c_status, c_id);

CREATE TRIGGER IF NOT EXISTS trg_t_cloud_function_instances_mtime
AFTER UPDATE ON t_cloud_function_instances
FOR EACH ROW
BEGIN
    UPDATE t_cloud_function_instances SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_cloud_accounts_mtime
AFTER UPDATE ON t_cloud_accounts
FOR EACH ROW
BEGIN
    UPDATE t_cloud_accounts SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_cloud_function_packages_mtime
AFTER UPDATE ON t_cloud_function_packages
FOR EACH ROW
BEGIN
    UPDATE t_cloud_function_packages SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;
