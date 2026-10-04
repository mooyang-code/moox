CREATE TABLE IF NOT EXISTS t_paper_balance_projections (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_applied_fill_count INTEGER NOT NULL DEFAULT 0,
    c_initialized_at INTEGER NOT NULL,
    PRIMARY KEY (c_space_id, c_trading_account_id),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_paper_account_configs (c_space_id, c_trading_account_id),
    CHECK (c_applied_fill_count >= 0),
    CHECK (c_initialized_at > 0)
);

CREATE TABLE IF NOT EXISTS t_paper_asset_balances (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_asset TEXT NOT NULL,
    c_total TEXT NOT NULL,
    PRIMARY KEY (c_space_id, c_trading_account_id, c_asset),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_paper_balance_projections (c_space_id, c_trading_account_id),
    CHECK (c_asset <> ''),
    CHECK (c_total <> '')
);
