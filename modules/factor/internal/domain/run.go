package domain

import "time"

const (
	RunStatusSucceeded  = "succeeded"
	RunStatusFailed     = "failed"
	RunStatusSuperseded = "superseded"
)

// FactorRun is an append-only terminal execution record.
type FactorRun struct {
	RunID         string    `gorm:"column:c_run_id;primaryKey"`
	TriggerType   string    `gorm:"column:c_trigger_type"`
	SpaceID       string    `gorm:"column:c_space_id"`
	SourceDataset string    `gorm:"column:c_source_dataset"`
	TargetDataset string    `gorm:"column:c_target_dataset"`
	SubjectID     string    `gorm:"column:c_subject_id"`
	Freq          string    `gorm:"column:c_freq"`
	BarTime       string    `gorm:"column:c_bar_time"`
	FactorCount   int       `gorm:"column:c_factor_count"`
	Status        string    `gorm:"column:c_status"`
	Error         string    `gorm:"column:c_error"`
	ElapsedMS     int64     `gorm:"column:c_elapsed_ms"`
	CreateTime    time.Time `gorm:"column:c_ctime"`
}

// TableName returns the factor run table.
func (FactorRun) TableName() string {
	return "t_factor_runs"
}
