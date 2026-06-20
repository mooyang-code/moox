-- moox storage metadata schema
--
-- 设计目标：
-- 1. Space 是业务命名空间；DataSource、Subject、DataSet、Field、Factor 和 View 都归属 Space。
-- 2. DataSet 描述可写事实数据集，并且只绑定一个 DataSource。
-- 3. Subject 是 Space 内业务对象，不归属 DataSource；来源侧代码由 SubjectSymbol 管理。
-- 4. View 是查询入口，必须指定 primary_dataset_id，物化结果按 c_query_window 异步构建。
-- 5. StorageRoute 只把在线事实主存路由到 StorageNode，不直接绑定 Device。
-- 6. DuckDB、Bleve 和 Parquet 均从 Pebble 主存变更异步派生。

PRAGMA foreign_keys = ON;

-- ************ Space ************
CREATE TABLE IF NOT EXISTS t_spaces (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_owner TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    UNIQUE (c_space_id),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_spaces_status ON t_spaces (c_status);
CREATE INDEX IF NOT EXISTS idx_t_spaces_owner ON t_spaces (c_owner);

CREATE TRIGGER IF NOT EXISTS trg_t_spaces_mtime
AFTER UPDATE ON t_spaces
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_spaces SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- ************ View ************
CREATE TABLE IF NOT EXISTS t_views (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_view_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_primary_dataset_id TEXT NOT NULL,
    c_dataset_ids_json TEXT NOT NULL DEFAULT '[]',
    c_grain_keys_json TEXT NOT NULL DEFAULT '[]',
    c_filter_json TEXT NOT NULL DEFAULT '{}',
    c_engine TEXT NOT NULL DEFAULT 'duckdb',
    c_query_window TEXT NOT NULL DEFAULT '',
    c_active_result TEXT NOT NULL DEFAULT '',
    c_build_status TEXT NOT NULL DEFAULT 'pending',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_engine IN ('duckdb')),
    CHECK (c_build_status IN ('pending', 'building', 'active', 'failed', 'disabled', 'archived', 'deleted')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_space_id, c_primary_dataset_id) REFERENCES t_datasets (c_space_id, c_dataset_id) ON DELETE RESTRICT ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_view_id),
    UNIQUE (c_space_id, c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_views_space ON t_views (c_space_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_views_primary_dataset ON t_views (c_space_id, c_primary_dataset_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_views_build_status ON t_views (c_space_id, c_build_status);

CREATE TRIGGER IF NOT EXISTS trg_t_views_mtime
AFTER UPDATE ON t_views
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_views SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_view_columns (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_view_id TEXT NOT NULL,
    c_column_name TEXT NOT NULL,
    c_origin_type TEXT NOT NULL,
    c_origin_id TEXT NOT NULL,
    c_value_type TEXT NOT NULL,
    c_online_time DATETIME NOT NULL DEFAULT '',
    c_sort_order INTEGER NOT NULL DEFAULT 0,
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_origin_type IN ('dataset_column', 'system', 'expression')),
    CHECK (c_value_type IN ('string', 'int', 'double', 'bool', 'time', 'json', 'bytes')),
    CHECK (c_sort_order >= 0),
    FOREIGN KEY (c_space_id, c_view_id) REFERENCES t_views (c_space_id, c_view_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_view_id, c_column_name)
);

CREATE INDEX IF NOT EXISTS idx_t_view_columns_view ON t_view_columns (c_space_id, c_view_id, c_sort_order);
CREATE INDEX IF NOT EXISTS idx_t_view_columns_origin ON t_view_columns (c_space_id, c_origin_type, c_origin_id);

CREATE TRIGGER IF NOT EXISTS trg_t_view_columns_mtime
AFTER UPDATE ON t_view_columns
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_view_columns SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- ************ 数据来源与数据对象 ************
CREATE TABLE IF NOT EXISTS t_data_sources (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_data_source_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_kind TEXT NOT NULL,
    c_market TEXT NOT NULL DEFAULT '',
    c_timezone TEXT NOT NULL DEFAULT '',
    c_config_json TEXT NOT NULL DEFAULT '{}',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_kind IN ('exchange', 'vendor_api', 'file_import', 'manual', 'internal')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_data_source_id),
    UNIQUE (c_space_id, c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_data_sources_kind ON t_data_sources (c_space_id, c_kind, c_status);
CREATE INDEX IF NOT EXISTS idx_t_data_sources_market ON t_data_sources (c_space_id, c_market, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_data_sources_mtime
AFTER UPDATE ON t_data_sources
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_data_sources SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_subjects (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_subject_type TEXT NOT NULL,
    c_name TEXT NOT NULL DEFAULT '',
    c_market TEXT NOT NULL DEFAULT '',
    c_currency TEXT NOT NULL DEFAULT '',
    c_timezone TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_subject_id)
);

CREATE INDEX IF NOT EXISTS idx_t_subjects_type ON t_subjects (c_space_id, c_subject_type, c_status);
CREATE INDEX IF NOT EXISTS idx_t_subjects_market ON t_subjects (c_space_id, c_market, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_subjects_mtime
AFTER UPDATE ON t_subjects
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_subjects SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_subject_symbols (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_data_source_id TEXT NOT NULL,
    c_external_symbol TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id, c_subject_id) REFERENCES t_subjects (c_space_id, c_subject_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_space_id, c_data_source_id) REFERENCES t_data_sources (c_space_id, c_data_source_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_data_source_id, c_external_symbol)
);

CREATE INDEX IF NOT EXISTS idx_t_subject_symbols_subject ON t_subject_symbols (c_space_id, c_subject_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_subject_symbols_source ON t_subject_symbols (c_space_id, c_data_source_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_subject_symbols_mtime
AFTER UPDATE ON t_subject_symbols
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_subject_symbols SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- ************ DataSet、Field 与 Factor ************
CREATE TABLE IF NOT EXISTS t_datasets (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_data_source_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_data_kind TEXT NOT NULL,
    c_freqs_json TEXT NOT NULL DEFAULT '[]',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_data_kind IN ('object', 'time_series', 'snapshot', 'event', 'document', 'table')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_space_id, c_data_source_id) REFERENCES t_data_sources (c_space_id, c_data_source_id) ON DELETE RESTRICT ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_dataset_id),
    UNIQUE (c_space_id, c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_datasets_source ON t_datasets (c_space_id, c_data_source_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_datasets_kind ON t_datasets (c_space_id, c_data_kind, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_datasets_mtime
AFTER UPDATE ON t_datasets
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_datasets SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_dataset_subjects (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_subject_role TEXT NOT NULL DEFAULT 'normal',
    c_effective_start_time DATETIME NOT NULL DEFAULT '',
    c_effective_end_time DATETIME NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_subject_role IN ('normal', 'benchmark', 'index', 'universe_member', 'object')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id, c_dataset_id) REFERENCES t_datasets (c_space_id, c_dataset_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_space_id, c_subject_id) REFERENCES t_subjects (c_space_id, c_subject_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_dataset_id, c_subject_id)
);

CREATE INDEX IF NOT EXISTS idx_t_dataset_subjects_dataset ON t_dataset_subjects (c_space_id, c_dataset_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_dataset_subjects_subject ON t_dataset_subjects (c_space_id, c_subject_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_dataset_subjects_mtime
AFTER UPDATE ON t_dataset_subjects
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_dataset_subjects SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_fields (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_field_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_value_type TEXT NOT NULL,
    c_unit TEXT NOT NULL DEFAULT '',
    c_validation_rule_json TEXT NOT NULL DEFAULT '{}',
    c_write_example TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_value_type IN ('string', 'int', 'double', 'bool', 'time', 'json', 'bytes')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_field_id)
);

CREATE INDEX IF NOT EXISTS idx_t_fields_value_type ON t_fields (c_space_id, c_value_type, c_status);
CREATE INDEX IF NOT EXISTS idx_t_fields_status ON t_fields (c_space_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_fields_mtime
AFTER UPDATE ON t_fields
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_fields SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_factors (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_factor_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_algorithm TEXT NOT NULL DEFAULT '',
    c_params_json TEXT NOT NULL DEFAULT '{}',
    c_value_type TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_value_type IN ('string', 'int', 'double', 'bool', 'time', 'json', 'bytes')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_factor_id)
);

CREATE INDEX IF NOT EXISTS idx_t_factors_algorithm ON t_factors (c_space_id, c_algorithm, c_status);
CREATE INDEX IF NOT EXISTS idx_t_factors_status ON t_factors (c_space_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_factors_mtime
AFTER UPDATE ON t_factors
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factors SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_dataset_columns (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_column_name TEXT NOT NULL,
    c_origin_type TEXT NOT NULL,
    c_origin_id TEXT NOT NULL DEFAULT '',
    c_value_type TEXT NOT NULL,
    c_required INTEGER NOT NULL DEFAULT 0,
    c_is_unique INTEGER NOT NULL DEFAULT 0,
    c_aliases_json TEXT NOT NULL DEFAULT '[]',
    c_text_indexed INTEGER NOT NULL DEFAULT 0,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_origin_type IN ('field', 'factor', 'system')),
    CHECK (c_value_type IN ('string', 'int', 'double', 'bool', 'time', 'json', 'bytes')),
    CHECK (c_required IN (0, 1)),
    CHECK (c_is_unique IN (0, 1)),
    CHECK (c_text_indexed IN (0, 1)),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id, c_dataset_id) REFERENCES t_datasets (c_space_id, c_dataset_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_dataset_id, c_column_name),
    UNIQUE (c_space_id, c_dataset_id, c_origin_type, c_origin_id)
);

CREATE INDEX IF NOT EXISTS idx_t_dataset_columns_dataset ON t_dataset_columns (c_space_id, c_dataset_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_dataset_columns_origin ON t_dataset_columns (c_space_id, c_origin_type, c_origin_id);
CREATE INDEX IF NOT EXISTS idx_t_dataset_columns_text_indexed ON t_dataset_columns (c_space_id, c_dataset_id, c_text_indexed, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_dataset_columns_mtime
AFTER UPDATE ON t_dataset_columns
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_dataset_columns SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- ************ 存储节点、设备、路由和归档 ************
CREATE TABLE IF NOT EXISTS t_storage_nodes (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_node_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_endpoint TEXT NOT NULL DEFAULT '',
    c_weight INTEGER NOT NULL DEFAULT 100,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_config_json TEXT NOT NULL DEFAULT '{}',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_weight >= 0),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    UNIQUE (c_node_id),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_storage_nodes_status ON t_storage_nodes (c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_storage_nodes_mtime
AFTER UPDATE ON t_storage_nodes
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_storage_nodes SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_storage_devices (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_device_id TEXT NOT NULL,
    c_node_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_engine TEXT NOT NULL,
    c_endpoint TEXT NOT NULL DEFAULT '',
    c_config_json TEXT NOT NULL DEFAULT '{}',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_engine IN ('pebble', 'duckdb', 'bleve', 'parquet_archive')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_node_id) REFERENCES t_storage_nodes (c_node_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_device_id),
    UNIQUE (c_node_id, c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_storage_devices_node ON t_storage_devices (c_node_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_storage_devices_engine ON t_storage_devices (c_engine, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_storage_devices_mtime
AFTER UPDATE ON t_storage_devices
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_storage_devices SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_storage_routes (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_route_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL DEFAULT '',
    c_subject_pattern TEXT NOT NULL DEFAULT '',
    c_hash_rule TEXT NOT NULL DEFAULT '',
    c_node_id TEXT NOT NULL,
    c_priority INTEGER NOT NULL DEFAULT 100,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_priority >= 0),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id, c_dataset_id) REFERENCES t_datasets (c_space_id, c_dataset_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_node_id) REFERENCES t_storage_nodes (c_node_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_route_id)
);

CREATE INDEX IF NOT EXISTS idx_t_storage_routes_lookup ON t_storage_routes (c_space_id, c_dataset_id, c_status, c_priority);
CREATE INDEX IF NOT EXISTS idx_t_storage_routes_subject ON t_storage_routes (c_space_id, c_subject_id, c_status, c_priority);
CREATE INDEX IF NOT EXISTS idx_t_storage_routes_node ON t_storage_routes (c_node_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_storage_routes_mtime
AFTER UPDATE ON t_storage_routes
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_storage_routes SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_archive_files (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_archive_file_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_device_id TEXT NOT NULL,
    c_partition_key TEXT NOT NULL,
    c_file_uri TEXT NOT NULL,
    c_file_format TEXT NOT NULL DEFAULT 'parquet',
    c_min_time DATETIME NOT NULL DEFAULT '',
    c_max_time DATETIME NOT NULL DEFAULT '',
    c_row_count INTEGER NOT NULL DEFAULT 0,
    c_content_hash TEXT NOT NULL DEFAULT '',
    c_columns_json TEXT NOT NULL DEFAULT '[]',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_file_format = 'parquet'),
    CHECK (c_row_count >= 0),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted', 'failed')),
    FOREIGN KEY (c_space_id, c_dataset_id) REFERENCES t_datasets (c_space_id, c_dataset_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_device_id) REFERENCES t_storage_devices (c_device_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_archive_file_id),
    UNIQUE (c_device_id, c_file_uri)
);

CREATE INDEX IF NOT EXISTS idx_t_archive_files_dataset ON t_archive_files (c_space_id, c_dataset_id, c_partition_key, c_status);
CREATE INDEX IF NOT EXISTS idx_t_archive_files_time ON t_archive_files (c_space_id, c_dataset_id, c_min_time, c_max_time);
CREATE INDEX IF NOT EXISTS idx_t_archive_files_device ON t_archive_files (c_device_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_archive_files_mtime
AFTER UPDATE ON t_archive_files
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_archive_files SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;
