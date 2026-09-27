package domain

import "time"

type RunStatus string

const (
	RunStatusPlanned       RunStatus = "planned"
	RunStatusActive        RunStatus = "active"
	RunStatusSucceeded     RunStatus = "succeeded"
	RunStatusPartialFailed RunStatus = "partial_failed"
	RunStatusFailed        RunStatus = "failed"
)

type CollectionRun struct {
	ID           int        `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID      string     `gorm:"column:c_space_id"`
	RunID        string     `gorm:"column:c_run_id"`
	RunKey       string     `gorm:"column:c_run_key"`
	RunType      string     `gorm:"column:c_run_type"`
	Frequency    string     `gorm:"column:c_frequency"`
	TargetTime   *time.Time `gorm:"column:c_target_time"`
	Status       RunStatus  `gorm:"column:c_status"`
	ErrorSummary string     `gorm:"column:c_error_summary"`
	CreateTime   time.Time  `gorm:"column:c_ctime"`
	ModifyTime   time.Time  `gorm:"column:c_mtime"`
}

func (r *CollectionRun) TableName() string { return "t_collector_runs" }
