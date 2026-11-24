CREATE TABLE IF NOT EXISTS t_logical_accounts (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    -- 关闭到哪个会话为止（会话的开始时刻，毫秒）：开始时刻不大于它的会话的目标一律不再接受
    c_closed_through INTEGER NOT NULL DEFAULT 0,
    c_execution_mode TEXT NOT NULL,
    c_settlement_asset TEXT NOT NULL,
    -- 下单设置与资金设置：空对象表示全部取默认值
    c_order_settings_json TEXT NOT NULL DEFAULT '{}',
    c_fund_settings_json TEXT NOT NULL DEFAULT '{}',
    c_automation_state TEXT NOT NULL DEFAULT 'PAUSED',
    -- 暂停原因只用于展示；程序按暂停来源与减仓状态判断
    c_pause_reason TEXT NOT NULL DEFAULT '',
    -- 暂停来源：USER 人工（含新建时的初始暂停）、SYSTEM 系统自动、MARGIN 保证金率风控；运行中为空
    c_pause_source TEXT NOT NULL DEFAULT '',
    -- 保证金率风控暂停的减仓状态：REDUCED 已发起减仓、PENDING 减仓待执行、UNREDUCED 未减仓已回安全线、FAILED 减仓失败、PARTIAL 只减了一部分；其他情况为空
    c_margin_reduce_state TEXT NOT NULL DEFAULT '',
    -- 最近一次从暂停恢复自动执行的时间（毫秒）：保证金率风控据此判断“上次风控减仓之后组合是否恢复过”，不依赖进程内存
    c_resumed_at INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_control_mode TEXT NOT NULL DEFAULT 'STRATEGY',
    PRIMARY KEY (c_space_id, c_logical_account_id),
    UNIQUE (c_space_id, c_name),
    CHECK (c_execution_mode IN ('PAPER', 'LIVE')),
    CHECK (c_control_mode IN ('STRATEGY', 'MANUAL')),
    CHECK (json_valid(c_order_settings_json)),
    CHECK (json_type(c_order_settings_json) = 'object'),
    CHECK (json_valid(c_fund_settings_json)),
    CHECK (json_type(c_fund_settings_json) = 'object'),
    CHECK (c_automation_state IN ('ACTIVE', 'PAUSED')),
    CHECK (c_pause_source IN ('', 'USER', 'SYSTEM', 'MARGIN')),
    CHECK (c_margin_reduce_state IN ('', 'REDUCED', 'PENDING', 'UNREDUCED', 'FAILED', 'PARTIAL')),
    CHECK (
        (c_automation_state = 'ACTIVE' AND c_pause_reason = '' AND c_pause_source = '')
        OR
        (c_automation_state = 'PAUSED' AND c_pause_reason <> '' AND c_pause_source <> '')
    ),
    CHECK ((c_pause_source = 'MARGIN') = (c_margin_reduce_state <> ''))
);

CREATE TABLE IF NOT EXISTS t_logical_account_members (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_enabled INTEGER NOT NULL DEFAULT 1,
    c_priority INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (
        c_space_id,
        c_logical_account_id,
        c_trading_account_id
    ),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
        ON DELETE CASCADE,
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id),
    CHECK (c_enabled IN (0, 1))
);

-- 一个执行账户最多属于一个组合账户（启用和停用成员都算）
CREATE UNIQUE INDEX IF NOT EXISTS ux_logical_account_members_trading_account
ON t_logical_account_members (c_space_id, c_trading_account_id);

CREATE TABLE IF NOT EXISTS t_logical_account_targets (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_instance_id TEXT NOT NULL,
    c_session_id TEXT NOT NULL,
    -- 目标所属会话的开始时刻（毫秒）：比较会话先后用
    c_session_started_at INTEGER NOT NULL,
    c_strategy_id TEXT NOT NULL,
    c_bar_end_time INTEGER NOT NULL,
    c_valid_until INTEGER NOT NULL,
    c_targets_json TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_blocked_targets_json TEXT NOT NULL DEFAULT '[]',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_accepted_at INTEGER NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_logical_account_id),
    UNIQUE (c_space_id, c_target_id),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
        ON DELETE CASCADE,
    CHECK (c_instance_id <> '' AND c_session_id <> '' AND
           c_session_started_at > 0 AND
           c_strategy_id <> '' AND c_bar_end_time > 0 AND
           c_valid_until > c_bar_end_time),
    CHECK (c_status IN ('PENDING', 'CONVERGING', 'CONVERGED', 'BLOCKED', 'EXPIRED')),
    CHECK (json_valid(c_targets_json)),
    CHECK (json_type(c_targets_json) = 'array'),
    CHECK (json_valid(c_blocked_targets_json)),
    CHECK (json_type(c_blocked_targets_json) = 'array')
);

CREATE INDEX IF NOT EXISTS idx_logical_account_targets_status
ON t_logical_account_targets (c_space_id, c_status, c_mtime);

CREATE TABLE IF NOT EXISTS t_operator_actions (
    c_space_id TEXT NOT NULL,
    c_action_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_action_type TEXT NOT NULL,
    c_reason TEXT NOT NULL,
    c_request_json TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_result_json TEXT,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_action_id),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id),
    CHECK (
        c_action_type IN (
            'MANUAL_ORDER',
            'CANCEL_ORDER',
            'FLATTEN',
            'REDUCE',
            'DELIST_FLATTEN'
        )
    ),
    CHECK (c_status IN ('RUNNING', 'COMPLETED', 'PARTIAL', 'FAILED')),
    CHECK (json_valid(c_request_json)),
    CHECK (c_result_json IS NULL OR json_valid(c_result_json))
);

CREATE INDEX IF NOT EXISTS idx_operator_actions_logical_account
ON t_operator_actions (
    c_space_id,
    c_logical_account_id,
    c_status,
    c_mtime
);
