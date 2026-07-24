CREATE TABLE IF NOT EXISTS t_trade_signal_recommendations (
 c_id INTEGER PRIMARY KEY AUTOINCREMENT,
 c_space_id TEXT NOT NULL,
 c_event_id TEXT NOT NULL,
 c_signal_id TEXT NOT NULL,
 c_strategy_id TEXT NOT NULL,
 c_symbol TEXT NOT NULL,
 c_side TEXT NOT NULL,
 c_action TEXT NOT NULL,
 c_target_price TEXT NOT NULL DEFAULT '',
 c_stop_loss_price TEXT NOT NULL DEFAULT '',
 c_take_profit_price TEXT NOT NULL DEFAULT '',
 c_signal_time DATETIME NOT NULL,
 c_tags TEXT NOT NULL DEFAULT '{}',
 c_received_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(c_space_id,c_event_id),
 UNIQUE(c_space_id,c_signal_id)
);
CREATE INDEX IF NOT EXISTS idx_trade_signals_space_symbol ON t_trade_signal_recommendations(c_space_id,c_symbol,c_signal_time DESC);
CREATE INDEX IF NOT EXISTS idx_trade_signals_strategy ON t_trade_signal_recommendations(c_space_id,c_strategy_id,c_signal_time DESC);
