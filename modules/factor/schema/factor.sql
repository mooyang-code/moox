PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_factor_catalog (
    c_id INTEGER PRIMARY KEY CHECK (c_id = 1),
    c_revision INTEGER NOT NULL DEFAULT 0 CHECK (c_revision >= 0),
    c_snapshot_hash TEXT NOT NULL DEFAULT ''
);
INSERT OR IGNORE INTO t_factor_catalog (c_id) VALUES (1);

CREATE TABLE IF NOT EXISTS t_factor_defs (
    c_factor_id TEXT NOT NULL PRIMARY KEY,
    c_name TEXT NOT NULL,
    c_factor_type TEXT NOT NULL,
    c_source_code TEXT NOT NULL,
    c_source_hash TEXT NOT NULL,
    c_source_path TEXT NOT NULL DEFAULT '',
    c_input_columns_json TEXT NOT NULL,
    c_outputs_json TEXT NOT NULL,
    c_params_json TEXT NOT NULL DEFAULT '{}',
    c_lookback_periods INTEGER NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'disabled',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_lookback_periods >= 1),
    CHECK (c_factor_type IN ('timeseries', 'cross_section')),
    CHECK (c_status IN ('enabled', 'disabled')),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_factor_defs_status ON t_factor_defs (c_status);

CREATE TABLE IF NOT EXISTS t_factor_bindings (
    c_binding_id TEXT NOT NULL PRIMARY KEY,
    c_factor_id TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_source_view_id TEXT NOT NULL,
    c_freq TEXT NOT NULL,
    c_subject_mode TEXT NOT NULL DEFAULT 'all',
    c_subjects_json TEXT NOT NULL DEFAULT '[]',
    c_result_dataset_id TEXT NOT NULL,
    c_result_view_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending_view',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_subject_mode IN ('all', 'include')),
    CHECK (c_status IN ('pending_view', 'enabled', 'disabled', 'cleanup_pending')),
    FOREIGN KEY (c_factor_id) REFERENCES t_factor_defs (c_factor_id),
    UNIQUE (c_factor_id, c_space_id, c_source_view_id, c_freq)
);

CREATE INDEX IF NOT EXISTS idx_factor_bindings_source
ON t_factor_bindings (c_space_id, c_source_view_id, c_freq, c_status);

CREATE TABLE IF NOT EXISTS t_factor_output_manifests (
    c_binding_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_period_time INTEGER NOT NULL,
    c_row_keys_json TEXT NOT NULL DEFAULT '[]',
    c_updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_binding_id, c_subject_id, c_frequency, c_period_time)
);

CREATE TRIGGER IF NOT EXISTS update_factor_defs_mtime
AFTER UPDATE ON t_factor_defs
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_defs SET c_mtime = CURRENT_TIMESTAMP WHERE c_factor_id = OLD.c_factor_id;
END;

CREATE TRIGGER IF NOT EXISTS factor_catalog_defs_insert AFTER INSERT ON t_factor_defs
BEGIN UPDATE t_factor_catalog SET c_revision = c_revision + 1, c_snapshot_hash = '' WHERE c_id = 1; END;
CREATE TRIGGER IF NOT EXISTS factor_catalog_defs_delete AFTER DELETE ON t_factor_defs
BEGIN UPDATE t_factor_catalog SET c_revision = c_revision + 1, c_snapshot_hash = '' WHERE c_id = 1; END;
CREATE TRIGGER IF NOT EXISTS factor_catalog_defs_update
AFTER UPDATE OF c_name, c_factor_type, c_source_code, c_source_hash, c_input_columns_json, c_outputs_json, c_params_json, c_lookback_periods, c_status ON t_factor_defs
BEGIN UPDATE t_factor_catalog SET c_revision = c_revision + 1, c_snapshot_hash = '' WHERE c_id = 1; END;
CREATE TRIGGER IF NOT EXISTS factor_catalog_bindings_insert AFTER INSERT ON t_factor_bindings
BEGIN UPDATE t_factor_catalog SET c_revision = c_revision + 1, c_snapshot_hash = '' WHERE c_id = 1; END;
CREATE TRIGGER IF NOT EXISTS factor_catalog_bindings_delete AFTER DELETE ON t_factor_bindings
BEGIN UPDATE t_factor_catalog SET c_revision = c_revision + 1, c_snapshot_hash = '' WHERE c_id = 1; END;
CREATE TRIGGER IF NOT EXISTS factor_catalog_bindings_update
AFTER UPDATE OF c_binding_id, c_factor_id, c_space_id, c_source_view_id, c_freq, c_subject_mode, c_subjects_json, c_result_dataset_id, c_result_view_id, c_status ON t_factor_bindings
BEGIN UPDATE t_factor_catalog SET c_revision = c_revision + 1, c_snapshot_hash = '' WHERE c_id = 1; END;

CREATE TRIGGER IF NOT EXISTS update_factor_bindings_mtime
AFTER UPDATE ON t_factor_bindings
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_bindings SET c_mtime = CURRENT_TIMESTAMP WHERE c_binding_id = OLD.c_binding_id;
END;
