-- moox storage metadata schema
--
-- 设计目标：
-- 1. Space 只选择 View，不拥有 Subject、DataSet、Field 或 Factor。
-- 2. DataSet 描述事实数据集，DataSetColumn 描述该数据集允许写入的列。
-- 3. Subject 表示 DataSource 下的数据对象，交易标的只是 Subject 的一种。
-- 4. View 是查询入口，DuckDB 宽表按 View 的 c_query_window 异步构建。
-- 5. StorageRoute 只负责 Pebble 在线主存的水平切分。
-- 6. DuckDB、Bleve 和 Parquet 均从 Pebble 主存变更异步派生。

PRAGMA foreign_keys = ON;

-- ************ Space 与 View ************
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

CREATE TABLE IF NOT EXISTS t_views (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_view_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_dataset_ids_json TEXT NOT NULL DEFAULT '[]',
    c_grain_json TEXT NOT NULL DEFAULT '{}',
    c_freq TEXT NOT NULL DEFAULT '',
    c_filter_json TEXT NOT NULL DEFAULT '{}',
    c_engine TEXT NOT NULL DEFAULT 'duckdb',
    c_query_window TEXT NOT NULL DEFAULT '',
    c_active_table TEXT NOT NULL DEFAULT '',
    c_build_status TEXT NOT NULL DEFAULT 'pending',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_engine IN ('duckdb')),
    CHECK (c_build_status IN ('pending', 'building', 'active', 'failed', 'disabled', 'archived', 'deleted')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    UNIQUE (c_view_id),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_views_status ON t_views (c_status);
CREATE INDEX IF NOT EXISTS idx_t_views_freq ON t_views (c_freq, c_status);
CREATE INDEX IF NOT EXISTS idx_t_views_build_status ON t_views (c_build_status);

CREATE TRIGGER IF NOT EXISTS trg_t_views_mtime
AFTER UPDATE ON t_views
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_views SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_space_views (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_view_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_view_id) REFERENCES t_views (c_view_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_view_id)
);

CREATE INDEX IF NOT EXISTS idx_t_space_views_space ON t_space_views (c_space_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_space_views_view ON t_space_views (c_view_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_space_views_mtime
AFTER UPDATE ON t_space_views
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_space_views SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_view_columns (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_view_id TEXT NOT NULL,
    c_column_name TEXT NOT NULL,
    c_source_type TEXT NOT NULL,
    c_source_id TEXT NOT NULL DEFAULT '',
    c_value_type TEXT NOT NULL,
    c_online_time DATETIME NOT NULL DEFAULT '',
    c_sort_order INTEGER NOT NULL DEFAULT 0,
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_source_type IN ('field', 'factor', 'system', 'expression')),
    CHECK (c_value_type IN ('string', 'int', 'double', 'bool', 'time', 'json', 'bytes')),
    CHECK (c_sort_order >= 0),
    FOREIGN KEY (c_view_id) REFERENCES t_views (c_view_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_view_id, c_column_name)
);

CREATE INDEX IF NOT EXISTS idx_t_view_columns_view ON t_view_columns (c_view_id, c_sort_order);
CREATE INDEX IF NOT EXISTS idx_t_view_columns_source ON t_view_columns (c_source_type, c_source_id);

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
    c_data_source_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_source_type TEXT NOT NULL,
    c_market TEXT NOT NULL DEFAULT '',
    c_timezone TEXT NOT NULL DEFAULT '',
    c_config_json TEXT NOT NULL DEFAULT '{}',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_source_type IN ('exchange', 'vendor_api', 'file_import', 'manual', 'internal')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    UNIQUE (c_data_source_id),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_data_sources_type ON t_data_sources (c_source_type, c_status);
CREATE INDEX IF NOT EXISTS idx_t_data_sources_market ON t_data_sources (c_market, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_data_sources_mtime
AFTER UPDATE ON t_data_sources
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_data_sources SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_subjects (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_subject_id TEXT NOT NULL,
    c_data_source_id TEXT NOT NULL,
    c_subject_type TEXT NOT NULL,
    c_source_symbol TEXT NOT NULL,
    c_name TEXT NOT NULL DEFAULT '',
    c_market TEXT NOT NULL DEFAULT '',
    c_currency TEXT NOT NULL DEFAULT '',
    c_timezone TEXT NOT NULL DEFAULT '',
    c_aliases_json TEXT NOT NULL DEFAULT '[]',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_data_source_id) REFERENCES t_data_sources (c_data_source_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_subject_id),
    UNIQUE (c_data_source_id, c_source_symbol)
);

CREATE INDEX IF NOT EXISTS idx_t_subjects_source ON t_subjects (c_data_source_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_subjects_type ON t_subjects (c_subject_type, c_status);
CREATE INDEX IF NOT EXISTS idx_t_subjects_market ON t_subjects (c_market, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_subjects_mtime
AFTER UPDATE ON t_subjects
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_subjects SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- ************ DataSet、Field 与 Factor ************
CREATE TABLE IF NOT EXISTS t_datasets (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_dataset_id TEXT NOT NULL,
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
    UNIQUE (c_dataset_id),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_datasets_kind ON t_datasets (c_data_kind, c_status);
CREATE INDEX IF NOT EXISTS idx_t_datasets_status ON t_datasets (c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_datasets_mtime
AFTER UPDATE ON t_datasets
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_datasets SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_dataset_subjects (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_dataset_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_subject_role TEXT NOT NULL DEFAULT 'normal',
    c_effective_start_time DATETIME NOT NULL DEFAULT '',
    c_effective_end_time DATETIME NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_subject_role IN ('normal', 'benchmark', 'index', 'universe_member')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_dataset_id) REFERENCES t_datasets (c_dataset_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_subject_id) REFERENCES t_subjects (c_subject_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_dataset_id, c_subject_id)
);

CREATE INDEX IF NOT EXISTS idx_t_dataset_subjects_dataset ON t_dataset_subjects (c_dataset_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_dataset_subjects_subject ON t_dataset_subjects (c_subject_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_dataset_subjects_mtime
AFTER UPDATE ON t_dataset_subjects
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_dataset_subjects SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_fields (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_field_id TEXT NOT NULL,
    c_interface_name TEXT NOT NULL,
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
    UNIQUE (c_field_id),
    UNIQUE (c_interface_name)
);

CREATE INDEX IF NOT EXISTS idx_t_fields_value_type ON t_fields (c_value_type, c_status);
CREATE INDEX IF NOT EXISTS idx_t_fields_status ON t_fields (c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_fields_mtime
AFTER UPDATE ON t_fields
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_fields SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_factors (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
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
    UNIQUE (c_factor_id),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_factors_algorithm ON t_factors (c_algorithm, c_status);
CREATE INDEX IF NOT EXISTS idx_t_factors_status ON t_factors (c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_factors_mtime
AFTER UPDATE ON t_factors
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factors SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_dataset_columns (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_dataset_id TEXT NOT NULL,
    c_column_name TEXT NOT NULL,
    c_source_type TEXT NOT NULL,
    c_source_id TEXT NOT NULL DEFAULT '',
    c_value_type TEXT NOT NULL,
    c_required INTEGER NOT NULL DEFAULT 0,
    c_is_unique INTEGER NOT NULL DEFAULT 0,
    c_aliases_json TEXT NOT NULL DEFAULT '[]',
    c_text_indexed INTEGER NOT NULL DEFAULT 0,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_source_type IN ('field', 'factor', 'system')),
    CHECK (c_value_type IN ('string', 'int', 'double', 'bool', 'time', 'json', 'bytes')),
    CHECK (c_required IN (0, 1)),
    CHECK (c_is_unique IN (0, 1)),
    CHECK (c_text_indexed IN (0, 1)),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_dataset_id) REFERENCES t_datasets (c_dataset_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_dataset_id, c_column_name),
    UNIQUE (c_dataset_id, c_source_type, c_source_id)
);

CREATE INDEX IF NOT EXISTS idx_t_dataset_columns_dataset ON t_dataset_columns (c_dataset_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_dataset_columns_source ON t_dataset_columns (c_source_type, c_source_id);

CREATE TRIGGER IF NOT EXISTS trg_t_dataset_columns_mtime
AFTER UPDATE ON t_dataset_columns
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_dataset_columns SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- ************ 存储实体、设备、路由和归档 ************
CREATE TABLE IF NOT EXISTS t_storage_entities (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_entity_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_endpoint TEXT NOT NULL DEFAULT '',
    c_role TEXT NOT NULL DEFAULT 'adapter',
    c_weight INTEGER NOT NULL DEFAULT 100,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_config_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_role IN ('adapter')),
    CHECK (c_weight >= 0),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    UNIQUE (c_entity_id),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_storage_entities_role ON t_storage_entities (c_role, c_status);
CREATE INDEX IF NOT EXISTS idx_t_storage_entities_status ON t_storage_entities (c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_storage_entities_mtime
AFTER UPDATE ON t_storage_entities
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_storage_entities SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_storage_devices (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_device_id TEXT NOT NULL,
    c_entity_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_engine TEXT NOT NULL,
    c_endpoint TEXT NOT NULL DEFAULT '',
    c_config_json TEXT NOT NULL DEFAULT '{}',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_engine IN ('pebble', 'duckdb', 'bleve', 'parquet_archive')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_entity_id) REFERENCES t_storage_entities (c_entity_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_device_id),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_storage_devices_entity ON t_storage_devices (c_entity_id, c_status);
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
    c_route_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL DEFAULT '',
    c_subject_pattern TEXT NOT NULL DEFAULT '',
    c_hash_rule TEXT NOT NULL DEFAULT '',
    c_entity_id TEXT NOT NULL,
    c_device_id TEXT NOT NULL,
    c_priority INTEGER NOT NULL DEFAULT 100,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_priority >= 0),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_dataset_id) REFERENCES t_datasets (c_dataset_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_entity_id) REFERENCES t_storage_entities (c_entity_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_device_id) REFERENCES t_storage_devices (c_device_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_route_id)
);

CREATE INDEX IF NOT EXISTS idx_t_storage_routes_lookup ON t_storage_routes (c_dataset_id, c_status, c_priority);
CREATE INDEX IF NOT EXISTS idx_t_storage_routes_subject ON t_storage_routes (c_subject_id, c_status, c_priority);
CREATE INDEX IF NOT EXISTS idx_t_storage_routes_entity ON t_storage_routes (c_entity_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_storage_routes_device ON t_storage_routes (c_device_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_storage_routes_mtime
AFTER UPDATE ON t_storage_routes
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_storage_routes SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_archive_files (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
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
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_file_format = 'parquet'),
    CHECK (c_row_count >= 0),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted', 'failed')),
    FOREIGN KEY (c_dataset_id) REFERENCES t_datasets (c_dataset_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_device_id) REFERENCES t_storage_devices (c_device_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_archive_file_id),
    UNIQUE (c_device_id, c_file_uri)
);

CREATE INDEX IF NOT EXISTS idx_t_archive_files_dataset ON t_archive_files (c_dataset_id, c_partition_key, c_status);
CREATE INDEX IF NOT EXISTS idx_t_archive_files_time ON t_archive_files (c_dataset_id, c_min_time, c_max_time);
CREATE INDEX IF NOT EXISTS idx_t_archive_files_device ON t_archive_files (c_device_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_archive_files_mtime
AFTER UPDATE ON t_archive_files
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_archive_files SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;
