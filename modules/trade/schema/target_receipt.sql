CREATE TABLE IF NOT EXISTS t_logical_account_target_receipts (
    c_space_id TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_runner_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_command_sequence INTEGER NOT NULL,
    c_request_hash TEXT NOT NULL,
    c_signal_time INTEGER NOT NULL,
    c_weights_json TEXT NOT NULL,
    c_equity TEXT NOT NULL,
    c_equity_source_time INTEGER NOT NULL,
    c_reference_prices_json TEXT NOT NULL,
    c_quantity_targets_json TEXT NOT NULL,
    c_accepted_at INTEGER NOT NULL,
    PRIMARY KEY (c_space_id, c_target_id),
    -- The runner is part of the sequence namespace. command_sequence remains
    -- monotonic for a runner, while target_id is the replay/idempotency key
    -- for one accepted command.
    UNIQUE (c_space_id, c_logical_account_id, c_runner_id, c_command_sequence),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
        ON DELETE CASCADE,
    CHECK (c_command_sequence > 0),
    CHECK (json_valid(c_weights_json)),
    CHECK (json_type(c_weights_json) = 'array'),
    CHECK (json_valid(c_reference_prices_json)),
    CHECK (json_type(c_reference_prices_json) = 'object'),
    CHECK (json_valid(c_quantity_targets_json)),
    CHECK (json_type(c_quantity_targets_json) = 'array')
);

CREATE INDEX IF NOT EXISTS idx_target_receipts_logical
ON t_logical_account_target_receipts (c_space_id, c_logical_account_id, c_accepted_at);
