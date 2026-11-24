-- 模拟账户的资产余额：建账户时写入结算资产的初始资金，此后只改不删；账户有余额行即表示余额已初始化
CREATE TABLE IF NOT EXISTS t_paper_asset_balances (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_asset TEXT NOT NULL,
    c_total TEXT NOT NULL,
    PRIMARY KEY (c_space_id, c_trading_account_id, c_asset),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_paper_account_configs (c_space_id, c_trading_account_id),
    CHECK (c_asset <> ''),
    CHECK (c_total <> '')
);
