CREATE TABLE IF NOT EXISTS t_trade_inbox (
 c_id INTEGER PRIMARY KEY AUTOINCREMENT, c_consumer TEXT NOT NULL, c_message_id TEXT NOT NULL, c_event_name TEXT NOT NULL,
 c_processed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(c_consumer,c_message_id)
);
CREATE TABLE IF NOT EXISTS t_trade_outbox (
 c_id INTEGER PRIMARY KEY AUTOINCREMENT, c_message_id TEXT NOT NULL, c_event_data BLOB NOT NULL,
 c_status TEXT NOT NULL DEFAULT 'PENDING', c_attempts INTEGER NOT NULL DEFAULT 0, c_lease_until DATETIME, c_claim_token TEXT NOT NULL DEFAULT '',
 c_last_error TEXT NOT NULL DEFAULT '', c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, c_published_at DATETIME,
 UNIQUE(c_message_id)
);
CREATE INDEX IF NOT EXISTS idx_trade_outbox_claim ON t_trade_outbox(c_status,c_lease_until,c_id);
CREATE TABLE IF NOT EXISTS t_reconciliation_runs (
 c_id INTEGER PRIMARY KEY AUTOINCREMENT, c_space_id TEXT NOT NULL, c_run_id TEXT NOT NULL, c_account_id TEXT NOT NULL,
 c_status TEXT NOT NULL, c_started_at DATETIME NOT NULL, c_completed_at DATETIME, c_error TEXT NOT NULL DEFAULT '',
 UNIQUE(c_space_id,c_run_id)
);
CREATE TABLE IF NOT EXISTS t_reconciliation_differences (
 c_id INTEGER PRIMARY KEY AUTOINCREMENT, c_space_id TEXT NOT NULL, c_run_id TEXT NOT NULL, c_kind TEXT NOT NULL,
 c_resource_id TEXT NOT NULL, c_local_value TEXT NOT NULL, c_exchange_value TEXT NOT NULL, c_action TEXT NOT NULL,
 c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
