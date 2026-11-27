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
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id),
    PRIMARY KEY (c_space_id, c_trading_account_id, c_bucket_time)
);

CREATE INDEX IF NOT EXISTS idx_account_equity_points_bucket_time ON t_account_equity_points (c_bucket_time);

CREATE TRIGGER IF NOT EXISTS trg_t_account_equity_points_mtime
AFTER UPDATE ON t_account_equity_points
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_account_equity_points
    SET c_mtime = CURRENT_TIMESTAMP
    WHERE c_space_id = OLD.c_space_id
        AND c_trading_account_id = OLD.c_trading_account_id
        AND c_bucket_time = OLD.c_bucket_time;
END;

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
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id),
    PRIMARY KEY (c_space_id, c_logical_account_id, c_bucket_time)
);

CREATE INDEX IF NOT EXISTS idx_logical_account_equity_points_bucket_time ON t_logical_account_equity_points (c_bucket_time);

CREATE TRIGGER IF NOT EXISTS trg_t_logical_account_equity_points_mtime
AFTER UPDATE ON t_logical_account_equity_points
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_logical_account_equity_points
    SET c_mtime = CURRENT_TIMESTAMP
    WHERE c_space_id = OLD.c_space_id
        AND c_logical_account_id = OLD.c_logical_account_id
        AND c_bucket_time = OLD.c_bucket_time;
END;
