
-- ============ MooX Trade 模块 - 账户域（Account）表设计 ============
-- 风格约定（与 admin.sql 一致）：
--   表名 t_xxx / 列名 c_xxx；软删除 c_is_deleted（'false'=有效,'true'=删除）；
--   多空间隔离 c_space_id；时间 c_ctime/c_mtime + 触发器自动更新 mtime；
--   金额/数量等高精度数值统一用 TEXT 存 decimal 字符串，避免浮点精度丢失。

PRAGMA foreign_keys = ON;

-- ============ 资金账户体系 ============

-- ************ 交易账户表（一个用户在某交易通道下的账户）************
-- 说明：account 区分逻辑资金账户，与 t_users(c_user_id) 关联，可绑定到某交易通道（channel）。
CREATE TABLE IF NOT EXISTS t_accounts (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_space_id TEXT NOT NULL DEFAULT '',                       -- 空间ID
    c_account_id TEXT NOT NULL,                                -- 账户唯一标识（UUID/雪花）
    c_user_id TEXT NOT NULL,                                   -- 所属用户UUID（关联 t_users.c_user_id）
    c_account_name TEXT NOT NULL DEFAULT '',                   -- 账户名称（用户可读）
    c_account_type TEXT NOT NULL DEFAULT 'spot',               -- 账户类型: spot=现货, margin=杠杆, swap=合约, sim=模拟
    c_channel_id TEXT NOT NULL DEFAULT '',                     -- 绑定的交易通道ID（关联 t_trade_channels.c_channel_id，可空表示仅记账）
    c_base_currency TEXT NOT NULL DEFAULT 'USDT',              -- 账户计价币种
    c_status INTEGER NOT NULL DEFAULT 1,                       -- 状态: 0=禁用, 1=正常, 2=冻结, 3=只读
    c_is_default INTEGER NOT NULL DEFAULT 0,                   -- 是否用户默认账户: 0=否,1=是
    c_remark TEXT NOT NULL DEFAULT '',                         -- 备注
    c_attributes TEXT NOT NULL DEFAULT '{}',                   -- 扩展属性（JSON）
    c_is_deleted TEXT NOT NULL DEFAULT 'false',                      -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 修改时间
);

CREATE INDEX IF NOT EXISTS idx_accounts_space_id ON t_accounts(c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_space_account_id ON t_accounts(c_space_id, c_account_id);
CREATE INDEX IF NOT EXISTS idx_accounts_user_id ON t_accounts(c_user_id);
CREATE INDEX IF NOT EXISTS idx_accounts_channel_id ON t_accounts(c_channel_id);
CREATE INDEX IF NOT EXISTS idx_accounts_status ON t_accounts(c_status);
CREATE INDEX IF NOT EXISTS idx_accounts_deleted ON t_accounts(c_is_deleted);

-- ************ 账户资产余额表（按账户+币种维度）************
CREATE TABLE IF NOT EXISTS t_account_balances (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_space_id TEXT NOT NULL DEFAULT '',                       -- 空间ID
    c_account_id TEXT NOT NULL,                                -- 账户ID（关联 t_accounts.c_account_id）
    c_currency TEXT NOT NULL,                                  -- 币种/资产代码（USDT/BTC/...）
    c_available TEXT NOT NULL DEFAULT '0',                     -- 可用余额（decimal 字符串）
    c_frozen TEXT NOT NULL DEFAULT '0',                        -- 冻结金额（挂单/出金占用）
    c_total TEXT NOT NULL DEFAULT '0',                         -- 总额 = available + frozen（冗余便于查询）
    c_version INTEGER NOT NULL DEFAULT 0,                       -- 乐观锁版本号（并发扣减用）
    c_is_deleted TEXT NOT NULL DEFAULT 'false',                      -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 修改时间
);

CREATE INDEX IF NOT EXISTS idx_account_balances_space_id ON t_account_balances(c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_balances_account_currency ON t_account_balances(c_account_id, c_currency, c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_account_balances_currency ON t_account_balances(c_currency);

-- ************ 资金流水表（充值/提现/划转/交易结算引起的余额变动）************
-- 说明：流水只追加不修改，是余额的权威账本，余额表是其物化结果。
CREATE TABLE IF NOT EXISTS t_account_fund_flows (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_space_id TEXT NOT NULL DEFAULT '',                       -- 空间ID
    c_flow_id TEXT NOT NULL,                                   -- 流水唯一标识
    c_account_id TEXT NOT NULL,                                -- 账户ID
    c_currency TEXT NOT NULL,                                  -- 币种
    c_biz_type TEXT NOT NULL,                                  -- 业务类型: deposit=充值, withdraw=提现, transfer_in=转入, transfer_out=转出, trade=成交结算, fee=手续费, funding=资金费, adjust=调整
    c_direction INTEGER NOT NULL,                              -- 方向: 1=增加, -1=减少
    c_amount TEXT NOT NULL DEFAULT '0',                        -- 变动金额（绝对值，decimal）
    c_balance_after TEXT NOT NULL DEFAULT '0',                 -- 变动后余额（decimal）
    c_ref_type TEXT NOT NULL DEFAULT '',                       -- 关联单据类型: order, trade, withdraw 等
    c_ref_id TEXT NOT NULL DEFAULT '',                         -- 关联单据ID（订单ID/成交ID等）
    c_remark TEXT NOT NULL DEFAULT '',                         -- 备注
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 发生时间（流水不可变，无 mtime）
);

CREATE INDEX IF NOT EXISTS idx_fund_flows_space_id ON t_account_fund_flows(c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_fund_flows_flow_id ON t_account_fund_flows(c_flow_id);
CREATE INDEX IF NOT EXISTS idx_fund_flows_account ON t_account_fund_flows(c_account_id, c_currency);
CREATE INDEX IF NOT EXISTS idx_fund_flows_biz_type ON t_account_fund_flows(c_biz_type);
CREATE INDEX IF NOT EXISTS idx_fund_flows_ref ON t_account_fund_flows(c_ref_type, c_ref_id);
CREATE INDEX IF NOT EXISTS idx_fund_flows_ctime ON t_account_fund_flows(c_ctime DESC);

-- ************ 账户 API 凭证表（对接交易所的密钥，敏感字段加密存储）************
-- 说明：与交易通道解耦——一个账户可持有多套 API Key（不同权限/不同子账户）。
CREATE TABLE IF NOT EXISTS t_account_api_keys (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_space_id TEXT NOT NULL DEFAULT '',                       -- 空间ID
    c_api_key_id TEXT NOT NULL,                                -- 凭证唯一标识
    c_account_id TEXT NOT NULL,                                -- 所属账户ID
    c_exchange TEXT NOT NULL,                                  -- 交易所: binance/okx/...
    c_api_key TEXT NOT NULL,                                   -- API Key（加密存储）
    c_api_secret TEXT NOT NULL,                                -- API Secret（加密存储）
    c_passphrase TEXT NOT NULL DEFAULT '',                     -- Passphrase（部分交易所需要，加密存储）
    c_permissions TEXT NOT NULL DEFAULT '[]',                  -- 权限范围（JSON数组: ["read","trade","withdraw"]）
    c_status INTEGER NOT NULL DEFAULT 1,                       -- 状态: 0=禁用, 1=可用, 2=已失效
    c_is_deleted TEXT NOT NULL DEFAULT 'false',                      -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 修改时间
);

CREATE INDEX IF NOT EXISTS idx_api_keys_space_id ON t_account_api_keys(c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_api_key_id ON t_account_api_keys(c_api_key_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_account ON t_account_api_keys(c_account_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_exchange ON t_account_api_keys(c_exchange);

-- ============ 触发器：自动更新 mtime ============
DROP TRIGGER IF EXISTS update_accounts_mtime;
CREATE TRIGGER IF NOT EXISTS update_accounts_mtime AFTER UPDATE ON t_accounts BEGIN
    UPDATE t_accounts SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

DROP TRIGGER IF EXISTS update_account_balances_mtime;
CREATE TRIGGER IF NOT EXISTS update_account_balances_mtime AFTER UPDATE ON t_account_balances BEGIN
    UPDATE t_account_balances SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

DROP TRIGGER IF EXISTS update_account_api_keys_mtime;
CREATE TRIGGER IF NOT EXISTS update_account_api_keys_mtime AFTER UPDATE ON t_account_api_keys BEGIN
    UPDATE t_account_api_keys SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;
