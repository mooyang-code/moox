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
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id),
    CHECK (c_exchange_symbol <> ''),
    CHECK (c_settled_at > 0),
    CHECK (c_asset <> ''),
    PRIMARY KEY (c_space_id, c_trading_account_id, c_exchange_symbol, c_settled_at)
);

CREATE INDEX IF NOT EXISTS idx_paper_funding_fees_account_time
ON t_paper_funding_fees (c_space_id, c_trading_account_id, c_settled_at);
