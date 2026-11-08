-- 事件中心的自有库。时间一律用 Unix 毫秒。

-- ************ 事件规则 ************
-- 通知地址规则的验证密钥 c_secret 要用来计算签名，只能明文保存；库文件权限由部署目录保证。
CREATE TABLE IF NOT EXISTS t_event_rules (
    c_rule_id TEXT NOT NULL PRIMARY KEY,
    c_name TEXT NOT NULL,
    c_event_name TEXT NOT NULL,
    c_scope_json TEXT NOT NULL DEFAULT '[]',
    c_action_type TEXT NOT NULL,
    c_notify_url TEXT NOT NULL DEFAULT '',
    c_allow_private INTEGER NOT NULL DEFAULT 0,
    c_secret TEXT NOT NULL DEFAULT '',
    c_system_action TEXT NOT NULL DEFAULT '',
    c_system INTEGER NOT NULL DEFAULT 0,
    c_enabled INTEGER NOT NULL DEFAULT 1,
    c_created_at INTEGER NOT NULL,
    c_updated_at INTEGER NOT NULL,
    CHECK (c_action_type IN ('NOTIFY_URL', 'SYSTEM')),
    CHECK (c_allow_private IN (0, 1)),
    CHECK (c_system IN (0, 1)),
    CHECK (c_enabled IN (0, 1)),
    CHECK (json_valid(c_scope_json)),
    CHECK (json_type(c_scope_json) = 'array'),
    CHECK (
        (c_action_type = 'NOTIFY_URL' AND c_notify_url <> '' AND c_secret <> '' AND c_system_action = '' AND c_system = 0) OR
        (c_action_type = 'SYSTEM' AND c_notify_url = '' AND c_secret = '' AND c_system_action <> '' AND c_system = 1)
    )
);

-- 系统动作规则每种动作只有服务自带的一条。
CREATE UNIQUE INDEX IF NOT EXISTS ux_t_event_rules_system_action
ON t_event_rules (c_system_action)
WHERE c_system = 1;

CREATE INDEX IF NOT EXISTS idx_t_event_rules_event ON t_event_rules (c_event_name, c_enabled);

-- ************ 事件回执 ************
-- 同一事件对同一规则只处理一次：插入失败说明已经处理过。
CREATE TABLE IF NOT EXISTS t_event_receipts (
    c_event_id TEXT NOT NULL,
    c_rule_id TEXT NOT NULL,
    c_created_at INTEGER NOT NULL,
    PRIMARY KEY (c_event_id, c_rule_id)
);

CREATE INDEX IF NOT EXISTS idx_t_event_receipts_created ON t_event_receipts (c_created_at);

-- ************ 执行记录 ************
-- c_dedupe_key 只用于系统动作：规则加“交易所:市场:代码”，同一标的未结束的记录合并成一条。
-- c_next_run_at 是工作线程下一次该处理这条记录的时间；已结束的记录为 0。
-- c_attempts 是累计尝试次数（编号用），c_failures 是自上次开始或重新执行以来的连续失败次数（通知用它决定重试间隔）。
-- 系统动作的 c_failures 是连续暂时失败（调用 Trade 出错等）的次数，c_first_failed_at 是其中第一次的时间，一次推进没有出错就清零。
CREATE TABLE IF NOT EXISTS t_event_executions (
    c_execution_id TEXT NOT NULL PRIMARY KEY,
    c_rule_id TEXT NOT NULL,
    c_rule_name TEXT NOT NULL,
    c_action_type TEXT NOT NULL,
    c_event_id TEXT NOT NULL,
    c_event_name TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_occurred_at INTEGER NOT NULL,
    c_payload_json TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'PENDING',
    c_attempts INTEGER NOT NULL DEFAULT 0,
    c_failures INTEGER NOT NULL DEFAULT 0,
    c_first_failed_at INTEGER NOT NULL DEFAULT 0,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_dedupe_key TEXT NOT NULL DEFAULT '',
    -- 最近一次领取（置为执行中）的时间，同时是领取凭证：写回结果时必须对得上，卡住后被放回再被领取的记录，旧线程的结果写不进来。
    c_claimed_at INTEGER NOT NULL DEFAULT 0,
    c_next_run_at INTEGER NOT NULL DEFAULT 0,
    c_duration_ms INTEGER NOT NULL DEFAULT 0,
    c_acknowledged INTEGER NOT NULL DEFAULT 0,
    -- 系统动作记录已取消但善后（解除禁止买入）还没做成：1 时可以再取消一次重做，善后成功后清 0；清理旧记录时跳过。
    c_cleanup_pending INTEGER NOT NULL DEFAULT 0,
    -- 每次置位“善后未完成”加一。善后做完只在序号没变时才清除标记：做善后期间别处（Step 写名单后补解除失败）又置了位，它不会被吞掉。
    c_cleanup_seq INTEGER NOT NULL DEFAULT 0,
    c_created_at INTEGER NOT NULL,
    c_updated_at INTEGER NOT NULL,
    c_finished_at INTEGER NOT NULL DEFAULT 0,
    CHECK (c_action_type IN ('NOTIFY_URL', 'SYSTEM')),
    CHECK (c_status IN ('PENDING', 'RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELED')),
    CHECK (c_acknowledged IN (0, 1)),
    CHECK (c_cleanup_pending IN (0, 1)),
    CHECK (json_valid(c_payload_json))
);

CREATE INDEX IF NOT EXISTS idx_t_event_executions_due ON t_event_executions (c_status, c_next_run_at);
CREATE INDEX IF NOT EXISTS idx_t_event_executions_rule ON t_event_executions (c_rule_id, c_created_at);
CREATE INDEX IF NOT EXISTS idx_t_event_executions_created ON t_event_executions (c_created_at);
CREATE UNIQUE INDEX IF NOT EXISTS ux_t_event_executions_dedupe
ON t_event_executions (c_dedupe_key)
WHERE c_dedupe_key <> '' AND c_status IN ('PENDING', 'RUNNING');

-- ************ 通知的每次尝试 ************
CREATE TABLE IF NOT EXISTS t_event_attempts (
    c_execution_id TEXT NOT NULL,
    c_attempt INTEGER NOT NULL,
    c_at INTEGER NOT NULL,
    c_http_status INTEGER NOT NULL DEFAULT 0,
    c_duration_ms INTEGER NOT NULL DEFAULT 0,
    c_response_snippet TEXT NOT NULL DEFAULT '',
    c_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (c_execution_id, c_attempt),
    FOREIGN KEY (c_execution_id)
        REFERENCES t_event_executions (c_execution_id)
        ON DELETE CASCADE
);

-- ************ 下架处理的各轮步骤 ************
-- 轮次 0 是发现即禁止，1 是下架前 24 小时平仓，2 是下架前 2 小时复查平仓，3 是下架后核对。
-- 平仓轮次里空间和组合账户都为空的一行是这一轮的汇总（查到几个持有者、零头），每个持有的组合另有一行。
CREATE TABLE IF NOT EXISTS t_delist_actions (
    c_execution_id TEXT NOT NULL,
    c_round INTEGER NOT NULL,
    c_kind TEXT NOT NULL,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_logical_account_id TEXT NOT NULL DEFAULT '',
    c_exchange TEXT NOT NULL DEFAULT '',
    c_operator_action_id TEXT NOT NULL DEFAULT '',
    c_reason TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'PENDING',
    c_attempts INTEGER NOT NULL DEFAULT 0,
    c_result TEXT NOT NULL DEFAULT '',
    c_error TEXT NOT NULL DEFAULT '',
    c_next_check_at INTEGER NOT NULL DEFAULT 0,
    -- 只用于禁止买入那一行：演练（PAPER_ONLY）核对已有结论（SUCCEEDED 或 FAILED，说明在 c_error）而解除禁止没做成时记下结论，
    -- 之后只重试解除，结论不会因为名单里已经没有这个标的（解除其实已在 Trade 上生效、响应丢了）而丢掉；解除成功后清空。
    c_release_verdict TEXT NOT NULL DEFAULT '',
    -- 只用于禁止买入那一行：演练结束时本记录自己解除名单（解除之前先置 1）。之后记录被重新打开（合并了新的登记、重新执行），
    -- 名单里没有这一项不是手工解除，要清掉上一次演练的各轮步骤、重新写名单、按新时间线走；重新写名单或看到名单里还有这一项时清 0。
    c_self_released INTEGER NOT NULL DEFAULT 0,
    -- 平仓各行和汇总行：这一轮是按哪个下架时间（Unix 毫秒）规划的。改期后这一轮没成功就按新时间重开（换新操作号）。
    -- 禁止买入那一行：记下解除待重试的结论时，结论对应的下架时间；对不上说明之后合并进了新的登记，结论作废。
    -- 核对那一行：按哪个下架时间核对的；演练结束后又回到待处理而下架时间不同，说明合并了新的登记，重走时间线。
    c_planned_delist_at INTEGER NOT NULL DEFAULT 0,
    -- 只用于平仓各行：从第一次遇到 Trade 正忙起的时间（Unix 毫秒），能发起或失败后清 0；持续 30 分钟还没能发起就算暂时失败。
    c_busy_since INTEGER NOT NULL DEFAULT 0,
    -- 只用于平仓各行和汇总行：这一行所属计划的重开序号，参与生成操作号。一轮第一次规划为 0，每按新的下架时间重开一次加 1；
    -- 下架时间 A→B→A 改回原值后重开的一轮，操作号因此不会与第一次相同（同号请求 Trade 直接返回旧操作已结束的结果）。同一行重发沿用存下的操作号。
    c_reopen_seq INTEGER NOT NULL DEFAULT 0,
    -- 只用于禁止买入那一行：演练结束后被重新打开、清掉上一次的各轮步骤之前，记下上一次各轮用过的最大重开序号加 1。
    -- 新一次演练各轮第一次规划从它开始，操作号不会与上一次演练用过的相同（同号请求 Trade 直接返回旧操作已结束的结果）。
    c_next_reopen_seq INTEGER NOT NULL DEFAULT 0,
    c_updated_at INTEGER NOT NULL,
    PRIMARY KEY (c_execution_id, c_round, c_kind, c_space_id, c_logical_account_id),
    FOREIGN KEY (c_execution_id)
        REFERENCES t_event_executions (c_execution_id)
        ON DELETE CASCADE,
    CHECK (c_release_verdict IN ('', 'SUCCEEDED', 'FAILED')),
    CHECK (c_self_released IN (0, 1)),
    CHECK (c_kind IN ('BLOCK', 'FLATTEN', 'VERIFY')),
    CHECK (c_status IN ('PENDING', 'RUNNING', 'SUCCEEDED', 'FAILED', 'PARTIAL'))
);
