CREATE TABLE IF NOT EXISTS t_strategies (
    strategy_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    manifest_yaml TEXT NOT NULL,
    source_code TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS t_strategy_runners (
    runner_id TEXT PRIMARY KEY,
    strategy_id TEXT NOT NULL,
    space_id TEXT NOT NULL,
    view_id TEXT NOT NULL,
    frequency TEXT NOT NULL,
    params_json TEXT NOT NULL,
    logical_account_id TEXT,
    status TEXT NOT NULL,
    current_targets_json TEXT NOT NULL,
    command_sequence INTEGER NOT NULL,
    last_result_id TEXT,
    last_success_at INTEGER,
    last_error TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_strategy_runners_enabled_logical_account
ON t_strategy_runners (logical_account_id)
WHERE logical_account_id IS NOT NULL AND status = 'ENABLED';

CREATE TABLE IF NOT EXISTS t_strategy_results (
    result_id TEXT PRIMARY KEY,
    runner_id TEXT NOT NULL,
    strategy_id TEXT NOT NULL,
    trigger_bar_time INTEGER NOT NULL,
    namespace TEXT NOT NULL,
    input_hash TEXT NOT NULL,
    action TEXT NOT NULL,
    output_json TEXT NOT NULL,
    command_sequence INTEGER,
    created_at INTEGER NOT NULL,
    UNIQUE (runner_id, strategy_id, namespace, trigger_bar_time)
);

CREATE TABLE IF NOT EXISTS t_strategy_outbox (
    message_id TEXT PRIMARY KEY,
    event_data BLOB NOT NULL,
    created_at INTEGER NOT NULL
);
