-- 币安普通账户在同一执行账户的现货、合约钱包之间的划转记录（不做跨账户划转和提现）
CREATE TABLE IF NOT EXISTS t_trade_wallet_transfers (
    c_space_id TEXT NOT NULL,
    c_transfer_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    -- 触发这次划转的目标；风控等没有目标的划转为空串
    c_target_id TEXT NOT NULL DEFAULT '',
    -- TO_SPOT 划到现货钱包，TO_FUTURES 归集回合约钱包
    c_direction TEXT NOT NULL,
    c_asset TEXT NOT NULL,
    c_amount TEXT NOT NULL,
    -- 请求交易所之前先写入的本地引用号；超时后靠它连同时间窗口、金额对账交易所的划转记录
    c_client_ref TEXT NOT NULL,
    c_exchange_transfer_id TEXT NOT NULL DEFAULT '',
    c_state TEXT NOT NULL,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_requested_at INTEGER NOT NULL,
    c_completed_at INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_transfer_id),
    UNIQUE (c_space_id, c_trading_account_id, c_client_ref),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id),
    CHECK (c_direction IN ('TO_SPOT', 'TO_FUTURES')),
    CHECK (c_state IN ('PENDING', 'COMPLETED', 'FAILED')),
    CHECK (c_asset <> ''),
    CHECK (c_amount <> ''),
    CHECK (c_client_ref <> '')
);

CREATE INDEX IF NOT EXISTS idx_trade_wallet_transfers_target
ON t_trade_wallet_transfers (c_space_id, c_logical_account_id, c_target_id, c_direction);

CREATE INDEX IF NOT EXISTS idx_trade_wallet_transfers_account_time
ON t_trade_wallet_transfers (c_space_id, c_trading_account_id, c_requested_at);

CREATE UNIQUE INDEX IF NOT EXISTS uk_trade_wallet_transfers_exchange_id
ON t_trade_wallet_transfers (c_space_id, c_trading_account_id, c_exchange_transfer_id)
WHERE c_exchange_transfer_id <> '';
