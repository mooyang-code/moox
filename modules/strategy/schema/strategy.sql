-- 策略定义：一份 DSL 文本，按内容哈希标识版本；软删除
CREATE TABLE IF NOT EXISTS t_strategy_defs (
    c_strategy_id TEXT NOT NULL PRIMARY KEY,
    c_name TEXT NOT NULL,
    c_dsl_yaml TEXT NOT NULL,
    c_dsl_hash TEXT NOT NULL,
    c_deleted_at DATETIME,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 出现过的每个 DSL 版本，按内容哈希去重，永久保留
CREATE TABLE IF NOT EXISTS t_strategy_def_versions (
    c_dsl_hash TEXT NOT NULL PRIMARY KEY,
    c_dsl_yaml TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 实例：定义 + View + 可选组合账户；启用时固化解析结果；软删除
CREATE TABLE IF NOT EXISTS t_strategy_instances (
    c_instance_id TEXT NOT NULL PRIMARY KEY,
    c_strategy_id TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_view_id TEXT NOT NULL,
    c_logical_account_id TEXT,
    c_enabled INTEGER NOT NULL DEFAULT 0,
    c_session_id TEXT,
    c_resolved_json TEXT NOT NULL DEFAULT '{}',
    c_health TEXT NOT NULL DEFAULT 'ok',
    c_deleted_at DATETIME,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_enabled IN (0, 1)),
    CHECK (c_health IN ('ok', 'degraded', 'session_unverified')),
    FOREIGN KEY (c_strategy_id) REFERENCES t_strategy_defs (c_strategy_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_t_strategy_instances_account
ON t_strategy_instances (c_space_id, c_logical_account_id)
WHERE c_enabled = 1 AND c_logical_account_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_t_strategy_instances_view ON t_strategy_instances (c_space_id, c_view_id, c_enabled);

-- 每次启用产生一个会话，固化当时的 DSL 版本与绑定解析
CREATE TABLE IF NOT EXISTS t_strategy_sessions (
    c_session_id TEXT NOT NULL PRIMARY KEY,
    c_instance_id TEXT NOT NULL,
    c_dsl_hash TEXT NOT NULL,
    c_resolved_json TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_closed_at DATETIME,
    FOREIGN KEY (c_instance_id) REFERENCES t_strategy_instances (c_instance_id),
    FOREIGN KEY (c_dsl_hash) REFERENCES t_strategy_def_versions (c_dsl_hash)
);

CREATE INDEX IF NOT EXISTS idx_t_strategy_sessions_instance ON t_strategy_sessions (c_instance_id, c_ctime);

-- 每个处理过的周期一行：ok 带目标与规则状态，skipped 带原因；只有 ok 行参与投递
CREATE TABLE IF NOT EXISTS t_strategy_results (
    c_result_id TEXT NOT NULL PRIMARY KEY,
    c_instance_id TEXT NOT NULL,
    c_session_id TEXT NOT NULL,
    c_bar_end_time INTEGER NOT NULL,
    c_valid_until INTEGER NOT NULL,
    c_status TEXT NOT NULL,
    c_skip_reason TEXT NOT NULL DEFAULT '',
    c_dsl_hash TEXT NOT NULL,
    c_input_json TEXT NOT NULL,
    c_targets_json TEXT NOT NULL,
    c_rule_states_json TEXT NOT NULL,
    c_summary_json TEXT NOT NULL,
    c_event_data BLOB,
    c_publish_status TEXT NOT NULL DEFAULT 'none',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('ok', 'skipped')),
    CHECK (c_publish_status IN ('none', 'pending', 'sent', 'cancelled')),
    UNIQUE (c_instance_id, c_session_id, c_bar_end_time),
    FOREIGN KEY (c_session_id) REFERENCES t_strategy_sessions (c_session_id)
);

CREATE INDEX IF NOT EXISTS idx_t_strategy_results_instance_bar ON t_strategy_results (c_instance_id, c_bar_end_time);

CREATE INDEX IF NOT EXISTS idx_t_strategy_results_latest_ok
ON t_strategy_results (c_instance_id, c_session_id, c_status, c_bar_end_time);

CREATE INDEX IF NOT EXISTS idx_t_strategy_results_pending
ON t_strategy_results (c_ctime, c_result_id)
WHERE c_publish_status = 'pending';

-- 解释明细：各规则 E(r) 中每个标的的最终阶段、分数、名次、权重与原因；c_ctime 与所属结果相同，按天数清理
CREATE TABLE IF NOT EXISTS t_strategy_result_items (
    c_result_id TEXT NOT NULL,
    c_rule_id TEXT NOT NULL,
    c_instrument_id TEXT NOT NULL,
    c_stage TEXT NOT NULL,
    c_score REAL,
    c_rank INTEGER,
    c_weight TEXT,
    c_reason TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL,
    PRIMARY KEY (c_result_id, c_rule_id, c_instrument_id),
    FOREIGN KEY (c_result_id) REFERENCES t_strategy_results (c_result_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_t_strategy_result_items_ctime ON t_strategy_result_items (c_ctime);

-- 回放任务：基于 View 的研究回放，按天数清理
CREATE TABLE IF NOT EXISTS t_strategy_replays (
    c_replay_id TEXT NOT NULL PRIMARY KEY,
    c_strategy_id TEXT,
    c_dsl_yaml TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_view_id TEXT NOT NULL,
    c_start_time INTEGER NOT NULL,
    c_end_time INTEGER NOT NULL,
    c_fee_bps REAL NOT NULL DEFAULT 0,
    c_factors_json TEXT NOT NULL DEFAULT '{}',
    c_status TEXT NOT NULL,
    c_progress_time INTEGER,
    c_metrics_json TEXT NOT NULL DEFAULT '{}',
    c_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('pending', 'running', 'done', 'failed', 'cancelled')),
    CHECK (c_fee_bps >= 0 AND c_fee_bps <= 1000)
);

CREATE INDEX IF NOT EXISTS idx_t_strategy_replays_space ON t_strategy_replays (c_space_id, c_ctime);

CREATE TABLE IF NOT EXISTS t_strategy_replay_bars (
    c_replay_id TEXT NOT NULL,
    c_bar_end_time INTEGER NOT NULL,
    c_status TEXT NOT NULL,
    c_targets_json TEXT NOT NULL,
    c_positions_json TEXT NOT NULL,
    c_summary_json TEXT NOT NULL,
    c_return REAL,
    c_equity REAL,
    c_turnover REAL,
    c_fee REAL,
    c_holdings INTEGER NOT NULL DEFAULT 0,
    c_frozen INTEGER NOT NULL DEFAULT 0,
    c_skip_reason TEXT NOT NULL DEFAULT '',
    c_unfilled INTEGER NOT NULL DEFAULT 0,
    c_liquidated INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (c_replay_id, c_bar_end_time),
    FOREIGN KEY (c_replay_id) REFERENCES t_strategy_replays (c_replay_id) ON DELETE CASCADE
);
