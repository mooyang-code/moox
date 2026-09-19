package domain

import "time"

type CollectionTaskPrepareState string

const (
	PrepareStatePending     CollectionTaskPrepareState = "pending"
	PrepareStateWaitingView CollectionTaskPrepareState = "waiting_view"
	PrepareStateReady       CollectionTaskPrepareState = "ready"
	PrepareStateError       CollectionTaskPrepareState = "error"
)

func (s CollectionTaskPrepareState) Valid() bool {
	switch s {
	case PrepareStatePending, PrepareStateWaitingView, PrepareStateReady, PrepareStateError:
		return true
	default:
		return false
	}
}

// CollectionTask is the Collector-owned collection task.
type CollectionTask struct {
	ID                int                        `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID           string                     `gorm:"column:c_space_id"`
	TaskID            string                     `gorm:"column:c_task_id"`
	TaskName          string                     `gorm:"column:c_task_name"`
	Description       string                     `gorm:"column:c_description"`
	DataType          string                     `gorm:"column:c_data_type"`
	Provider          string                     `gorm:"column:c_provider"`
	MarketType        string                     `gorm:"column:c_market_type"`
	CollectParams     string                     `gorm:"column:c_collect_params"`
	Enabled           bool                       `gorm:"column:c_enabled"`
	Creator           string                     `gorm:"column:c_creator"`
	PrepareState      CollectionTaskPrepareState `gorm:"column:c_prepare_state"`
	LastError         string                     `gorm:"column:c_last_error"`
	ResultDatasetID   string                     `gorm:"column:c_result_dataset_id"`
	ResultViewID      string                     `gorm:"column:c_result_view_id"`
	CoverageStartTime *time.Time                 `gorm:"column:c_coverage_start_time"`
	CreateTime        time.Time                  `gorm:"column:c_ctime"`
	ModifyTime        time.Time                  `gorm:"column:c_mtime"`
}

// TableName returns the Collector collection-task table.
func (r *CollectionTask) TableName() string {
	return "t_collector_tasks"
}
