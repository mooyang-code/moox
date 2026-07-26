CREATE TABLE IF NOT EXISTS t_ledger_transactions (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_transaction_id TEXT NOT NULL,
    c_biz_type TEXT NOT NULL,
    c_ref_type TEXT NOT NULL,
    c_ref_id TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_transaction_id),
    UNIQUE (c_space_id, c_ref_type, c_ref_id, c_biz_type)
);

CREATE TABLE IF NOT EXISTS t_ledger_entries (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_transaction_id TEXT NOT NULL,
    c_account_id TEXT NOT NULL,
    c_asset TEXT NOT NULL,
    c_bucket TEXT NOT NULL,
    c_amount TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (c_space_id, c_transaction_id) REFERENCES t_ledger_transactions (c_space_id, c_transaction_id)
);

CREATE INDEX IF NOT EXISTS idx_ledger_entries_account ON t_ledger_entries (c_space_id, c_account_id, c_asset, c_bucket);

CREATE TABLE IF NOT EXISTS t_trade_balance_projections (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_account_id TEXT NOT NULL,
    c_asset TEXT NOT NULL,
    c_bucket TEXT NOT NULL,
    c_amount TEXT NOT NULL DEFAULT '0',
    c_version INTEGER NOT NULL DEFAULT 1,
    UNIQUE (c_space_id, c_account_id, c_asset, c_bucket)
);

CREATE TABLE IF NOT EXISTS t_trade_position_projections (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_account_id TEXT NOT NULL,
    c_symbol TEXT NOT NULL,
    c_quantity TEXT NOT NULL DEFAULT '0',
    c_average_price TEXT NOT NULL DEFAULT '0',
    c_realized_pnl TEXT NOT NULL DEFAULT '0',
    c_version INTEGER NOT NULL DEFAULT 1,
    UNIQUE (c_space_id, c_account_id, c_symbol)
);
