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

-- 回放任务：基于 View 的研究回放，按天数与每个空间的个数清理；c_view_generation 是提交时校验区间所用的活动索引代次
CREATE TABLE IF NOT EXISTS t_strategy_replays (
    c_replay_id TEXT NOT NULL PRIMARY KEY,
    c_strategy_id TEXT,
    c_instance_id TEXT,
    c_session_id TEXT,
    c_dsl_yaml TEXT NOT NULL,
    c_dsl_hash TEXT NOT NULL,
    c_view_generation TEXT NOT NULL,
    c_calendar TEXT NOT NULL,
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

-- 回放每期一行：目标、理论持仓账本、摘要与曲线字段，随所属回放清理
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

-- 年龄探针通过的标的：上市时间只会越来越久，通过一次后跨周期、跨重启复用；c_index_id 是 View 的活动索引，索引重建后旧行作废
CREATE TABLE IF NOT EXISTS t_strategy_age_passed (
    c_space_id TEXT NOT NULL,
    c_view_id TEXT NOT NULL,
    c_index_id TEXT NOT NULL,
    c_min_age_bars INTEGER NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_view_id, c_index_id, c_min_age_bars, c_subject_id)
);

-- 因子参数的待提交记录：策略每期求出下一期的参数后，与结果在同一个事务里落库，由独立的投递循环调用
-- FactorMgr.SubmitFactorParams 并记录终态。pending 退避重试到被接受、被拒绝或该期已冻结（too_late）；
-- c_period_time 是参数生效的目标期次（Unix 毫秒）。
CREATE TABLE IF NOT EXISTS t_strategy_param_submissions (
    c_request_id TEXT NOT NULL PRIMARY KEY,
    c_instance_id TEXT NOT NULL,
    c_session_id TEXT NOT NULL,
    c_result_id TEXT NOT NULL,
    c_task_id TEXT NOT NULL,
    c_period_time INTEGER NOT NULL,
    c_based_on_period INTEGER NOT NULL,
    c_params_json TEXT NOT NULL,
    c_notes TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_attempts INTEGER NOT NULL DEFAULT 0,
    c_next_attempt DATETIME NOT NULL,
    c_outcome_json TEXT NOT NULL DEFAULT '[]',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('pending', 'accepted', 'too_late', 'rejected', 'abandoned')),
    FOREIGN KEY (c_result_id) REFERENCES t_strategy_results (c_result_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_t_strategy_param_submissions_pending
ON t_strategy_param_submissions (c_next_attempt, c_request_id)
WHERE c_status = 'pending';

CREATE INDEX IF NOT EXISTS idx_t_strategy_param_submissions_instance
ON t_strategy_param_submissions (c_instance_id, c_period_time);

-- 实例的外显设置：只存隐藏的内容（默认全部展示）。只影响控制台展示与默认导出，不影响 View 的列和策略能读到的数据，
-- 不进会话快照与哈希，改了立即生效。c_hidden_json 形如 {"categories": [...], "members": [...], "columns": [...]}。
CREATE TABLE IF NOT EXISTS t_strategy_display_settings (
    c_instance_id TEXT NOT NULL PRIMARY KEY,
    c_hidden_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (c_instance_id) REFERENCES t_strategy_instances (c_instance_id) ON DELETE CASCADE
);
