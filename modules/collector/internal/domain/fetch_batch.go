package domain

import "time"

type BatchKind string

const (
	BatchKindRealtime       BatchKind = "realtime"
	BatchKindSymbolSnapshot BatchKind = "symbol_snapshot"
	BatchKindCatchup        BatchKind = "catchup"
)

type BatchStatus string

const (
	BatchStatusPlanned       BatchStatus = "planned"
	BatchStatusDispatched    BatchStatus = "dispatched"
	BatchStatusSucceeded     BatchStatus = "succeeded"
	BatchStatusPartialFailed BatchStatus = "partial_failed"
	BatchStatusFailed        BatchStatus = "failed"
	BatchStatusTimedOut      BatchStatus = "timed_out"
)

func (s BatchStatus) Terminal() bool {
	return s == BatchStatusSucceeded || s == BatchStatusPartialFailed || s == BatchStatusFailed || s == BatchStatusTimedOut
}

type ItemOutcome string

const (
	ItemOutcomeSuccess      ItemOutcome = "success"
	ItemOutcomeHTTP429      ItemOutcome = "http_429"
	ItemOutcomeHTTP5xx      ItemOutcome = "http_5xx"
	ItemOutcomeNetworkError ItemOutcome = "network_error"
	ItemOutcomeStorageError ItemOutcome = "storage_error"
	ItemOutcomeInvalid      ItemOutcome = "invalid_request"
)

type CollectionItem struct {
	TaskID             string `json:"task_id,omitempty"`
	SubjectID          string `json:"subject_id"`
	Symbol             string `json:"symbol"`
	TargetDataTime     string `json:"target_data_time,omitempty"`
	StartTime          string `json:"start_time,omitempty"`
	BarLimit           int    `json:"bar_limit,omitempty"`
	SourceEventID      string `json:"source_event_id,omitempty"`
	Provider           string `json:"provider"`
	MarketType         string `json:"market_type"`
	DataType           string `json:"data_type"`
	DatasetID          string `json:"dataset_id"`
	Frequency          string `json:"frequency,omitempty"`
	SnapshotShardIndex int    `json:"snapshot_shard_index,omitempty"`
	SnapshotShardCount int    `json:"snapshot_shard_count,omitempty"`
}

type ItemResult struct {
	CollectionItem
	Outcome      ItemOutcome `json:"outcome"`
	ErrorType    string      `json:"error_type,omitempty"`
	ErrorSummary string      `json:"error_summary,omitempty"`
}

type BatchInvocation struct {
	ID                   int         `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID              string      `gorm:"column:c_space_id"`
	BatchID              string      `gorm:"column:c_batch_id"`
	ParentBatchID        string      `gorm:"column:c_parent_batch_id"`
	ScheduleID           string      `gorm:"column:c_schedule_id"`
	BatchKind            BatchKind   `gorm:"column:c_batch_kind"`
	ShardIndex           int         `gorm:"column:c_shard_index"`
	RuleID               string      `gorm:"column:c_rule_id"`
	DatasetID            string      `gorm:"column:c_dataset_id"`
	Frequency            string      `gorm:"column:c_frequency"`
	Region               string      `gorm:"column:c_region"`
	NodeID               string      `gorm:"column:c_node_id"`
	FunctionName         string      `gorm:"column:c_function_name"`
	Status               BatchStatus `gorm:"column:c_status"`
	Attempt              int         `gorm:"column:c_attempt"`
	RequestID            string      `gorm:"column:c_request_id"`
	RequestJSON          string      `gorm:"column:c_request_json"`
	PlannedCount         int         `gorm:"column:c_planned_count"`
	SuccessCount         int         `gorm:"column:c_success_count"`
	RetryCount           int         `gorm:"column:c_retry_count"`
	PermanentFailedCount int         `gorm:"column:c_permanent_failed_count"`
	ErrorSummary         string      `gorm:"column:c_error_summary"`
	LateCompletion       bool        `gorm:"column:c_late_completion"`
	PlannedAt            *time.Time  `gorm:"column:c_planned_at"`
	DispatchedAt         *time.Time  `gorm:"column:c_dispatched_at"`
	DeadlineAt           *time.Time  `gorm:"column:c_deadline_at"`
	CompletedAt          *time.Time  `gorm:"column:c_completed_at"`
	CreateTime           time.Time   `gorm:"column:c_ctime"`
	ModifyTime           time.Time   `gorm:"column:c_mtime"`
}

func (b *BatchInvocation) TableName() string { return "t_collector_fetch_batches" }

type RetryItem struct {
	ID               int        `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID          string     `gorm:"column:c_space_id"`
	RetryKey         string     `gorm:"column:c_retry_key"`
	SourceBatchID    string     `gorm:"column:c_source_batch_id"`
	BatchKind        BatchKind  `gorm:"column:c_batch_kind"`
	RuleID           string     `gorm:"column:c_rule_id"`
	DatasetID        string     `gorm:"column:c_dataset_id"`
	SubjectID        string     `gorm:"column:c_subject_id"`
	Frequency        string     `gorm:"column:c_frequency"`
	TargetDataTime   time.Time  `gorm:"column:c_target_data_time"`
	TaskJSON         string     `gorm:"column:c_task_json"`
	Attempt          int        `gorm:"column:c_attempt"`
	Status           string     `gorm:"column:c_status"`
	NextRetryAt      *time.Time `gorm:"column:c_next_retry_at"`
	LastErrorType    string     `gorm:"column:c_last_error_type"`
	LastErrorSummary string     `gorm:"column:c_last_error_summary"`
	CreateTime       time.Time  `gorm:"column:c_ctime"`
	ModifyTime       time.Time  `gorm:"column:c_mtime"`
}

func (r *RetryItem) TableName() string { return "t_collector_fetch_retry_items" }
