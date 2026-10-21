-- 逐期回放的纸面账本：每次回放一个会话，独立于实盘账户、订单与预留；
-- 回放绝不产生实际下单，结果保留到被清理为止。
CREATE TABLE IF NOT EXISTS t_replay_sessions (
    c_replay_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'open',
    c_quote_asset TEXT NOT NULL DEFAULT 'USDT',
    c_initial_cash TEXT NOT NULL,
    c_fee_rate TEXT NOT NULL,
    c_slippage_bps TEXT NOT NULL,
    c_cash TEXT NOT NULL,
    -- 持仓：{标的: 数量}
    c_positions_json TEXT NOT NULL DEFAULT '{}',
    -- 已推进到的决策时刻（毫秒），0 表示还没有推进
    c_last_period INTEGER NOT NULL DEFAULT 0,
    c_created_at INTEGER NOT NULL,
    c_updated_at INTEGER NOT NULL,
    c_closed_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (c_replay_id),
    CHECK (c_status IN ('open', 'closed')),
    CHECK (json_valid(c_positions_json)),
    CHECK (json_type(c_positions_json) = 'object')
);

CREATE TABLE IF NOT EXISTS t_replay_periods (
    c_replay_id TEXT NOT NULL,
    -- 决策时刻（毫秒）：K 线起点 + 周期
    c_period INTEGER NOT NULL,
    c_kind TEXT NOT NULL,
    c_request_hash TEXT NOT NULL,
    c_equity_before TEXT NOT NULL,
    c_equity TEXT NOT NULL,
    c_cash TEXT NOT NULL,
    c_positions_json TEXT NOT NULL,
    c_fees TEXT NOT NULL,
    c_turnover TEXT NOT NULL,
    c_result_json TEXT NOT NULL,
    c_recorded_at INTEGER NOT NULL,
    PRIMARY KEY (c_replay_id, c_period),
    FOREIGN KEY (c_replay_id)
        REFERENCES t_replay_sessions (c_replay_id)
        ON DELETE CASCADE,
    CHECK (c_period > 0),
    CHECK (c_kind IN ('UPDATE_TARGET', 'SKIP')),
    CHECK (json_valid(c_positions_json)),
    CHECK (json_type(c_positions_json) = 'object'),
    CHECK (json_valid(c_result_json))
);

CREATE TABLE IF NOT EXISTS t_replay_orders (
    c_replay_id TEXT NOT NULL,
    c_order_id TEXT NOT NULL,
    c_period INTEGER NOT NULL,
    c_instrument_id TEXT NOT NULL,
    c_side TEXT NOT NULL,
    c_requested_quantity TEXT NOT NULL,
    c_filled_quantity TEXT NOT NULL,
    c_price TEXT NOT NULL,
    c_fee TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (c_replay_id, c_order_id),
    FOREIGN KEY (c_replay_id)
        REFERENCES t_replay_sessions (c_replay_id)
        ON DELETE CASCADE,
    CHECK (c_side IN ('BUY', 'SELL')),
    CHECK (c_status IN ('FILLED', 'PARTIAL', 'REJECTED'))
);

CREATE INDEX IF NOT EXISTS idx_replay_orders_period
ON t_replay_orders (c_replay_id, c_period);
