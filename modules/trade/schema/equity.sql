CREATE TABLE IF NOT EXISTS t_account_equity_points (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_bucket_time INTEGER NOT NULL,
    c_equity TEXT NOT NULL,
    c_available_funds TEXT NOT NULL,
    c_used_margin TEXT NOT NULL,
    c_unrealized_pnl TEXT,
    c_source_time INTEGER NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_trading_account_id, c_bucket_time),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id)
);

CREATE TABLE IF NOT EXISTS t_logical_account_equity_points (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_bucket_time INTEGER NOT NULL,
    c_equity TEXT NOT NULL,
    c_available_funds TEXT NOT NULL,
    c_used_margin TEXT NOT NULL,
    c_unrealized_pnl TEXT,
    c_source_time INTEGER NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_logical_account_id, c_bucket_time),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
);
