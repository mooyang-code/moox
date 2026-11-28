CREATE TABLE IF NOT EXISTS t_logical_accounts (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    -- 关闭到哪个会话为止（会话的开始时刻，毫秒）：开始时刻不大于它的会话的目标一律不再接受
    c_closed_through INTEGER NOT NULL DEFAULT 0,
    c_control_mode TEXT NOT NULL DEFAULT 'STRATEGY',
    -- 组合所属环境：PAPER 模拟盘、TESTNET 测试网、PRODUCTION 生产环境，创建时定下
    c_environment TEXT NOT NULL,
    c_settlement_asset TEXT NOT NULL,
    -- 下单设置与资金设置：空对象表示全部取默认值
    c_order_settings_json TEXT NOT NULL DEFAULT '{}',
    c_fund_settings_json TEXT NOT NULL DEFAULT '{}',
    -- 暂停原因只用于展示；程序按控制方式、暂停来源与减仓状态判断；自主下单组合与运行中的策略组合三列均为空
    c_pause_reason TEXT NOT NULL DEFAULT '',
    -- 暂停来源：USER 人工（含新建策略组合时的初始暂停）、SYSTEM 系统自动、MARGIN 保证金率风控；未暂停或自主下单为空
    c_pause_source TEXT NOT NULL DEFAULT '',
    -- 保证金率风控暂停的减仓状态：REDUCED 已发起减仓、PENDING 减仓待执行、UNREDUCED 未减仓已回安全线、FAILED 减仓失败、PARTIAL 只减了一部分；其他情况为空
    c_margin_reduce_state TEXT NOT NULL DEFAULT '',
    -- 恢复自动执行时已人工确认的最近一次保证金风控减仓动作号
    c_acked_margin_action_id TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_control_mode IN ('STRATEGY', 'MANUAL')),
    CHECK (c_environment IN ('PAPER', 'TESTNET', 'PRODUCTION')),
    CHECK (json_valid(c_order_settings_json)),
    CHECK (json_type(c_order_settings_json) = 'object'),
    CHECK (json_valid(c_fund_settings_json)),
    CHECK (json_type(c_fund_settings_json) = 'object'),
    CHECK (c_pause_source IN ('', 'USER', 'SYSTEM', 'MARGIN')),
    CHECK (c_margin_reduce_state IN ('', 'REDUCED', 'PENDING', 'UNREDUCED', 'FAILED', 'PARTIAL')),
    CHECK ((c_pause_source = '') = (c_pause_reason = '')),
    CHECK ((c_pause_source = 'MARGIN') = (c_margin_reduce_state <> '')),
    CHECK (
        c_control_mode <> 'MANUAL'
        OR (c_pause_source = '' AND c_pause_reason = '' AND c_margin_reduce_state = '')
    ),
    UNIQUE (c_space_id, c_name),
    PRIMARY KEY (c_space_id, c_logical_account_id)
);

CREATE TRIGGER IF NOT EXISTS trg_t_logical_accounts_mtime
AFTER UPDATE ON t_logical_accounts
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_logical_accounts
    SET c_mtime = CURRENT_TIMESTAMP
    WHERE c_space_id = OLD.c_space_id AND c_logical_account_id = OLD.c_logical_account_id;
END;

CREATE TABLE IF NOT EXISTS t_logical_account_members (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_enabled INTEGER NOT NULL DEFAULT 1,
    c_priority INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_enabled IN (0, 1)),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
        ON DELETE CASCADE,
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_trading_accounts (c_space_id, c_trading_account_id),
    PRIMARY KEY (
        c_space_id,
        c_logical_account_id,
        c_trading_account_id
    )
);

-- 一个执行账户最多属于一个组合账户（启用和停用成员都算）
CREATE UNIQUE INDEX IF NOT EXISTS ux_logical_account_members_trading_account
ON t_logical_account_members (c_space_id, c_trading_account_id);

CREATE TRIGGER IF NOT EXISTS trg_t_logical_account_members_mtime
AFTER UPDATE ON t_logical_account_members
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_logical_account_members
    SET c_mtime = CURRENT_TIMESTAMP
    WHERE c_space_id = OLD.c_space_id
        AND c_logical_account_id = OLD.c_logical_account_id
        AND c_trading_account_id = OLD.c_trading_account_id;
END;

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
    CHECK (
        c_instance_id <> ''
        AND c_session_id <> ''
        AND c_session_started_at > 0
        AND c_strategy_id <> ''
        AND c_bar_end_time > 0
        AND c_valid_until > c_bar_end_time
    ),
    CHECK (c_status IN ('PENDING', 'CONVERGING', 'CONVERGED', 'BLOCKED', 'EXPIRED')),
    CHECK (json_valid(c_targets_json)),
    CHECK (json_type(c_targets_json) = 'array'),
    CHECK (json_valid(c_blocked_targets_json)),
    CHECK (json_type(c_blocked_targets_json) = 'array'),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
        ON DELETE CASCADE,
    UNIQUE (c_space_id, c_target_id),
    PRIMARY KEY (c_space_id, c_logical_account_id)
);

CREATE INDEX IF NOT EXISTS idx_logical_account_targets_status
ON t_logical_account_targets (c_space_id, c_status, c_mtime);

CREATE TRIGGER IF NOT EXISTS trg_t_logical_account_targets_mtime
AFTER UPDATE ON t_logical_account_targets
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_logical_account_targets
    SET c_mtime = CURRENT_TIMESTAMP
    WHERE c_space_id = OLD.c_space_id AND c_logical_account_id = OLD.c_logical_account_id;
END;

CREATE TABLE IF NOT EXISTS t_operator_actions (
    c_space_id TEXT NOT NULL,
    c_action_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_action_type TEXT NOT NULL,
    -- 动作来源：USER 人工操作、MARGIN 保证金率风控、DELIST 下架清仓
    c_source TEXT NOT NULL DEFAULT 'USER',
    c_reason TEXT NOT NULL,
    c_request_json TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_result_json TEXT,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_created_at INTEGER NOT NULL,
    c_updated_at INTEGER NOT NULL,
    CHECK (
        c_action_type IN (
            'MANUAL_ORDER',
            'CANCEL_ORDER',
            'FLATTEN',
            'REDUCE',
            'DELIST_FLATTEN'
        )
    ),
    CHECK (c_source IN ('USER', 'MARGIN', 'DELIST')),
    CHECK (c_status IN ('RUNNING', 'COMPLETED', 'PARTIAL', 'FAILED')),
    CHECK (json_valid(c_request_json)),
    CHECK (c_result_json IS NULL OR json_valid(c_result_json)),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id),
    PRIMARY KEY (c_space_id, c_action_id)
);

CREATE INDEX IF NOT EXISTS idx_operator_actions_logical_account
ON t_operator_actions (
    c_space_id,
    c_logical_account_id,
    c_status,
    c_updated_at
);
CREATE INDEX IF NOT EXISTS idx_operator_actions_logical_source
ON t_operator_actions (
    c_space_id,
    c_logical_account_id,
    c_source,
    c_created_at,
    c_action_id
);
