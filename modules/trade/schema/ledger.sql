CREATE TABLE IF NOT EXISTS t_ledger_transactions (
    c_space_id TEXT NOT NULL,
    c_transaction_id TEXT NOT NULL,
    c_exchange_account_id TEXT NOT NULL,
    c_transaction_type TEXT NOT NULL,
    c_source_type TEXT NOT NULL,
    c_source_id TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_transaction_id),
    UNIQUE (
        c_space_id,
        c_exchange_account_id,
        c_source_type,
        c_source_id
    ),
    FOREIGN KEY (c_space_id, c_exchange_account_id)
        REFERENCES t_exchange_accounts (c_space_id, c_exchange_account_id),
    CHECK (
        c_transaction_type IN (
            'RESERVATION',
            'RESERVATION_RELEASE',
            'FILL_SETTLEMENT',
            'FEE',
            'SYNC_ADJUSTMENT'
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_ledger_transactions_account_time
ON t_ledger_transactions (c_space_id, c_exchange_account_id, c_ctime);

CREATE TABLE IF NOT EXISTS t_ledger_entries (
    c_space_id TEXT NOT NULL,
    c_transaction_id TEXT NOT NULL,
    c_entry_no INTEGER NOT NULL,
    c_asset TEXT NOT NULL,
    c_bucket TEXT NOT NULL,
    c_amount TEXT NOT NULL,
    PRIMARY KEY (c_space_id, c_transaction_id, c_entry_no),
    FOREIGN KEY (c_space_id, c_transaction_id)
        REFERENCES t_ledger_transactions (c_space_id, c_transaction_id)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_ledger_entries_asset_bucket
ON t_ledger_entries (c_space_id, c_asset, c_bucket);

CREATE TABLE IF NOT EXISTS t_trade_balance_projections (
    c_space_id TEXT NOT NULL,
    c_exchange_account_id TEXT NOT NULL,
    c_asset TEXT NOT NULL,
    c_bucket TEXT NOT NULL,
    c_amount TEXT NOT NULL DEFAULT '0',
    c_version INTEGER NOT NULL DEFAULT 1,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (
        c_space_id,
        c_exchange_account_id,
        c_asset,
        c_bucket
    ),
    FOREIGN KEY (c_space_id, c_exchange_account_id)
        REFERENCES t_exchange_accounts (c_space_id, c_exchange_account_id)
);
