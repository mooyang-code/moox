CREATE TABLE IF NOT EXISTS t_trade_orders (
    c_space_id TEXT NOT NULL,
    c_order_id TEXT NOT NULL,
    c_exchange_account_id TEXT NOT NULL,
    c_client_order_id TEXT NOT NULL,
    c_exchange_order_id TEXT NOT NULL DEFAULT '',
    c_exchange TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_symbol TEXT NOT NULL,
    c_order_type TEXT NOT NULL,
    c_time_in_force TEXT NOT NULL DEFAULT '',
    c_side TEXT NOT NULL,
    c_position_side TEXT NOT NULL DEFAULT '',
    c_quantity TEXT NOT NULL,
    c_limit_price TEXT,
    c_reference_price TEXT NOT NULL,
    c_reference_price_at INTEGER NOT NULL,
    c_reduce_only INTEGER NOT NULL DEFAULT 0,
    c_source TEXT NOT NULL,
    c_strategy_execution_id TEXT NOT NULL DEFAULT '',
    c_state TEXT NOT NULL,
    c_filled_quantity TEXT NOT NULL DEFAULT '0',
    c_average_price TEXT NOT NULL DEFAULT '0',
    c_reserved_asset TEXT NOT NULL DEFAULT '',
    c_reserved_quantity TEXT NOT NULL DEFAULT '0',
    c_remaining_reserved_quantity TEXT NOT NULL DEFAULT '0',
    c_reject_reason TEXT NOT NULL DEFAULT '',
    c_exchange_updated_at INTEGER NOT NULL DEFAULT 0,
    c_version INTEGER NOT NULL DEFAULT 1,
    c_submitted_at INTEGER NOT NULL DEFAULT 0,
    c_finished_at INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_order_id),
    UNIQUE (c_space_id, c_exchange_account_id, c_client_order_id),
    FOREIGN KEY (c_space_id, c_exchange_account_id)
        REFERENCES t_exchange_accounts (c_space_id, c_exchange_account_id),
    FOREIGN KEY (c_exchange, c_market_type, c_symbol)
        REFERENCES t_exchange_instruments (c_exchange, c_market_type, c_symbol)
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_trade_orders_exchange_order
ON t_trade_orders (
    c_space_id,
    c_exchange_account_id,
    c_symbol,
    c_exchange_order_id
)
WHERE c_exchange_order_id <> '';

CREATE INDEX IF NOT EXISTS idx_trade_orders_account_state
ON t_trade_orders (c_space_id, c_exchange_account_id, c_state, c_mtime);

CREATE TABLE IF NOT EXISTS t_order_fills (
    c_space_id TEXT NOT NULL,
    c_fill_id TEXT NOT NULL,
    c_exchange_trade_id TEXT NOT NULL,
    c_order_id TEXT NOT NULL,
    c_exchange_order_id TEXT NOT NULL DEFAULT '',
    c_exchange_account_id TEXT NOT NULL,
    c_exchange TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_symbol TEXT NOT NULL,
    c_side TEXT NOT NULL,
    c_position_side TEXT NOT NULL DEFAULT '',
    c_price TEXT NOT NULL,
    c_quantity TEXT NOT NULL,
    c_fee TEXT NOT NULL DEFAULT '0',
    c_fee_asset TEXT NOT NULL DEFAULT '',
    c_settlement_asset TEXT NOT NULL DEFAULT '',
    c_realized_pnl TEXT NOT NULL DEFAULT '0',
    c_role TEXT NOT NULL DEFAULT '',
    c_traded_at INTEGER NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_fill_id),
    UNIQUE (
        c_space_id,
        c_exchange_account_id,
        c_symbol,
        c_exchange_trade_id
    ),
    FOREIGN KEY (c_space_id, c_order_id)
        REFERENCES t_trade_orders (c_space_id, c_order_id),
    FOREIGN KEY (c_space_id, c_exchange_account_id)
        REFERENCES t_exchange_accounts (c_space_id, c_exchange_account_id)
);

CREATE INDEX IF NOT EXISTS idx_order_fills_order_time
ON t_order_fills (c_space_id, c_order_id, c_traded_at);

CREATE TABLE IF NOT EXISTS t_exchange_positions (
    c_space_id TEXT NOT NULL,
    c_exchange_account_id TEXT NOT NULL,
    c_symbol TEXT NOT NULL,
    c_position_side TEXT NOT NULL,
    c_signed_quantity TEXT NOT NULL DEFAULT '0',
    c_entry_price TEXT NOT NULL DEFAULT '0',
    c_mark_price TEXT NOT NULL DEFAULT '0',
    c_leverage TEXT NOT NULL DEFAULT '0',
    c_margin_mode TEXT NOT NULL DEFAULT '',
    c_used_margin TEXT NOT NULL DEFAULT '0',
    c_liquidation_price TEXT NOT NULL DEFAULT '0',
    c_unrealized_pnl TEXT NOT NULL DEFAULT '0',
    c_realized_pnl TEXT NOT NULL DEFAULT '0',
    c_exchange_updated_at INTEGER NOT NULL DEFAULT 0,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (
        c_space_id,
        c_exchange_account_id,
        c_symbol,
        c_position_side
    ),
    FOREIGN KEY (c_space_id, c_exchange_account_id)
        REFERENCES t_exchange_accounts (c_space_id, c_exchange_account_id)
);

CREATE TABLE IF NOT EXISTS t_target_executions (
    c_space_id TEXT NOT NULL,
    c_execution_id TEXT NOT NULL,
    c_event_id TEXT NOT NULL,
    c_strategy_run_id TEXT NOT NULL DEFAULT '',
    c_execution_binding_id TEXT NOT NULL,
    c_exchange_account_id TEXT NOT NULL,
    c_command_sequence INTEGER NOT NULL,
    c_not_after INTEGER NOT NULL DEFAULT 0,
    c_data_revision TEXT NOT NULL DEFAULT '',
    c_targets_json TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_progress TEXT NOT NULL DEFAULT '',
    c_residual_quantity TEXT NOT NULL DEFAULT '0',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_execution_id),
    UNIQUE (c_space_id, c_execution_binding_id, c_command_sequence),
    UNIQUE (c_space_id, c_event_id),
    UNIQUE (c_space_id, c_execution_binding_id),
    FOREIGN KEY (c_space_id, c_exchange_account_id)
        REFERENCES t_exchange_accounts (c_space_id, c_exchange_account_id),
    CHECK (json_valid(c_targets_json)),
    CHECK (json_type(c_targets_json) = 'array')
);

CREATE INDEX IF NOT EXISTS idx_target_executions_account_status
ON t_target_executions (
    c_space_id,
    c_exchange_account_id,
    c_status,
    c_mtime
);
