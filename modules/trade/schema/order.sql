-- ============ MooX Trade 模块 - 交易域（Order）表设计 ============
-- 风格约定（与 admin.sql / account.sql 一致）：
--   表名 t_xxx / 列名 c_xxx；软删除 c_is_deleted（0=有效,1=删除）；多空间隔离 c_space_id；
--   c_ctime/c_mtime + mtime 触发器；金额/数量用 TEXT 存 decimal 字符串。

PRAGMA foreign_keys = ON;

-- ============ 交易通道（连接交易所/券商的执行通道）============

-- ************ 交易通道表 ************
-- 说明：channel 抽象一条到交易所的下单链路，绑定具体账户与 API 凭证。
CREATE TABLE IF NOT EXISTS t_trade_channels (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_space_id TEXT NOT NULL DEFAULT '',                       -- 空间ID
    c_channel_id TEXT NOT NULL,                                -- 通道唯一标识
    c_channel_name TEXT NOT NULL,                              -- 通道名称
    c_exchange TEXT NOT NULL,                                  -- 交易所: binance/okx/...
    c_market_type TEXT NOT NULL DEFAULT 'spot',                -- 市场类型: spot=现货, margin=杠杆, swap=永续, futures=交割
    c_account_id TEXT NOT NULL DEFAULT '',                     -- 绑定账户ID（关联 t_accounts.c_account_id）
    c_api_key_id TEXT NOT NULL DEFAULT '',                     -- 使用的API凭证ID（关联 t_account_api_keys.c_api_key_id）
    c_endpoint TEXT NOT NULL DEFAULT '',                       -- 接入地址（REST/WS base url，可空走默认）
    c_is_simulated INTEGER NOT NULL DEFAULT 0,                 -- 是否模拟盘: 0=实盘,1=模拟
    c_status INTEGER NOT NULL DEFAULT 1,                       -- 状态: 0=禁用, 1=在线, 2=离线, 3=异常
    c_rate_limit INTEGER NOT NULL DEFAULT 0,                   -- 下单限速（次/秒，0=不限）
    c_last_heartbeat DATETIME,                                 -- 最后心跳时间
    c_config TEXT NOT NULL DEFAULT '{}',                       -- 通道额外配置（JSON）
    c_is_deleted INTEGER NOT NULL DEFAULT 0,                    -- 删除标记: 0=有效,1=删除
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 修改时间
);

CREATE INDEX IF NOT EXISTS idx_trade_channels_space_id ON t_trade_channels (c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_trade_channels_space_channel_id ON t_trade_channels (c_space_id, c_channel_id);
CREATE INDEX IF NOT EXISTS idx_trade_channels_exchange ON t_trade_channels (c_exchange);
CREATE INDEX IF NOT EXISTS idx_trade_channels_account ON t_trade_channels (c_account_id);
CREATE INDEX IF NOT EXISTS idx_trade_channels_status ON t_trade_channels (c_status);
CREATE INDEX IF NOT EXISTS idx_trade_channels_deleted ON t_trade_channels (c_is_deleted);

-- ============ 操作审计 ============

-- ************ 账户交易操作日志表（下单/撤单/改单等操作审计）************
-- 说明：记录对交易通道发起的每一次操作请求与结果，用于审计与排障。
CREATE TABLE IF NOT EXISTS t_order_operations (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_space_id TEXT NOT NULL DEFAULT '',                       -- 空间ID
    c_op_id TEXT NOT NULL,                                     -- 操作唯一标识
    c_account_id TEXT NOT NULL,                                -- 账户ID
    c_channel_id TEXT NOT NULL DEFAULT '',                     -- 交易通道ID
    c_order_id TEXT NOT NULL DEFAULT '',                       -- 关联订单ID（可空，如查询类操作）
    c_op_type TEXT NOT NULL,                                   -- 操作类型: place=下单, cancel=撤单, amend=改单, cancel_all=全撤, query=查询, set_leverage=调杠杆
    c_request TEXT NOT NULL DEFAULT '{}',                      -- 请求参数（JSON）
    c_response TEXT NOT NULL DEFAULT '{}',                     -- 通道返回（JSON）
    c_op_status INTEGER NOT NULL DEFAULT 0,                    -- 操作结果: 0=处理中, 1=成功, 2=失败
    c_error_code TEXT NOT NULL DEFAULT '',                     -- 错误码（交易所/系统）
    c_error_message TEXT NOT NULL DEFAULT '',                  -- 错误信息
    c_latency_ms INTEGER NOT NULL DEFAULT 0,                   -- 通道往返耗时（毫秒）
    c_operator TEXT NOT NULL DEFAULT '',                       -- 操作发起者（用户ID/策略ID/system）
    c_client_ip TEXT NOT NULL DEFAULT '',                      -- 客户端IP
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 操作时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 修改时间（结果回填时更新）
);

CREATE INDEX IF NOT EXISTS idx_order_ops_space_id ON t_order_operations (c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_order_ops_op_id ON t_order_operations (c_op_id);
CREATE INDEX IF NOT EXISTS idx_order_ops_account ON t_order_operations (c_account_id);
CREATE INDEX IF NOT EXISTS idx_order_ops_order_id ON t_order_operations (c_order_id);
CREATE INDEX IF NOT EXISTS idx_order_ops_type ON t_order_operations (c_op_type);
CREATE INDEX IF NOT EXISTS idx_order_ops_status ON t_order_operations (c_op_status);
CREATE INDEX IF NOT EXISTS idx_order_ops_ctime ON t_order_operations (c_ctime DESC);

-- ============ 触发器：自动更新 mtime ============
DROP TRIGGER IF EXISTS update_trade_channels_mtime;
CREATE TRIGGER IF NOT EXISTS update_trade_channels_mtime
AFTER UPDATE ON t_trade_channels
BEGIN
    UPDATE t_trade_channels SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;

DROP TRIGGER IF EXISTS update_order_operations_mtime;
CREATE TRIGGER IF NOT EXISTS update_order_operations_mtime
AFTER UPDATE ON t_order_operations
BEGIN
    UPDATE t_order_operations SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;
