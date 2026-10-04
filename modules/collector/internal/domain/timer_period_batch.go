package domain

import "time"

// TimerPeriodBatch freezes one shard's Timer work for a Dataset period.
// ClaimRequestID and ClaimedAt are the only mutable ownership fields; the
// remainder describes the immutable plan shared by every Timer retry.
type TimerPeriodBatch struct {
	Key            string     `gorm:"column:c_key;primaryKey"`
	SpaceID        string     `gorm:"column:c_space_id"`
	DatasetID      string     `gorm:"column:c_dataset_id"`
	Frequency      string     `gorm:"column:c_frequency"`
	PeriodTime     time.Time  `gorm:"column:c_period_time"`
	TaskID         string     `gorm:"column:c_task_id"`
	FirstRunID     string     `gorm:"column:c_first_run_id"`
	SeriesHash     string     `gorm:"column:c_series_hash"`
	ExpectedCount  uint32     `gorm:"column:c_expected_count"`
	GroupID        uint32     `gorm:"column:c_group_id"`
	GroupCount     uint32     `gorm:"column:c_group_count"`
	ShardIndex     uint32     `gorm:"column:c_shard_index"`
	BindingHash    string     `gorm:"column:c_binding_hash"`
	RouteVersion   string     `gorm:"column:c_route_version"`
	BatchID        string     `gorm:"column:c_batch_id"`
	FunctionName   string     `gorm:"column:c_function_name"`
	NodeID         string     `gorm:"column:c_node_id"`
	Region         string     `gorm:"column:c_region"`
	ClaimRequestID string     `gorm:"column:c_claim_request_id"`
	ClaimedAt      *time.Time `gorm:"column:c_claimed_at"`
	DeadlineAt     time.Time  `gorm:"column:c_deadline_at"`
	CreateTime     time.Time  `gorm:"column:c_ctime"`
}

func (TimerPeriodBatch) TableName() string { return "t_collector_timer_period_batches" }
