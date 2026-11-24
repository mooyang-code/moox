-- ************ 禁止名单 ************
-- 全局的，不分空间。范围 ALL 对所有账户生效；PAPER_ONLY 只对模拟盘账户生效（演练用）。名单里的标的只许减仓、不许加仓。
CREATE TABLE IF NOT EXISTS t_trade_blocked_instruments (
    c_exchange TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_exchange_symbol TEXT NOT NULL,
    c_scope TEXT NOT NULL,
    -- 内部标的 ID 和基础资产只用于展示和审计，匹配一律在使用时查标的表；写入时查不到则为空串
    c_instrument_id TEXT NOT NULL DEFAULT '',
    c_base_asset TEXT NOT NULL DEFAULT '',
    c_reason TEXT NOT NULL,
    -- 写入或把范围升级为 ALL 的那条下架记录
    c_delist_id TEXT NOT NULL,
    c_blocked_at INTEGER NOT NULL,
    CHECK (c_exchange <> ''),
    CHECK (c_market_type IN ('SPOT', 'SWAP')),
    CHECK (c_exchange_symbol <> ''),
    CHECK (c_scope IN ('ALL', 'PAPER_ONLY')),
    CHECK (c_reason <> ''),
    PRIMARY KEY (c_exchange, c_market_type, c_exchange_symbol)
);

-- ************ 下架记录 ************
-- 一条记录对应一个（标的、范围、下架时间）。改期时旧记录记为 RESCHEDULED 并结束，另建一条新记录。
-- c_planned_round：已规划到第几轮。0 表示还没到 T−24h；1 表示已按 T−24h 规划；2 表示已按 T−2h 规划。
CREATE TABLE IF NOT EXISTS t_trade_delists (
    c_delist_id TEXT NOT NULL PRIMARY KEY,
    c_exchange TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_exchange_symbol TEXT NOT NULL,
    c_scope TEXT NOT NULL,
    c_delist_at INTEGER NOT NULL,
    c_source TEXT NOT NULL,
    c_source_event_id TEXT NOT NULL,
    c_event_occurred_at INTEGER NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'ACTIVE',
    c_planned_round INTEGER NOT NULL DEFAULT 0,
    -- 最近一次规划或核对的说明：没能判断的账户、零头、不算持有的说明
    c_note TEXT NOT NULL DEFAULT '',
    -- 结束说明：核对结论、取消原因、改期后由哪条记录接着处理
    c_result TEXT NOT NULL DEFAULT '',
    c_created_at INTEGER NOT NULL,
    c_updated_at INTEGER NOT NULL,
    c_finished_at INTEGER NOT NULL DEFAULT 0,
    CHECK (c_market_type IN ('SPOT', 'SWAP')),
    CHECK (c_scope IN ('ALL', 'PAPER_ONLY')),
    CHECK (c_status IN ('ACTIVE', 'COMPLETED', 'FAILED', 'CANCELED', 'RESCHEDULED')),
    CHECK (c_planned_round IN (0, 1, 2)),
    UNIQUE (c_source_event_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_t_trade_delists_active
ON t_trade_delists (c_exchange, c_market_type, c_exchange_symbol, c_scope)
WHERE c_status = 'ACTIVE';
CREATE INDEX IF NOT EXISTS idx_t_trade_delists_status ON t_trade_delists (c_status, c_finished_at);

-- ************ 下架清仓的各轮各组合 ************
-- 规划一轮时，给每个持有的（空间、组合账户）建一行，先写下确定生成的操作号，再发起。动作状态不在这里存，从 t_operator_actions 读。
CREATE TABLE IF NOT EXISTS t_trade_delist_flattens (
    c_delist_id TEXT NOT NULL,
    c_round INTEGER NOT NULL,
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_action_id TEXT NOT NULL,
    c_created_at INTEGER NOT NULL,
    CHECK (c_round IN (1, 2)),
    PRIMARY KEY (c_delist_id, c_round, c_space_id, c_logical_account_id),
    FOREIGN KEY (c_delist_id) REFERENCES t_trade_delists (c_delist_id) ON DELETE CASCADE
);
