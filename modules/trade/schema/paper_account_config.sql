-- c_order_retention_days 是已结束订单与成交保留的天数，0 表示不清理；只有纯合约模拟账户会被清理（见交易模块文档）。
-- 旧库用 ALTER TABLE ADD COLUMN 加这一列，SQLite 把它接在最后一个字段之后、表级约束之前，
-- 启动检查又比对建表语句原文，所以它必须是最后一个字段；注释也不能写进建表语句里。
CREATE TABLE IF NOT EXISTS t_paper_account_configs (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_initial_balance TEXT NOT NULL,
    c_maker_fee_rate TEXT NOT NULL,
    c_taker_fee_rate TEXT NOT NULL,
    c_slippage_bps TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_order_retention_days INTEGER NOT NULL DEFAULT 5,
    PRIMARY KEY (c_space_id, c_trading_account_id),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id),
    CHECK (c_initial_balance <> '0'),
    CHECK (c_maker_fee_rate <> ''),
    CHECK (c_taker_fee_rate <> ''),
    CHECK (c_slippage_bps <> '')
);
