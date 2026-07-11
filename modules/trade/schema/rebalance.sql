CREATE TABLE IF NOT EXISTS t_rebalance_runs (
 c_id INTEGER PRIMARY KEY AUTOINCREMENT, c_space_id TEXT NOT NULL, c_run_id TEXT NOT NULL, c_account_id TEXT NOT NULL,
 c_idempotency_key TEXT NOT NULL, c_market_snapshot_id TEXT NOT NULL, c_position_snapshot_id TEXT NOT NULL,
 c_rules_version TEXT NOT NULL, c_algorithm_name TEXT NOT NULL, c_algorithm_version TEXT NOT NULL, c_status TEXT NOT NULL,
 c_version INTEGER NOT NULL DEFAULT 1, c_residual TEXT NOT NULL DEFAULT '{}', c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(c_space_id,c_run_id), UNIQUE(c_space_id,c_idempotency_key)
);
CREATE TABLE IF NOT EXISTS t_rebalance_targets (
 c_id INTEGER PRIMARY KEY AUTOINCREMENT, c_space_id TEXT NOT NULL, c_run_id TEXT NOT NULL, c_symbol TEXT NOT NULL,
 c_target_quantity TEXT NOT NULL, UNIQUE(c_space_id,c_run_id,c_symbol)
);
CREATE TABLE IF NOT EXISTS t_rebalance_legs (
 c_id INTEGER PRIMARY KEY AUTOINCREMENT, c_space_id TEXT NOT NULL, c_run_id TEXT NOT NULL, c_leg_id TEXT NOT NULL,
 c_symbol TEXT NOT NULL, c_action TEXT NOT NULL, c_quantity TEXT NOT NULL, c_reduce_only INTEGER NOT NULL DEFAULT 0,
 c_sequence INTEGER NOT NULL, c_depends_on TEXT NOT NULL DEFAULT '[]', c_plan_id TEXT NOT NULL DEFAULT '', c_status TEXT NOT NULL,
 UNIQUE(c_space_id,c_leg_id), UNIQUE(c_space_id,c_run_id,c_sequence)
);
