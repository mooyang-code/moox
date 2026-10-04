CREATE TABLE IF NOT EXISTS t_trade_orders (
    c_space_id TEXT NOT NULL,
    c_order_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_client_order_id TEXT NOT NULL,
    c_exchange_order_id TEXT NOT NULL DEFAULT '',
    c_exchange TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_instrument_id TEXT NOT NULL,
    c_exchange_symbol TEXT NOT NULL,
    c_order_type TEXT NOT NULL,
    c_time_in_force TEXT NOT NULL DEFAULT '',
    c_side TEXT NOT NULL,
    c_position_side TEXT NOT NULL DEFAULT '',
    c_quantity TEXT NOT NULL,
    c_limit_price TEXT,
    c_reference_price TEXT NOT NULL,
    c_reference_price_at INTEGER NOT NULL,
    c_reduce_only INTEGER NOT NULL DEFAULT 0,
    c_owner_type TEXT NOT NULL,
    c_owner_id TEXT NOT NULL,
    c_logical_account_id TEXT,
    c_runner_id TEXT,
    c_state TEXT NOT NULL,
    c_filled_quantity TEXT NOT NULL DEFAULT '0',
    c_average_price TEXT NOT NULL DEFAULT '0',
    c_reserved_asset TEXT NOT NULL DEFAULT '',
    c_reserved_quantity TEXT NOT NULL DEFAULT '0',
    c_remaining_reserved_quantity TEXT NOT NULL DEFAULT '0',
    c_paper_execution_price TEXT,
    c_first_match_pending INTEGER NOT NULL DEFAULT 0,
    c_reject_reason TEXT NOT NULL DEFAULT '',
    c_exchange_updated_at INTEGER NOT NULL DEFAULT 0,
    c_version INTEGER NOT NULL DEFAULT 1,
    c_submitted_at INTEGER NOT NULL DEFAULT 0,
    c_finished_at INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_order_id),
    UNIQUE (c_space_id, c_trading_account_id, c_client_order_id),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id),
    CHECK (c_first_match_pending IN (0, 1)),
    CHECK (c_owner_type IN ('TARGET', 'OPERATOR', 'EXTERNAL')),
    CHECK (c_owner_id <> ''),
    CHECK (
        (
            c_owner_type = 'TARGET'
            AND c_logical_account_id IS NOT NULL
            AND c_runner_id IS NOT NULL
        )
        OR
        (
            c_owner_type = 'OPERATOR'
            AND c_logical_account_id IS NOT NULL
            AND c_runner_id IS NULL
        )
        OR
        (
            c_owner_type = 'EXTERNAL'
            AND c_runner_id IS NULL
        )
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_trade_orders_exchange_order
ON t_trade_orders (
    c_space_id,
    c_trading_account_id,
    c_exchange_symbol,
    c_exchange_order_id
)
WHERE c_exchange_order_id <> '';

CREATE INDEX IF NOT EXISTS idx_trade_orders_account_state
ON t_trade_orders (c_space_id, c_trading_account_id, c_state, c_mtime);

CREATE INDEX IF NOT EXISTS idx_trade_orders_logical_owner_state
ON t_trade_orders (
    c_space_id,
    c_logical_account_id,
    c_owner_type,
    c_owner_id,
    c_state,
    c_mtime
);

CREATE TABLE IF NOT EXISTS t_order_fills (
    c_space_id TEXT NOT NULL,
    c_fill_id TEXT NOT NULL,
    c_exchange_trade_id TEXT NOT NULL,
    c_order_id TEXT NOT NULL,
    c_exchange_order_id TEXT NOT NULL DEFAULT '',
    c_trading_account_id TEXT NOT NULL,
    c_exchange TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_instrument_id TEXT NOT NULL,
    c_exchange_symbol TEXT NOT NULL,
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
        c_trading_account_id,
        c_exchange_symbol,
        c_exchange_trade_id
    ),
    FOREIGN KEY (c_space_id, c_order_id)
        REFERENCES t_trade_orders (c_space_id, c_order_id),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id)
);

CREATE INDEX IF NOT EXISTS idx_order_fills_order_time
ON t_order_fills (c_space_id, c_order_id, c_traded_at);

CREATE INDEX IF NOT EXISTS idx_order_fills_paper_balance_history
ON t_order_fills (c_space_id, c_trading_account_id, c_traded_at, c_fill_id);

CREATE TABLE IF NOT EXISTS t_trading_positions (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_instrument_id TEXT NOT NULL,
    c_exchange_symbol TEXT NOT NULL,
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
        c_trading_account_id,
        c_exchange_symbol,
        c_position_side
    ),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id)
);
