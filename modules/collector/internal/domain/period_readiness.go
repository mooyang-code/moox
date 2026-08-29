package domain

import "time"

const (
	PeriodStatusWaiting  = "waiting"
	PeriodStatusComplete = "complete"
	PeriodStatusDegraded = "degraded"

	PeriodReportWaiting  = "waiting"
	PeriodReportPending  = "pending"
	PeriodReportReported = "reported"

	PeriodItemPending  = "pending"
	PeriodItemSuccess  = "success"
	PeriodItemTimedOut = "timed_out"
)

// PeriodReadiness identifies one immutable collection period. Its subject
// membership is captured when the row is created; later assignment changes
// only affect periods that have not been created yet.
type PeriodReadiness struct {
	ID          int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID     string    `gorm:"column:c_space_id"`
	DatasetID   string    `gorm:"column:c_dataset_id"`
	Frequency   string    `gorm:"column:c_frequency"`
	WorkType    string    `gorm:"column:c_work_type"`
	PeriodTime  time.Time `gorm:"column:c_period_time"`
	DeadlineAt  time.Time `gorm:"column:c_deadline_at"`
	Status      string    `gorm:"column:c_status"`
	ReportState string    `gorm:"column:c_report_state"`
	EventID     string    `gorm:"column:c_event_id"`
	CollectedAt time.Time `gorm:"column:c_collected_at"`
	PayloadJSON string    `gorm:"column:c_payload_json"`
	CreatedAt   time.Time `gorm:"column:c_ctime"`
	UpdatedAt   time.Time `gorm:"column:c_mtime"`
}

func (PeriodReadiness) TableName() string { return "t_period_readiness" }

type PeriodReadinessItem struct {
	ReadinessID    int64     `gorm:"column:c_readiness_id;primaryKey"`
	TaskID         string    `gorm:"column:c_task_id;primaryKey"`
	SubjectID      string    `gorm:"column:c_subject_id"`
	FunctionName   string    `gorm:"column:c_function_name"`
	WriteSource    string    `gorm:"column:c_write_source"`
	RequiredFields string    `gorm:"column:c_required_fields_json"`
	State          string    `gorm:"column:c_state"`
	UpdatedAt      time.Time `gorm:"column:c_updated_at"`
}

func (PeriodReadinessItem) TableName() string { return "t_period_readiness_items" }

type PeriodKey struct {
	SpaceID    string
	DatasetID  string
	Frequency  string
	PeriodTime time.Time
}

type PeriodTaskSeed struct {
	TaskID         string
	SubjectID      string
	FunctionName   string
	WriteSource    string
	RequiredFields string
}

type PeriodSeed struct {
	PeriodKey
	DeadlineAt time.Time
	WorkType   string
	Tasks      []PeriodTaskSeed
}

type PeriodReport struct {
	Readiness PeriodReadiness
	Items     []PeriodReadinessItem
}
