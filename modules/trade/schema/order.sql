
-- ============ MooX Trade 模块 - 交易域（Order）表设计 ============
-- 风格约定（与 admin.sql / account.sql 一致）：
--   表名 t_xxx / 列名 c_xxx；软删除 c_is_deleted（'false'=有效,'true'=删除）；多空间隔离 c_space_id；
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
    c_is_deleted TEXT NOT NULL DEFAULT 'false',                      -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 修改时间
);

CREATE INDEX IF NOT EXISTS idx_trade_channels_space_id ON t_trade_channels(c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_trade_channels_space_channel_id ON t_trade_channels(c_space_id, c_channel_id);
CREATE INDEX IF NOT EXISTS idx_trade_channels_exchange ON t_trade_channels(c_exchange);
CREATE INDEX IF NOT EXISTS idx_trade_channels_account ON t_trade_channels(c_account_id);
CREATE INDEX IF NOT EXISTS idx_trade_channels_status ON t_trade_channels(c_status);
CREATE INDEX IF NOT EXISTS idx_trade_channels_deleted ON t_trade_channels(c_is_deleted);

-- ============ 订单 ============

-- ************ 订单表 ************
-- 说明：记录一次下单的完整生命周期。client_order_id 为业务幂等键；
--      exchange_order_id 为交易所返回的真实订单号。
CREATE TABLE IF NOT EXISTS t_orders (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_space_id TEXT NOT NULL DEFAULT '',                       -- 空间ID
    c_order_id TEXT NOT NULL,                                  -- 系统内订单唯一标识
    c_client_order_id TEXT NOT NULL DEFAULT '',                -- 客户端自定义订单ID（幂等键）
    c_exchange_order_id TEXT NOT NULL DEFAULT '',              -- 交易所订单ID（下单成功后回填）
    c_account_id TEXT NOT NULL,                                -- 账户ID
    c_channel_id TEXT NOT NULL,                                -- 交易通道ID
    c_exchange TEXT NOT NULL,                                  -- 交易所
    c_symbol TEXT NOT NULL,                                    -- 交易对（如 BTC-USDT）
    c_market_type TEXT NOT NULL DEFAULT 'spot',                -- 市场类型: spot/margin/swap/futures
    c_side TEXT NOT NULL,                                      -- 方向: buy=买入, sell=卖出
    c_pos_side TEXT NOT NULL DEFAULT '',                       -- 持仓方向（合约）: long/short/net
    c_order_type TEXT NOT NULL,                                -- 订单类型: limit=限价, market=市价, stop=止损, stop_limit, post_only, ioc, fok
    c_time_in_force TEXT NOT NULL DEFAULT 'GTC',               -- 有效方式: GTC/IOC/FOK
    c_price TEXT NOT NULL DEFAULT '0',                         -- 委托价格（市价单为0）
    c_quantity TEXT NOT NULL DEFAULT '0',                      -- 委托数量
    c_amount TEXT NOT NULL DEFAULT '0',                        -- 委托金额（市价买单按金额下单时使用）
    c_filled_qty TEXT NOT NULL DEFAULT '0',                    -- 已成交数量
    c_filled_amount TEXT NOT NULL DEFAULT '0',                 -- 已成交金额
    c_avg_price TEXT NOT NULL DEFAULT '0',                     -- 平均成交价
    c_fee TEXT NOT NULL DEFAULT '0',                           -- 累计手续费
    c_fee_currency TEXT NOT NULL DEFAULT '',                   -- 手续费币种
    c_status INTEGER NOT NULL DEFAULT 0,                       -- 状态: 0=待提交, 1=已提交, 2=部分成交, 3=完全成交, 4=已撤销, 5=部分成交后撤销, 6=拒绝, 7=过期
    c_reduce_only INTEGER NOT NULL DEFAULT 0,                  -- 只减仓标记（合约）: 0/1
    c_trigger_price TEXT NOT NULL DEFAULT '0',                 -- 触发价（条件单）
    c_source TEXT NOT NULL DEFAULT 'manual',                   -- 来源: manual=手动, strategy=策略, api=接口, factor=因子信号
    c_strategy_id TEXT NOT NULL DEFAULT '',                    -- 关联策略ID（可空）
    c_reject_reason TEXT NOT NULL DEFAULT '',                  -- 拒绝/失败原因
    c_submitted_at DATETIME,                                   -- 提交到交易所时间
    c_finished_at DATETIME,                                    -- 终态时间（成交完/撤销）
    c_extra TEXT NOT NULL DEFAULT '{}',                        -- 扩展字段（JSON）
    c_is_deleted TEXT NOT NULL DEFAULT 'false',                      -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 修改时间
);

CREATE INDEX IF NOT EXISTS idx_orders_space_id ON t_orders(c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_space_order_id ON t_orders(c_space_id, c_order_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_client_order_id ON t_orders(c_space_id, c_client_order_id, c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_orders_exchange_order_id ON t_orders(c_exchange, c_exchange_order_id);
CREATE INDEX IF NOT EXISTS idx_orders_account ON t_orders(c_account_id);
CREATE INDEX IF NOT EXISTS idx_orders_channel ON t_orders(c_channel_id);
CREATE INDEX IF NOT EXISTS idx_orders_symbol ON t_orders(c_symbol);
CREATE INDEX IF NOT EXISTS idx_orders_status ON t_orders(c_status);
CREATE INDEX IF NOT EXISTS idx_orders_strategy ON t_orders(c_strategy_id);
CREATE INDEX IF NOT EXISTS idx_orders_ctime ON t_orders(c_ctime DESC);

-- ************ 成交明细表（一笔订单可对应多笔成交/fill）************
CREATE TABLE IF NOT EXISTS t_trades (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_space_id TEXT NOT NULL DEFAULT '',                       -- 空间ID
    c_trade_id TEXT NOT NULL,                                  -- 系统内成交唯一标识
    c_exchange_trade_id TEXT NOT NULL DEFAULT '',              -- 交易所成交ID
    c_order_id TEXT NOT NULL,                                  -- 关联系统订单ID
    c_exchange_order_id TEXT NOT NULL DEFAULT '',              -- 关联交易所订单ID
    c_account_id TEXT NOT NULL,                                -- 账户ID
    c_channel_id TEXT NOT NULL DEFAULT '',                     -- 交易通道ID
    c_exchange TEXT NOT NULL,                                  -- 交易所
    c_symbol TEXT NOT NULL,                                    -- 交易对
    c_side TEXT NOT NULL,                                      -- 方向: buy/sell
    c_price TEXT NOT NULL DEFAULT '0',                         -- 成交价
    c_quantity TEXT NOT NULL DEFAULT '0',                      -- 成交数量
    c_amount TEXT NOT NULL DEFAULT '0',                        -- 成交金额 = price * quantity
    c_fee TEXT NOT NULL DEFAULT '0',                           -- 手续费
    c_fee_currency TEXT NOT NULL DEFAULT '',                   -- 手续费币种
    c_role TEXT NOT NULL DEFAULT '',                           -- 角色: maker/taker
    c_traded_at DATETIME,                                      -- 成交时间（交易所时间）
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 入库时间（成交不可变，无 mtime）
);

CREATE INDEX IF NOT EXISTS idx_trades_space_id ON t_trades(c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_trades_trade_id ON t_trades(c_space_id, c_trade_id);
CREATE INDEX IF NOT EXISTS idx_trades_exchange_trade_id ON t_trades(c_exchange, c_exchange_trade_id);
CREATE INDEX IF NOT EXISTS idx_trades_order_id ON t_trades(c_order_id);
CREATE INDEX IF NOT EXISTS idx_trades_account ON t_trades(c_account_id);
CREATE INDEX IF NOT EXISTS idx_trades_symbol ON t_trades(c_symbol);
CREATE INDEX IF NOT EXISTS idx_trades_traded_at ON t_trades(c_traded_at DESC);

-- ************ 持仓表（合约/杠杆账户当前持仓快照）************
CREATE TABLE IF NOT EXISTS t_positions (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_space_id TEXT NOT NULL DEFAULT '',                       -- 空间ID
    c_position_id TEXT NOT NULL,                               -- 持仓唯一标识
    c_account_id TEXT NOT NULL,                                -- 账户ID
    c_channel_id TEXT NOT NULL DEFAULT '',                     -- 交易通道ID
    c_exchange TEXT NOT NULL,                                  -- 交易所
    c_symbol TEXT NOT NULL,                                    -- 交易对
    c_pos_side TEXT NOT NULL DEFAULT 'net',                    -- 持仓方向: long/short/net
    c_quantity TEXT NOT NULL DEFAULT '0',                      -- 持仓数量
    c_avg_price TEXT NOT NULL DEFAULT '0',                     -- 持仓均价
    c_leverage TEXT NOT NULL DEFAULT '1',                      -- 杠杆倍数
    c_margin TEXT NOT NULL DEFAULT '0',                        -- 占用保证金
    c_liq_price TEXT NOT NULL DEFAULT '0',                     -- 预估强平价
    c_unrealized_pnl TEXT NOT NULL DEFAULT '0',                -- 未实现盈亏
    c_realized_pnl TEXT NOT NULL DEFAULT '0',                  -- 已实现盈亏
    c_is_deleted TEXT NOT NULL DEFAULT 'false',                      -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 修改时间
);

CREATE INDEX IF NOT EXISTS idx_positions_space_id ON t_positions(c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_positions_account_symbol_side ON t_positions(c_account_id, c_symbol, c_pos_side, c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_positions_channel ON t_positions(c_channel_id);
CREATE INDEX IF NOT EXISTS idx_positions_symbol ON t_positions(c_symbol);

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

CREATE INDEX IF NOT EXISTS idx_order_ops_space_id ON t_order_operations(c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_order_ops_op_id ON t_order_operations(c_op_id);
CREATE INDEX IF NOT EXISTS idx_order_ops_account ON t_order_operations(c_account_id);
CREATE INDEX IF NOT EXISTS idx_order_ops_order_id ON t_order_operations(c_order_id);
CREATE INDEX IF NOT EXISTS idx_order_ops_type ON t_order_operations(c_op_type);
CREATE INDEX IF NOT EXISTS idx_order_ops_status ON t_order_operations(c_op_status);
CREATE INDEX IF NOT EXISTS idx_order_ops_ctime ON t_order_operations(c_ctime DESC);

-- ============ 触发器：自动更新 mtime ============
DROP TRIGGER IF EXISTS update_trade_channels_mtime;
CREATE TRIGGER IF NOT EXISTS update_trade_channels_mtime AFTER UPDATE ON t_trade_channels BEGIN
    UPDATE t_trade_channels SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

DROP TRIGGER IF EXISTS update_orders_mtime;
CREATE TRIGGER IF NOT EXISTS update_orders_mtime AFTER UPDATE ON t_orders BEGIN
    UPDATE t_orders SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

DROP TRIGGER IF EXISTS update_positions_mtime;
CREATE TRIGGER IF NOT EXISTS update_positions_mtime AFTER UPDATE ON t_positions BEGIN
    UPDATE t_positions SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

DROP TRIGGER IF EXISTS update_order_operations_mtime;
CREATE TRIGGER IF NOT EXISTS update_order_operations_mtime AFTER UPDATE ON t_order_operations BEGIN
    UPDATE t_order_operations SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;
