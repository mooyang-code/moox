CREATE TABLE IF NOT EXISTS t_strategies (
    strategy_id TEXT PRIMARY KEY,
    strategy_name TEXT NOT NULL,
    dsl_yaml TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS t_strategy_instances (
    instance_id TEXT PRIMARY KEY,
    strategy_id TEXT NOT NULL,
    space_id TEXT NOT NULL,
    input_bindings_json TEXT NOT NULL,
    logical_account_id TEXT,
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    session_id TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_strategy_instances_enabled_account
ON t_strategy_instances (space_id, logical_account_id)
WHERE enabled = 1 AND logical_account_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS t_strategy_results (
    result_id TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    bar_end_time INTEGER NOT NULL,
    valid_until INTEGER NOT NULL,
    snapshot_json TEXT NOT NULL,
    targets_json TEXT NOT NULL,
    rule_states_json TEXT NOT NULL,
    event_data BLOB,
    publish_status TEXT NOT NULL CHECK (publish_status IN ('none', 'pending', 'sent', 'cancelled')),
    created_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_strategy_results_session_bar
ON t_strategy_results (instance_id, session_id, bar_end_time);

CREATE INDEX IF NOT EXISTS ix_strategy_results_pending
ON t_strategy_results (created_at, result_id)
WHERE publish_status = 'pending';
