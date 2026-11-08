-- 下架名单：全局的，不分空间，下架是交易所层面的事实。名单里的标的只许减仓、不许加仓。
CREATE TABLE IF NOT EXISTS t_trade_blocked_instruments (
    c_exchange TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_exchange_symbol TEXT NOT NULL,
    -- 内部标的 ID 和基础资产只用于展示和审计，匹配一律在使用时查标的表；写入时查不到则为空串
    c_instrument_id TEXT NOT NULL DEFAULT '',
    c_base_asset TEXT NOT NULL DEFAULT '',
    c_reason TEXT NOT NULL,
    c_source_event_id TEXT NOT NULL DEFAULT '',
    c_blocked_at INTEGER NOT NULL,
    PRIMARY KEY (c_exchange, c_market_type, c_exchange_symbol),
    CHECK (c_exchange <> ''),
    CHECK (c_market_type IN ('SPOT', 'SWAP', 'STOCK')),
    CHECK (c_exchange_symbol <> ''),
    CHECK (c_reason <> '')
);
