PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_factor_sets (
    c_set_id TEXT NOT NULL PRIMARY KEY,
    c_space_id TEXT NOT NULL,
    c_source_dataset_id TEXT NOT NULL,
    c_freq TEXT NOT NULL,
    c_subject_mode TEXT NOT NULL DEFAULT 'all',
    c_subjects_json TEXT NOT NULL DEFAULT '[]',
    c_result_dataset_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_subject_mode IN ('all', 'include')),
    CHECK (c_status IN ('pending', 'enabled', 'disabled', 'deleting')),
    UNIQUE (c_space_id, c_source_dataset_id, c_freq),
    UNIQUE (c_result_dataset_id)
);

-- 因子定义：只描述算法，与因子集无关，无运行状态
CREATE TABLE IF NOT EXISTS t_factor_defs (
    c_factor_id TEXT NOT NULL PRIMARY KEY,
    c_name TEXT NOT NULL,
    c_factor_type TEXT NOT NULL,
    c_source_code TEXT NOT NULL,
    c_source_hash TEXT NOT NULL,
    c_input_columns_json TEXT NOT NULL,
    c_outputs_json TEXT NOT NULL,
    c_params_json TEXT NOT NULL DEFAULT '{}',
    c_lookback_periods INTEGER NOT NULL,
    c_allow_partial_universe INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_factor_type IN ('timeseries', 'cross_section')),
    CHECK (c_lookback_periods >= 1),
    CHECK (c_allow_partial_universe IN (0, 1))
);

-- 因子集成员：定义在某个因子集中的运行实例，启停状态属于成员
CREATE TABLE IF NOT EXISTS t_factor_set_members (
    c_set_id TEXT NOT NULL,
    c_factor_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'disabled',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_set_id, c_factor_id),
    CHECK (c_status IN ('enabled', 'disabled')),
    FOREIGN KEY (c_set_id) REFERENCES t_factor_sets (c_set_id),
    FOREIGN KEY (c_factor_id) REFERENCES t_factor_defs (c_factor_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_t_factor_set_members_set ON t_factor_set_members (c_set_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_factor_set_members_factor ON t_factor_set_members (c_factor_id);

CREATE TABLE IF NOT EXISTS t_factor_recalc_jobs (
    c_job_id TEXT NOT NULL PRIMARY KEY,
    c_request_id TEXT NOT NULL,
    c_set_id TEXT NOT NULL,
    c_factor_ids_json TEXT NOT NULL DEFAULT '[]',
    c_subjects_json TEXT NOT NULL DEFAULT '[]',
    c_factors_omitted INTEGER NOT NULL DEFAULT 0 CHECK (c_factors_omitted IN (0, 1)),
    c_subjects_omitted INTEGER NOT NULL DEFAULT 0 CHECK (c_subjects_omitted IN (0, 1)),
    c_start_time INTEGER NOT NULL,
    c_end_time INTEGER NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'accepted',
    c_progress_time INTEGER NOT NULL DEFAULT 0,
    c_error TEXT NOT NULL DEFAULT '',
    -- 执行该任务的计算引擎与租约；租约过期后任务可被重新领取
    c_engine_id TEXT NOT NULL DEFAULT '',
    c_lease_token TEXT NOT NULL DEFAULT '',
    c_lease_expires_at INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('accepted', 'running', 'succeeded', 'failed', 'cancelled')),
    CHECK (c_end_time > c_start_time),
    UNIQUE (c_request_id)
);

CREATE INDEX IF NOT EXISTS idx_t_factor_recalc_jobs_status
ON t_factor_recalc_jobs (c_status, c_mtime);
CREATE INDEX IF NOT EXISTS idx_t_factor_recalc_jobs_pull
ON t_factor_recalc_jobs (c_status, c_lease_expires_at);

CREATE TRIGGER IF NOT EXISTS trg_t_factor_sets_mtime
AFTER UPDATE ON t_factor_sets
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_sets SET c_mtime = CURRENT_TIMESTAMP WHERE c_set_id = OLD.c_set_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_factor_defs_mtime
AFTER UPDATE ON t_factor_defs
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_defs SET c_mtime = CURRENT_TIMESTAMP WHERE c_factor_id = OLD.c_factor_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_factor_set_members_mtime
AFTER UPDATE ON t_factor_set_members
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_set_members SET c_mtime = CURRENT_TIMESTAMP
    WHERE c_set_id = OLD.c_set_id AND c_factor_id = OLD.c_factor_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_factor_recalc_jobs_mtime
AFTER UPDATE ON t_factor_recalc_jobs
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_recalc_jobs SET c_mtime = CURRENT_TIMESTAMP WHERE c_job_id = OLD.c_job_id;
END;
