-- 目标历史：收到的每个目标一条，按目标号幂等。正式目标（c_shadow = 0）是被接受的目标与它当时的换算快照；
-- 影子目标（c_shadow = 1）是试运行中的策略实例发来的目标，Trade 只完成权重到数量的换算并记录，不接受、不预留资金、不下单。
-- 读正式目标的地方（重投查重、换手检查）都要带 c_shadow = 0。
CREATE TABLE IF NOT EXISTS t_logical_account_target_history (
    c_space_id TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_shadow INTEGER NOT NULL DEFAULT 0,
    c_instance_id TEXT NOT NULL,
    c_session_id TEXT NOT NULL,
    -- 目标所属会话的开始时刻（毫秒）
    c_session_started_at INTEGER NOT NULL,
    c_strategy_id TEXT NOT NULL,
    c_bar_end_time INTEGER NOT NULL,
    c_valid_until INTEGER NOT NULL,
    c_request_hash TEXT NOT NULL,
    c_signal_time INTEGER NOT NULL,
    c_weights_json TEXT NOT NULL,
    c_equity TEXT NOT NULL,
    c_equity_source_time INTEGER NOT NULL,
    c_reference_prices_json TEXT NOT NULL,
    c_quantity_targets_json TEXT NOT NULL,
    -- 正式目标是接受时刻，影子目标是记录时刻（毫秒）
    c_recorded_at INTEGER NOT NULL,
    CHECK (c_shadow IN (0, 1)),
    CHECK (
        c_instance_id <> ''
        AND c_session_id <> ''
        AND c_strategy_id <> ''
        AND c_session_started_at > 0
        AND c_bar_end_time > 0
        AND c_valid_until > c_bar_end_time
    ),
    CHECK (json_valid(c_weights_json)),
    CHECK (json_type(c_weights_json) = 'array'),
    CHECK (json_valid(c_reference_prices_json)),
    CHECK (json_type(c_reference_prices_json) = 'object'),
    CHECK (json_valid(c_quantity_targets_json)),
    CHECK (json_type(c_quantity_targets_json) = 'array'),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
        ON DELETE CASCADE,
    PRIMARY KEY (c_space_id, c_target_id)
);

-- 换手检查按接受时间读组合的正式目标。
CREATE INDEX IF NOT EXISTS idx_target_history_logical
ON t_logical_account_target_history (c_space_id, c_logical_account_id, c_recorded_at)
WHERE c_shadow = 0;
-- 同一会话同一根 K 线只接受一个正式目标。
CREATE UNIQUE INDEX IF NOT EXISTS ux_target_history_session_bar
ON t_logical_account_target_history (
    c_space_id,
    c_logical_account_id,
    c_instance_id,
    c_session_id,
    c_bar_end_time
)
WHERE c_shadow = 0;
-- 管理台按实例看最近的影子目标。
CREATE INDEX IF NOT EXISTS idx_target_history_shadow_instance
ON t_logical_account_target_history (c_space_id, c_instance_id, c_bar_end_time)
WHERE c_shadow = 1;
-- 目标历史清理按过期时刻扫描删除。
CREATE INDEX IF NOT EXISTS idx_target_history_valid_until
ON t_logical_account_target_history (c_valid_until);
