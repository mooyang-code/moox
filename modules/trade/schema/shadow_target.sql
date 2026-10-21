-- 影子目标：预热中的策略实例发来的目标，Trade 只完成权重到数量的换算并记录，
-- 不接受为正式目标，不预留资金，不下单。
CREATE TABLE IF NOT EXISTS t_shadow_targets (
    c_space_id TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_instance_id TEXT NOT NULL,
    c_session_id TEXT NOT NULL,
    c_strategy_id TEXT NOT NULL,
    c_bar_end_time INTEGER NOT NULL,
    c_valid_until INTEGER NOT NULL,
    c_weights_json TEXT NOT NULL,
    c_equity TEXT NOT NULL,
    c_equity_source_time INTEGER NOT NULL,
    c_reference_prices_json TEXT NOT NULL,
    c_quantity_targets_json TEXT NOT NULL,
    c_recorded_at INTEGER NOT NULL,
    PRIMARY KEY (c_space_id, c_target_id),
    CHECK (c_bar_end_time > 0),
    CHECK (json_valid(c_weights_json)),
    CHECK (json_type(c_weights_json) = 'array'),
    CHECK (json_valid(c_reference_prices_json)),
    CHECK (json_type(c_reference_prices_json) = 'object'),
    CHECK (json_valid(c_quantity_targets_json)),
    CHECK (json_type(c_quantity_targets_json) = 'array')
);

CREATE INDEX IF NOT EXISTS idx_shadow_targets_instance
ON t_shadow_targets (c_space_id, c_instance_id, c_bar_end_time);
