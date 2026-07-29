CREATE TABLE IF NOT EXISTS t_logical_accounts (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_owner_runner_id TEXT,
    c_execution_mode TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_settlement_asset TEXT NOT NULL,
    c_automation_state TEXT NOT NULL DEFAULT 'PAUSED',
    c_pause_reason TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_logical_account_id),
    UNIQUE (c_space_id, c_name),
    CHECK (c_execution_mode IN ('PAPER', 'LIVE')),
    CHECK (c_market_type IN ('SPOT', 'SWAP')),
    CHECK (c_automation_state IN ('ACTIVE', 'PAUSED')),
    CHECK (
        (c_automation_state = 'ACTIVE' AND c_pause_reason = '')
        OR
        (c_automation_state = 'PAUSED' AND c_pause_reason <> '')
    )
);

CREATE TABLE IF NOT EXISTS t_logical_account_members (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_exchange_account_id TEXT NOT NULL,
    c_enabled INTEGER NOT NULL DEFAULT 1,
    c_priority INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (
        c_space_id,
        c_logical_account_id,
        c_exchange_account_id
    ),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
        ON DELETE CASCADE,
    FOREIGN KEY (c_space_id, c_exchange_account_id)
        REFERENCES t_exchange_accounts (c_space_id, c_exchange_account_id),
    CHECK (c_enabled IN (0, 1))
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_enabled_physical_account_membership
ON t_logical_account_members (c_space_id, c_exchange_account_id)
WHERE c_enabled = 1;

CREATE INDEX IF NOT EXISTS idx_logical_account_members_account
ON t_logical_account_members (c_space_id, c_exchange_account_id);

CREATE TABLE IF NOT EXISTS t_logical_account_targets (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_runner_id TEXT NOT NULL,
    c_command_sequence INTEGER NOT NULL,
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
    CHECK (c_command_sequence > 0),
    CHECK (c_status IN ('PENDING', 'CONVERGING', 'CONVERGED', 'BLOCKED')),
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
    CHECK (c_action_type IN ('MANUAL_ORDER', 'CANCEL_ORDER', 'FLATTEN')),
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
