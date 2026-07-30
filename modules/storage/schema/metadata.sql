-- moox storage metadata schema
--
-- 设计目标：
-- 1. Space 是业务命名空间；DataSource、Subject、Dataset、Field、Factor 和 View 都归属 Space。
-- 2. Dataset 描述可写事实数据集，并且只绑定一个 DataSource。
-- 3. Subject 是 Space 内业务对象，不归属 DataSource；来源侧代码由 SubjectSymbol 管理。
-- 4. View 是查询入口，使用 keep_duration 控制 TimeSeries 行保留。
-- 5. Dataset 直接绑定 DataNode；运行时路由只解析 Dataset 到 DataNode 的关系。
-- 6. DuckDB、Bleve 和 Parquet 均从 Pebble 主存变更异步派生。

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_schema_meta (
    c_key TEXT NOT NULL PRIMARY KEY,
    c_value TEXT NOT NULL
);

INSERT INTO t_schema_meta (c_key, c_value)
VALUES ('schema_version', '6')
ON CONFLICT(c_key) DO NOTHING;

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
    c_keep_duration TEXT NOT NULL DEFAULT '0',
    c_active_index_id TEXT NOT NULL DEFAULT '',
    c_desired_view_revision INTEGER NOT NULL DEFAULT 1,
    c_active_view_revision INTEGER NOT NULL DEFAULT 0,
    c_active_columns_json TEXT NOT NULL DEFAULT '[]',
    c_active_view_schema_hash TEXT NOT NULL DEFAULT '',
    c_active_slot TEXT NOT NULL DEFAULT 'slot-a',
    c_indexed_from TEXT NOT NULL DEFAULT '',
    c_indexed_to TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_engine IN ('duckdb', 'bleve')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_space_id, c_primary_dataset_id) REFERENCES t_datasets (c_space_id, c_dataset_id) ON DELETE RESTRICT ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_view_id),
    UNIQUE (c_space_id, c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_views_space ON t_views (c_space_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_views_primary_dataset ON t_views (c_space_id, c_primary_dataset_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_views_revision_pending ON t_views (c_space_id, c_status, c_desired_view_revision, c_active_view_revision);

CREATE TRIGGER IF NOT EXISTS trg_t_views_mtime
AFTER UPDATE ON t_views
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_views SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_view_index_builds (
    c_space_id TEXT NOT NULL,
    c_view_id TEXT NOT NULL,
    c_build_id TEXT NOT NULL UNIQUE,
    c_index_id TEXT NOT NULL,
    c_engine TEXT NOT NULL,
    c_target_view_version INTEGER NOT NULL,
    c_state INTEGER NOT NULL,
    c_owner_id TEXT NOT NULL,
    c_new_slot TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_started_at TEXT NOT NULL,
    c_backfilled_rows INTEGER NOT NULL DEFAULT 0,
    c_safe_error TEXT NOT NULL DEFAULT '',
    c_updated_at TEXT NOT NULL,
    PRIMARY KEY (c_space_id, c_view_id),
    FOREIGN KEY (c_space_id, c_view_id) REFERENCES t_views (c_space_id, c_view_id) ON DELETE CASCADE ON UPDATE CASCADE,
    CHECK (c_engine IN ('duckdb', 'bleve')),
    CHECK (c_state BETWEEN 1 AND 5)
);

CREATE INDEX IF NOT EXISTS idx_t_view_index_builds_status
ON t_view_index_builds (c_status, c_started_at);

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

-- ************ DataNode ************
CREATE TABLE IF NOT EXISTS t_data_nodes (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_node_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_service_target TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'active' CHECK (c_status IN ('active', 'disabled')),
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_node_id),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_data_nodes_status ON t_data_nodes (c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_data_nodes_mtime
AFTER UPDATE ON t_data_nodes
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_data_nodes SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- ************ Dataset、Field 与 Factor ************
CREATE TABLE IF NOT EXISTS t_datasets (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_data_source_id TEXT NOT NULL,
    c_data_node_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_data_kind TEXT NOT NULL,
    c_freqs_json TEXT NOT NULL DEFAULT '[]',
    c_keep_duration TEXT NOT NULL,
    c_binding_locked INTEGER NOT NULL DEFAULT 0 CHECK (c_binding_locked IN (0, 1)),
    c_revision INTEGER NOT NULL DEFAULT 1 CHECK (c_revision > 0),
    c_status TEXT NOT NULL DEFAULT 'disabled' CHECK (c_status IN ('active', 'disabled')),
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_data_kind IN ('record', 'time_series')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_space_id, c_data_source_id) REFERENCES t_data_sources (c_space_id, c_data_source_id) ON DELETE RESTRICT ON UPDATE CASCADE,
    FOREIGN KEY (c_data_node_id) REFERENCES t_data_nodes (c_node_id) ON DELETE RESTRICT,
    UNIQUE (c_space_id, c_dataset_id),
    UNIQUE (c_space_id, c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_datasets_source ON t_datasets (c_space_id, c_data_source_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_datasets_kind ON t_datasets (c_space_id, c_data_kind, c_status);
CREATE INDEX IF NOT EXISTS idx_t_datasets_data_node_id ON t_datasets (c_data_node_id);

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
    CHECK (c_subject_role IN ('normal', 'benchmark', 'index', 'universe_member', 'record')),
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

CREATE TABLE IF NOT EXISTS t_field_groups (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_group_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_parent_group_id TEXT,
    c_sort_order INTEGER NOT NULL DEFAULT 0,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_sort_order >= 0),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    CHECK (c_parent_group_id IS NULL OR c_parent_group_id <> c_group_id),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_space_id, c_parent_group_id) REFERENCES t_field_groups (c_space_id, c_group_id) ON DELETE RESTRICT ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_group_id),
    UNIQUE (c_space_id, c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_field_groups_parent ON t_field_groups (c_space_id, c_parent_group_id, c_sort_order, c_group_id);

CREATE TRIGGER IF NOT EXISTS trg_t_field_groups_two_levels_insert
BEFORE INSERT ON t_field_groups
FOR EACH ROW
WHEN NEW.c_parent_group_id IS NOT NULL AND (
    EXISTS (
        SELECT 1 FROM t_field_groups parent
        WHERE parent.c_space_id = NEW.c_space_id
          AND parent.c_group_id = NEW.c_parent_group_id
          AND parent.c_parent_group_id IS NOT NULL
    )
    OR EXISTS (
        SELECT 1 FROM t_field_groups child
        WHERE child.c_space_id = NEW.c_space_id
          AND child.c_parent_group_id = NEW.c_group_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'field groups support at most two levels');
END;

CREATE TRIGGER IF NOT EXISTS trg_t_field_groups_two_levels_update
BEFORE UPDATE OF c_parent_group_id ON t_field_groups
FOR EACH ROW
WHEN NEW.c_parent_group_id IS NOT NULL AND (
    EXISTS (
        SELECT 1 FROM t_field_groups parent
        WHERE parent.c_space_id = NEW.c_space_id
          AND parent.c_group_id = NEW.c_parent_group_id
          AND parent.c_parent_group_id IS NOT NULL
    )
    OR EXISTS (
        SELECT 1 FROM t_field_groups child
        WHERE child.c_space_id = NEW.c_space_id
          AND child.c_parent_group_id = NEW.c_group_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'field groups support at most two levels');
END;

CREATE TRIGGER IF NOT EXISTS trg_t_field_groups_mtime
AFTER UPDATE ON t_field_groups
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_field_groups SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_fields (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_field_id TEXT NOT NULL,
    c_group_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_value_type TEXT NOT NULL,
    c_unit TEXT NOT NULL DEFAULT '',
    c_validation_rule_json TEXT NOT NULL DEFAULT '{}',
    c_write_example TEXT NOT NULL DEFAULT '',
    c_sort_order INTEGER NOT NULL DEFAULT 0,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_value_type IN ('string', 'int', 'double', 'bool', 'time', 'json', 'bytes')),
    CHECK (c_sort_order >= 0),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id, c_group_id) REFERENCES t_field_groups (c_space_id, c_group_id) ON DELETE RESTRICT ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_field_id)
);

CREATE INDEX IF NOT EXISTS idx_t_fields_value_type ON t_fields (c_space_id, c_value_type, c_status);
CREATE INDEX IF NOT EXISTS idx_t_fields_status ON t_fields (c_space_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_fields_group ON t_fields (c_space_id, c_group_id, c_sort_order, c_field_id);

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
    c_aliases_json TEXT NOT NULL DEFAULT '[]',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_origin_type IN ('field', 'factor', 'system')),
    CHECK (c_value_type IN ('string', 'int', 'double', 'bool', 'time', 'json', 'bytes')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    FOREIGN KEY (c_space_id, c_dataset_id) REFERENCES t_datasets (c_space_id, c_dataset_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_dataset_id, c_column_name),
    UNIQUE (c_space_id, c_dataset_id, c_origin_type, c_origin_id)
);

CREATE INDEX IF NOT EXISTS idx_t_dataset_columns_dataset ON t_dataset_columns (c_space_id, c_dataset_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_dataset_columns_origin ON t_dataset_columns (c_space_id, c_origin_type, c_origin_id);

CREATE TRIGGER IF NOT EXISTS trg_t_dataset_columns_mtime
AFTER UPDATE ON t_dataset_columns
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_dataset_columns SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- ************ 设备和归档 ************
CREATE TABLE IF NOT EXISTS t_storage_devices (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_device_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_engine TEXT NOT NULL,
    c_endpoint TEXT NOT NULL DEFAULT '',
    c_config_json TEXT NOT NULL DEFAULT '{}',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_engine IN ('pebble', 'duckdb', 'bleve', 'parquet')),
    CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
    UNIQUE (c_device_id)
);

CREATE INDEX IF NOT EXISTS idx_t_storage_devices_engine ON t_storage_devices (c_engine, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_storage_devices_mtime
AFTER UPDATE ON t_storage_devices
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_storage_devices SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
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
