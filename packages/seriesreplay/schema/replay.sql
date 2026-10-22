-- 回放任务：规格、绑定的策略与因子版本、状态与进度。
CREATE TABLE IF NOT EXISTS t_replay_runs (
    c_replay_id TEXT NOT NULL,
    c_spec_json TEXT NOT NULL,
    c_binding_json TEXT NOT NULL DEFAULT '{}',
    -- running 运行中；paused 已暂停；cancelled 已取消；completed 已完成；failed 失败（可续跑）
    c_status TEXT NOT NULL DEFAULT 'running',
    c_error TEXT NOT NULL DEFAULT '',
    c_periods_total INTEGER NOT NULL DEFAULT 0,
    c_periods_done INTEGER NOT NULL DEFAULT 0,
    -- 正在处理的周期（K 线 data_time，UTC 秒），0 表示还没有开始
    c_current_period INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('running', 'paused', 'cancelled', 'completed', 'failed')),
    PRIMARY KEY (c_replay_id)
);

-- 周期记录：按 (回放, 周期) 保存本期决策、下一期策略状态、待发送的 ReplayAdvance 与各阶段完成标记。
-- 同一期重复执行返回同一条记录，不重新求值；重启后按标记补齐缺的阶段。
CREATE TABLE IF NOT EXISTS t_replay_period_records (
    c_replay_id TEXT NOT NULL,
    -- K 线 data_time（UTC 秒）
    c_period INTEGER NOT NULL,
    c_decision_json TEXT NOT NULL,
    c_state_json TEXT NOT NULL,
    c_advance_json TEXT NOT NULL,
    c_trade_acked INTEGER NOT NULL DEFAULT 0,
    c_factor_written INTEGER NOT NULL DEFAULT 0,
    c_complete INTEGER NOT NULL DEFAULT 0,
    c_factor_ms INTEGER NOT NULL DEFAULT 0,
    c_strategy_ms INTEGER NOT NULL DEFAULT 0,
    c_trade_ms INTEGER NOT NULL DEFAULT 0,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_replay_id, c_period),
    FOREIGN KEY (c_replay_id)
        REFERENCES t_replay_runs (c_replay_id)
        ON DELETE CASCADE
);
