PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_factor_defs (
    c_factor_id TEXT NOT NULL PRIMARY KEY,
    c_name TEXT NOT NULL,
    c_source_code TEXT NOT NULL,
    c_source_hash TEXT NOT NULL,
    c_source_path TEXT NOT NULL DEFAULT '',
    c_periods_json TEXT NOT NULL,
    c_lookback_bars INTEGER NOT NULL,
    c_depends_json TEXT NOT NULL DEFAULT '[]',
    c_status TEXT NOT NULL DEFAULT 'disabled',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('enabled', 'disabled')),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_factor_defs_status ON t_factor_defs (c_status);

CREATE TABLE IF NOT EXISTS t_factor_bindings (
    c_binding_id TEXT NOT NULL PRIMARY KEY,
    c_factor_id TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_source_dataset TEXT NOT NULL,
    c_freq TEXT NOT NULL,
    c_subject_mode TEXT NOT NULL DEFAULT 'all',
    c_subjects_json TEXT NOT NULL DEFAULT '[]',
    c_target_dataset TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'enabled',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_subject_mode IN ('all', 'include')),
    CHECK (c_status IN ('enabled', 'disabled')),
    FOREIGN KEY (c_factor_id) REFERENCES t_factor_defs (c_factor_id),
    UNIQUE (c_factor_id, c_space_id, c_source_dataset, c_freq)
);

CREATE INDEX IF NOT EXISTS idx_factor_bindings_source
ON t_factor_bindings (c_space_id, c_source_dataset, c_freq, c_status);

CREATE TRIGGER IF NOT EXISTS update_factor_defs_mtime
AFTER UPDATE ON t_factor_defs
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_defs SET c_mtime = CURRENT_TIMESTAMP WHERE c_factor_id = OLD.c_factor_id;
END;

CREATE TRIGGER IF NOT EXISTS update_factor_bindings_mtime
AFTER UPDATE ON t_factor_bindings
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_bindings SET c_mtime = CURRENT_TIMESTAMP WHERE c_binding_id = OLD.c_binding_id;
END;
