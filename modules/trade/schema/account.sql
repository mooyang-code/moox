PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_exchange_accounts (
    c_space_id TEXT NOT NULL,
    c_exchange_account_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_exchange TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_execution_mode TEXT NOT NULL,
    c_credential_secret_id TEXT NOT NULL,
    c_settlement_asset TEXT NOT NULL,
    c_margin_mode TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL,
    c_paused INTEGER NOT NULL DEFAULT 0,
    c_pause_reason TEXT NOT NULL DEFAULT '',
    c_ready INTEGER NOT NULL DEFAULT 0,
    c_sync_symbols_json TEXT NOT NULL DEFAULT '[]',
    c_leverage_settings_json TEXT NOT NULL DEFAULT '{}',
    c_fill_cursors_json TEXT NOT NULL DEFAULT '{}',
    c_snapshot_json TEXT NOT NULL DEFAULT '{}',
    c_snapshot_source_time INTEGER NOT NULL DEFAULT 0,
    c_last_sync_at INTEGER NOT NULL DEFAULT 0,
    c_last_ready_at INTEGER NOT NULL DEFAULT 0,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_exchange_account_id),
    UNIQUE (c_space_id, c_name),
    CHECK (json_valid(c_leverage_settings_json)),
    CHECK (json_type(c_leverage_settings_json) = 'object'),
    CHECK (json_valid(c_sync_symbols_json)),
    CHECK (json_type(c_sync_symbols_json) = 'array'),
    CHECK (json_valid(c_fill_cursors_json)),
    CHECK (json_type(c_fill_cursors_json) = 'object'),
    CHECK (json_valid(c_snapshot_json)),
    CHECK (json_type(c_snapshot_json) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_exchange_accounts_status
ON t_exchange_accounts (c_space_id, c_status);

CREATE INDEX IF NOT EXISTS idx_exchange_accounts_exchange
ON t_exchange_accounts (c_exchange, c_market_type);

CREATE UNIQUE INDEX IF NOT EXISTS uk_exchange_accounts_id
ON t_exchange_accounts (c_exchange_account_id);
