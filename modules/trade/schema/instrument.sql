CREATE TABLE IF NOT EXISTS t_trade_instruments (
    c_exchange TEXT NOT NULL,
    c_environment TEXT NOT NULL DEFAULT 'PRODUCTION',
    c_market_type TEXT NOT NULL,
    c_exchange_symbol TEXT NOT NULL,
    c_instrument_id TEXT NOT NULL DEFAULT '',
    c_base_asset TEXT NOT NULL,
    c_quote_asset TEXT NOT NULL,
    c_settlement_asset TEXT NOT NULL DEFAULT '',
    c_linear INTEGER NOT NULL DEFAULT 0,
    c_contract_value TEXT NOT NULL DEFAULT '0',
    c_contract_value_asset TEXT NOT NULL DEFAULT '',
    c_exchange_quantity_step TEXT NOT NULL,
    c_min_exchange_quantity TEXT NOT NULL DEFAULT '0',
    c_price_tick TEXT NOT NULL,
    c_min_notional TEXT NOT NULL DEFAULT '0',
    c_status TEXT NOT NULL,
    c_exchange_updated_at INTEGER NOT NULL DEFAULT 0,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_exchange, c_environment, c_market_type, c_exchange_symbol),
    CHECK (c_linear IN (0, 1)),
    CHECK (
        c_market_type <> 'SWAP'
        OR (
            c_linear = 1
            AND c_contract_value <> '0'
            AND c_contract_value_asset = c_base_asset
            AND c_min_exchange_quantity <> '0'
        )
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_exchange_instruments_id
ON t_trade_instruments (c_exchange, c_environment, c_market_type, c_instrument_id)
WHERE c_instrument_id <> '';

CREATE INDEX IF NOT EXISTS idx_trade_instruments_status
ON t_trade_instruments (c_exchange, c_environment, c_market_type, c_status);
