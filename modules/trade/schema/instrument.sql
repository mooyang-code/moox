CREATE TABLE IF NOT EXISTS t_exchange_instruments (
    c_exchange TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_symbol TEXT NOT NULL,
    c_instrument_id TEXT NOT NULL DEFAULT '',
    c_base_asset TEXT NOT NULL,
    c_quote_asset TEXT NOT NULL,
    c_settlement_asset TEXT NOT NULL DEFAULT '',
    c_price_tick TEXT NOT NULL,
    c_quantity_step TEXT NOT NULL,
    c_min_quantity TEXT NOT NULL DEFAULT '0',
    c_min_notional TEXT NOT NULL DEFAULT '0',
    c_contract_size TEXT NOT NULL DEFAULT '1',
    c_status TEXT NOT NULL,
    c_exchange_updated_at INTEGER NOT NULL DEFAULT 0,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_exchange, c_market_type, c_symbol)
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_exchange_instruments_id
ON t_exchange_instruments (c_exchange, c_market_type, c_instrument_id)
WHERE c_instrument_id <> '';

CREATE INDEX IF NOT EXISTS idx_exchange_instruments_status
ON t_exchange_instruments (c_exchange, c_market_type, c_status);
