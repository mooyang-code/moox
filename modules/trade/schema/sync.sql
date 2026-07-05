-- Trade scheduled sync cursors.
CREATE TABLE IF NOT EXISTS t_trade_sync_cursors (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_account_id TEXT NOT NULL,
    c_channel_id TEXT NOT NULL DEFAULT '',
    c_exchange TEXT NOT NULL DEFAULT '',
    c_market_type TEXT NOT NULL DEFAULT '',
    c_sync_type TEXT NOT NULL,
    c_symbol TEXT NOT NULL DEFAULT '',
    c_cursor_start_ms INTEGER NOT NULL DEFAULT 0,
    c_cursor_end_ms INTEGER NOT NULL DEFAULT 0,
    c_last_success_at DATETIME,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_is_enabled INTEGER NOT NULL DEFAULT 1,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_trade_sync_cursors_unique
ON t_trade_sync_cursors(c_space_id, c_account_id, c_sync_type, c_symbol);

CREATE INDEX IF NOT EXISTS idx_trade_sync_cursors_account
ON t_trade_sync_cursors(c_space_id, c_account_id, c_channel_id);

CREATE INDEX IF NOT EXISTS idx_trade_sync_cursors_type
ON t_trade_sync_cursors(c_space_id, c_sync_type, c_is_enabled);

CREATE TRIGGER IF NOT EXISTS update_trade_sync_cursors_mtime AFTER UPDATE ON t_trade_sync_cursors BEGIN
    UPDATE t_trade_sync_cursors SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;
