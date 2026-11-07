package store

// 动作种类与执行状态；取值与 schema 里的 CHECK 约束一致。
const (
	ActionNotifyURL = "NOTIFY_URL"
	ActionSystem    = "SYSTEM"

	StatusPending   = "PENDING"
	StatusRunning   = "RUNNING"
	StatusSucceeded = "SUCCEEDED"
	StatusFailed    = "FAILED"
	StatusCanceled  = "CANCELED"

	// SystemActionSellDelistedHoldings 是第一版唯一的系统动作：卖出策略持有的下架币。
	SystemActionSellDelistedHoldings = "SELL_DELISTED_HOLDINGS"
)

// ScopeFilter 是规则限定范围里的一项：同一 Field 的 Values 之间是“或”，不同 Field 之间是“且”。
type ScopeFilter struct {
	Field  string   `json:"field"`
	Values []string `json:"values"`
}

// Rule 是一条事件规则。
type Rule struct {
	RuleID       string
	Name         string
	EventName    string
	Scope        []ScopeFilter
	ActionType   string
	NotifyURL    string
	AllowPrivate bool
	Secret       string
	SystemAction string
	System       bool
	Enabled      bool
	CreatedAt    int64
	UpdatedAt    int64
}

// Execution 是一条执行记录。
type Execution struct {
	ExecutionID   string
	RuleID        string
	RuleName      string
	ActionType    string
	EventID       string
	EventName     string
	SpaceID       string
	SubjectID     string
	OccurredAt    int64
	PayloadJSON   string
	Status        string
	Attempts      int
	Failures      int
	FirstFailedAt int64
	LastError     string
	DedupeKey     string
	// ClaimedAt 是最近一次领取的时间，也是领取凭证（见 SaveResult）。
	ClaimedAt    int64
	NextRunAt    int64
	DurationMS   int64
	Acknowledged bool
	// CleanupPending 表示系统动作记录已取消，但取消后的善后（解除禁止买入）还没做成；做成之前可以再取消一次重做。
	CleanupPending bool
	// CleanupSeq 是“善后未完成”标记每次置位时加一的序号，FinishCleanup 凭它判断善后期间标记有没有被重新置位。
	CleanupSeq int64
	CreatedAt  int64
	UpdatedAt  int64
	FinishedAt int64
}

// Attempt 是通知地址的一次尝试。
type Attempt struct {
	ExecutionID     string
	Attempt         int
	At              int64
	HTTPStatus      int
	DurationMS      int64
	ResponseSnippet string
	Error           string
}

// DelistAction 是下架处理里某一轮对某个组合账户做的一步。
type DelistAction struct {
	ExecutionID      string
	Round            int
	Kind             string
	SpaceID          string
	LogicalAccountID string
	Exchange         string
	OperatorActionID string
	Reason           string
	Status           string
	Attempts         int
	Result           string
	Error            string
	NextCheckAt      int64
	// OwnsBlock 只用于禁止买入那一行：标的是这条记录自己写进名单的。
	OwnsBlock bool
	UpdatedAt int64
}

// 下架处理的步骤种类与状态。
const (
	DelistKindBlock   = "BLOCK"
	DelistKindFlatten = "FLATTEN"
	DelistKindVerify  = "VERIFY"

	DelistPending   = "PENDING"
	DelistRunning   = "RUNNING"
	DelistSucceeded = "SUCCEEDED"
	DelistFailed    = "FAILED"
	// DelistPartial 是平仓只完成了一部分：Trade 报 PARTIAL（最近一笔被交易所拒绝、连续多笔没成交等），这一轮不再重试，由下一轮复查换新操作号再平。
	DelistPartial = "PARTIAL"
)

// 数据库行：列名与 schema 一一对应。
type ruleRow struct {
	RuleID       string `gorm:"column:c_rule_id;primaryKey"`
	Name         string `gorm:"column:c_name"`
	EventName    string `gorm:"column:c_event_name"`
	ScopeJSON    string `gorm:"column:c_scope_json"`
	ActionType   string `gorm:"column:c_action_type"`
	NotifyURL    string `gorm:"column:c_notify_url"`
	AllowPrivate bool   `gorm:"column:c_allow_private"`
	Secret       string `gorm:"column:c_secret"`
	SystemAction string `gorm:"column:c_system_action"`
	System       bool   `gorm:"column:c_system"`
	Enabled      bool   `gorm:"column:c_enabled"`
	CreatedAt    int64  `gorm:"column:c_created_at;autoCreateTime:false"`
	UpdatedAt    int64  `gorm:"column:c_updated_at;autoUpdateTime:false"`
}

func (ruleRow) TableName() string { return "t_event_rules" }

type executionRow struct {
	ExecutionID    string `gorm:"column:c_execution_id;primaryKey"`
	RuleID         string `gorm:"column:c_rule_id"`
	RuleName       string `gorm:"column:c_rule_name"`
	ActionType     string `gorm:"column:c_action_type"`
	EventID        string `gorm:"column:c_event_id"`
	EventName      string `gorm:"column:c_event_name"`
	SpaceID        string `gorm:"column:c_space_id"`
	SubjectID      string `gorm:"column:c_subject_id"`
	OccurredAt     int64  `gorm:"column:c_occurred_at"`
	PayloadJSON    string `gorm:"column:c_payload_json"`
	Status         string `gorm:"column:c_status"`
	Attempts       int    `gorm:"column:c_attempts"`
	Failures       int    `gorm:"column:c_failures"`
	FirstFailedAt  int64  `gorm:"column:c_first_failed_at"`
	LastError      string `gorm:"column:c_last_error"`
	DedupeKey      string `gorm:"column:c_dedupe_key"`
	ClaimedAt      int64  `gorm:"column:c_claimed_at"`
	NextRunAt      int64  `gorm:"column:c_next_run_at"`
	DurationMS     int64  `gorm:"column:c_duration_ms"`
	Acknowledged   bool   `gorm:"column:c_acknowledged"`
	CleanupPending bool   `gorm:"column:c_cleanup_pending"`
	CleanupSeq     int64  `gorm:"column:c_cleanup_seq"`
	CreatedAt      int64  `gorm:"column:c_created_at;autoCreateTime:false"`
	UpdatedAt      int64  `gorm:"column:c_updated_at;autoUpdateTime:false"`
	FinishedAt     int64  `gorm:"column:c_finished_at"`
}

func (executionRow) TableName() string { return "t_event_executions" }

type attemptRow struct {
	ExecutionID     string `gorm:"column:c_execution_id;primaryKey"`
	Attempt         int    `gorm:"column:c_attempt;primaryKey"`
	At              int64  `gorm:"column:c_at"`
	HTTPStatus      int    `gorm:"column:c_http_status"`
	DurationMS      int64  `gorm:"column:c_duration_ms"`
	ResponseSnippet string `gorm:"column:c_response_snippet"`
	Error           string `gorm:"column:c_error"`
}

func (attemptRow) TableName() string { return "t_event_attempts" }

type delistRow struct {
	ExecutionID      string `gorm:"column:c_execution_id;primaryKey"`
	Round            int    `gorm:"column:c_round;primaryKey"`
	Kind             string `gorm:"column:c_kind;primaryKey"`
	SpaceID          string `gorm:"column:c_space_id;primaryKey"`
	LogicalAccountID string `gorm:"column:c_logical_account_id;primaryKey"`
	Exchange         string `gorm:"column:c_exchange"`
	OperatorActionID string `gorm:"column:c_operator_action_id"`
	Reason           string `gorm:"column:c_reason"`
	Status           string `gorm:"column:c_status"`
	Attempts         int    `gorm:"column:c_attempts"`
	Result           string `gorm:"column:c_result"`
	Error            string `gorm:"column:c_error"`
	NextCheckAt      int64  `gorm:"column:c_next_check_at"`
	OwnsBlock        bool   `gorm:"column:c_owns_block"`
	UpdatedAt        int64  `gorm:"column:c_updated_at;autoUpdateTime:false"`
}

func (delistRow) TableName() string { return "t_delist_actions" }
