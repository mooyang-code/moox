CREATE TABLE IF NOT EXISTS t_paper_account_configs (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_initial_balance TEXT NOT NULL,
    c_maker_fee_rate TEXT NOT NULL,
    c_taker_fee_rate TEXT NOT NULL,
    c_slippage_bps TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- 已结束订单与成交保留的天数，0 表示不清理；所有模拟账户都按它清理（见交易模块文档）
    c_order_retention_days INTEGER NOT NULL DEFAULT 5,
    PRIMARY KEY (c_space_id, c_trading_account_id),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id),
    CHECK (c_initial_balance <> '0'),
    CHECK (c_maker_fee_rate <> ''),
    CHECK (c_taker_fee_rate <> ''),
    CHECK (c_slippage_bps <> '')
);
