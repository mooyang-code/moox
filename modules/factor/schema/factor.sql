PRAGMA foreign_keys = ON;

DROP TABLE IF EXISTS t_factor_runs;

CREATE TABLE IF NOT EXISTS t_factor_defs (
    c_factor_id TEXT PRIMARY KEY,
    c_name TEXT NOT NULL,
    c_kind TEXT NOT NULL DEFAULT 'timeseries' CHECK (c_kind IN ('timeseries', 'cross_section')),
    c_source_code TEXT NOT NULL,
    c_source_hash TEXT NOT NULL,
    c_params_json TEXT NOT NULL DEFAULT '[]',
    c_lookback_bars INTEGER NOT NULL,
    c_writeback_bars INTEGER NOT NULL DEFAULT 5,
    c_depends_json TEXT NOT NULL DEFAULT '[]',
    c_status TEXT NOT NULL DEFAULT 'disabled' CHECK (c_status IN ('enabled', 'disabled')),
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_factor_defs_kind_status
ON t_factor_defs(c_kind, c_status);

CREATE TABLE IF NOT EXISTS t_factor_bindings (
    c_binding_id TEXT PRIMARY KEY,
    c_factor_id TEXT NOT NULL REFERENCES t_factor_defs(c_factor_id),
    c_space_id TEXT NOT NULL,
    c_source_dataset TEXT NOT NULL,
    c_freq TEXT NOT NULL,
    c_subject_mode TEXT NOT NULL DEFAULT 'all' CHECK (c_subject_mode IN ('all', 'include')),
    c_subjects_json TEXT NOT NULL DEFAULT '[]',
    c_target_dataset TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'enabled' CHECK (c_status IN ('enabled', 'disabled')),
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_factor_bindings_unique
ON t_factor_bindings(c_factor_id, c_space_id, c_source_dataset, c_freq);

CREATE INDEX IF NOT EXISTS idx_factor_bindings_source
ON t_factor_bindings(c_space_id, c_source_dataset, c_freq, c_status);

CREATE TRIGGER IF NOT EXISTS update_factor_defs_mtime AFTER UPDATE ON t_factor_defs
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_defs SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;

CREATE TRIGGER IF NOT EXISTS update_factor_bindings_mtime AFTER UPDATE ON t_factor_bindings
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_bindings SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;
