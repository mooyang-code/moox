PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_trading_accounts (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_exchange TEXT NOT NULL,
    -- 已开通的市场：逗号分隔的有序集合，加密货币取 SPOT、SWAP 的子集，股票只有 STOCK
    c_markets TEXT NOT NULL,
    c_execution_mode TEXT NOT NULL,
    c_live_environment TEXT NOT NULL DEFAULT '',
    c_credential_secret_id TEXT NOT NULL,
    c_settlement_asset TEXT NOT NULL,
    c_margin_mode TEXT NOT NULL DEFAULT '',
    -- 账户类型，只由交易所检测写入；模拟盘和股票账户为空串
    c_account_mode TEXT NOT NULL DEFAULT '',
    -- 最近一次检测的结果：权限、OKX acctLv、持仓模式、检测时间、阻断项
    c_inspection_json TEXT NOT NULL DEFAULT '{}',
    c_status TEXT NOT NULL,
    c_ready INTEGER NOT NULL DEFAULT 0,
    c_leverage_settings_json TEXT NOT NULL DEFAULT '{}',
    c_fill_cursors_json TEXT NOT NULL DEFAULT '{}',
    c_snapshot_json TEXT NOT NULL DEFAULT '{}',
    c_snapshot_source_time INTEGER NOT NULL DEFAULT 0,
    -- 各钱包待余额确认的水位（钱包 -> 交易所毫秒时间）：私有成交/余额事件抬高对应钱包的水位，只有覆盖该水位的完整余额快照才能清掉并恢复准入
    c_balance_pending_json TEXT NOT NULL DEFAULT '{}',
    -- 上次小额资产兑换 BNB 的时间（毫秒）；单独存，不放进检测结果
    c_last_dust_convert_at INTEGER NOT NULL DEFAULT 0,
    c_last_sync_at INTEGER NOT NULL DEFAULT 0,
    c_last_ready_at INTEGER NOT NULL DEFAULT 0,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_trading_account_id),
    UNIQUE (c_space_id, c_name),
    CHECK (json_valid(c_leverage_settings_json)),
    CHECK (json_type(c_leverage_settings_json) = 'object'),
    CHECK (json_valid(c_fill_cursors_json)),
    CHECK (json_type(c_fill_cursors_json) = 'object'),
    CHECK (json_valid(c_snapshot_json)),
    CHECK (json_type(c_snapshot_json) = 'object'),
    CHECK (json_valid(c_balance_pending_json)),
    CHECK (json_type(c_balance_pending_json) = 'object'),
    CHECK (json_valid(c_inspection_json)),
    CHECK (json_type(c_inspection_json) = 'object'),
    CHECK (c_markets IN ('SPOT', 'SWAP', 'SPOT,SWAP', 'STOCK')),
    CHECK (c_account_mode IN ('', 'BINANCE_PORTFOLIO_MARGIN', 'BINANCE_STANDARD', 'OKX_UNIFIED')),
    CHECK (c_account_mode = '' OR (c_execution_mode = 'LIVE' AND c_markets <> 'STOCK')),
    -- 测试网一个账户只开一个市场
    CHECK (c_live_environment <> 'TESTNET' OR c_markets <> 'SPOT,SWAP'),
    CHECK (c_execution_mode IN ('PAPER', 'LIVE')),
    CHECK (
        (c_execution_mode = 'PAPER'
            AND c_live_environment = ''
            AND c_credential_secret_id = '')
        OR
        (
            c_execution_mode = 'LIVE'
            AND c_live_environment IN ('TESTNET', 'PRODUCTION')
            AND c_credential_secret_id <> ''
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_trading_accounts_status
ON t_trading_accounts (c_space_id, c_status);

-- 一把密钥（交易所、环境、密钥 ID）全局只能有一个启用的实盘执行账户
CREATE UNIQUE INDEX IF NOT EXISTS uk_trading_accounts_credential
ON t_trading_accounts (c_exchange, c_live_environment, c_credential_secret_id)
WHERE c_execution_mode = 'LIVE' AND c_status = 'ENABLED';

CREATE UNIQUE INDEX IF NOT EXISTS uk_trading_accounts_id
ON t_trading_accounts (c_trading_account_id);
