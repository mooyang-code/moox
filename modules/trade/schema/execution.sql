CREATE TABLE IF NOT EXISTS t_trade_order_aggregates (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_order_id TEXT NOT NULL,
    c_client_order_id TEXT NOT NULL,
    c_account_id TEXT NOT NULL,
    c_channel_id TEXT NOT NULL,
    c_symbol TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_base_asset TEXT NOT NULL,
    c_quote_asset TEXT NOT NULL,
    c_execution_mode TEXT NOT NULL DEFAULT 'live',
    c_side TEXT NOT NULL,
    c_quantity TEXT NOT NULL,
    c_price TEXT NOT NULL,
    c_filled_quantity TEXT NOT NULL DEFAULT '0',
    c_reduce_only INTEGER NOT NULL DEFAULT 0,
    c_reserved_asset TEXT NOT NULL,
    c_reserved_amount TEXT NOT NULL,
    c_consumed_reserved TEXT NOT NULL DEFAULT '0',
    c_state TEXT NOT NULL,
    c_exchange_order_id TEXT NOT NULL DEFAULT '',
    c_version INTEGER NOT NULL DEFAULT 1,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_order_id),
    UNIQUE (c_space_id, c_client_order_id)
);

CREATE TABLE IF NOT EXISTS t_trade_fill_events (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_fill_id TEXT NOT NULL,
    c_exchange_trade_id TEXT NOT NULL,
    c_account_id TEXT NOT NULL,
    c_channel_id TEXT NOT NULL,
    c_symbol TEXT NOT NULL,
    c_order_id TEXT NOT NULL,
    c_quantity TEXT NOT NULL,
    c_price TEXT NOT NULL,
    c_fee TEXT NOT NULL DEFAULT '0',
    c_fee_asset TEXT NOT NULL DEFAULT '',
    c_traded_at_ms INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_fill_id),
    UNIQUE (c_space_id, c_account_id, c_channel_id, c_symbol, c_exchange_trade_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_trade_order_exchange_scope
ON t_trade_order_aggregates (c_space_id, c_channel_id, c_symbol, c_exchange_order_id)
WHERE c_exchange_order_id <> '';

CREATE TABLE IF NOT EXISTS t_execution_plans (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_plan_id TEXT NOT NULL,
    c_order_id TEXT NOT NULL,
    c_algorithm_name TEXT NOT NULL,
    c_algorithm_version TEXT NOT NULL,
    c_algorithm_config TEXT NOT NULL DEFAULT '{}',
    c_input_hash TEXT NOT NULL,
    c_rules_version TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_version INTEGER NOT NULL DEFAULT 1,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_plan_id)
);

CREATE TABLE IF NOT EXISTS t_execution_slices (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_slice_id TEXT NOT NULL,
    c_plan_id TEXT NOT NULL,
    c_sequence INTEGER NOT NULL,
    c_quantity TEXT NOT NULL,
    c_filled_quantity TEXT NOT NULL DEFAULT '0',
    c_state TEXT NOT NULL,
    c_depends_on TEXT NOT NULL DEFAULT '[]',
    c_attempts INTEGER NOT NULL DEFAULT 0,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_slice_id),
    UNIQUE (c_space_id, c_plan_id, c_sequence)
);

CREATE TABLE IF NOT EXISTS t_trade_sagas (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_saga_id TEXT NOT NULL,
    c_type TEXT NOT NULL,
    c_state TEXT NOT NULL,
    c_order_id TEXT NOT NULL,
    c_replacement_order_id TEXT NOT NULL DEFAULT '',
    c_version INTEGER NOT NULL DEFAULT 1,
    c_payload TEXT NOT NULL DEFAULT '{}',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_saga_id)
);

CREATE TABLE IF NOT EXISTS t_trade_controls (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_target_type TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_paused INTEGER NOT NULL DEFAULT 0,
    c_reason TEXT NOT NULL DEFAULT '',
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_target_type, c_target_id)
);
