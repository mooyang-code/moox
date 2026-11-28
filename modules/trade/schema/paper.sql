CREATE TABLE IF NOT EXISTS t_paper_account_configs (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_initial_balance TEXT NOT NULL,
    c_maker_fee_rate TEXT NOT NULL,
    c_taker_fee_rate TEXT NOT NULL,
    c_slippage_bps TEXT NOT NULL,
    -- 已结束订单与成交保留的天数，0 表示不清理；所有模拟账户都按它清理（见交易模块文档）
    c_order_retention_days INTEGER NOT NULL DEFAULT 5,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_initial_balance <> '0'),
    CHECK (c_maker_fee_rate <> ''),
    CHECK (c_taker_fee_rate <> ''),
    CHECK (c_slippage_bps <> ''),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id),
    PRIMARY KEY (c_space_id, c_trading_account_id)
);

-- 模拟账户的资产余额：建账户时写入结算资产的初始资金，此后只改不删；账户有余额行即表示余额已初始化
CREATE TABLE IF NOT EXISTS t_paper_asset_balances (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_asset TEXT NOT NULL,
    c_total TEXT NOT NULL,
    CHECK (c_asset <> ''),
    CHECK (c_total <> ''),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_paper_account_configs (c_space_id, c_trading_account_id),
    PRIMARY KEY (c_space_id, c_trading_account_id, c_asset)
);

-- 模拟盘永续合约资金费：每个账户、合约、结算时刻一条，插入与改余额在同一个事务里；最后结算到哪个时刻取这里的最大结算时刻。
-- 金额 = -持仓数量（多为正、空为负）× 标记价格 × 费率，记入账户的结算资产。
CREATE TABLE IF NOT EXISTS t_paper_funding_fees (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_exchange_symbol TEXT NOT NULL,
    c_settled_at INTEGER NOT NULL,
    c_funding_rate TEXT NOT NULL,
    c_mark_price TEXT NOT NULL,
    c_quantity TEXT NOT NULL,
    c_amount TEXT NOT NULL,
    c_asset TEXT NOT NULL,
    CHECK (c_exchange_symbol <> ''),
    CHECK (c_settled_at > 0),
    CHECK (c_asset <> ''),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id),
    PRIMARY KEY (c_space_id, c_trading_account_id, c_exchange_symbol, c_settled_at)
);

CREATE INDEX IF NOT EXISTS idx_paper_funding_fees_account_time
ON t_paper_funding_fees (c_space_id, c_trading_account_id, c_settled_at);
