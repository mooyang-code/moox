package domain

import "time"

type MarketAttempt struct {
	JobItemID   string     `gorm:"column:c_job_item_id;primaryKey"`
	AttemptNo   int32      `gorm:"column:c_attempt_no;primaryKey"`
	PlanID      string     `gorm:"column:c_plan_id"`
	MarketID    string     `gorm:"column:c_market_id"`
	SpaceID     string     `gorm:"column:c_space_id"`
	ProviderID  string     `gorm:"column:c_provider_id"`
	Feed        string     `gorm:"column:c_feed"`
	Phase       string     `gorm:"column:c_phase"`
	WindowStart time.Time  `gorm:"column:c_window_start"`
	WindowEnd   time.Time  `gorm:"column:c_window_end"`
	Cursor      string     `gorm:"column:c_cursor"`
	Status      string     `gorm:"column:c_status"`
	Summary     string     `gorm:"column:c_summary"`
	ErrorClass  string     `gorm:"column:c_error_class"`
	Finalized   bool       `gorm:"column:c_finalized"`
	FinalizedAt *time.Time `gorm:"column:c_finalized_at"`
	CreateTime  time.Time  `gorm:"column:c_ctime"`
	ModifyTime  time.Time  `gorm:"column:c_mtime"`
}

func (MarketAttempt) TableName() string { return "t_collector_task_attempts" }

type AttemptSubject struct {
	JobItemID          string `gorm:"column:c_job_item_id;primaryKey"`
	AttemptNo          int32  `gorm:"column:c_attempt_no;primaryKey"`
	TaskID             string `gorm:"column:c_task_id;primaryKey"`
	SubjectID          string `gorm:"column:c_subject_id"`
	Status             string `gorm:"column:c_status"`
	NextCandidateIndex int    `gorm:"column:c_next_candidate_index"`
	Rows               int64  `gorm:"column:c_rows"`
	ErrorClass         string `gorm:"column:c_error_class"`
}

func (AttemptSubject) TableName() string { return "t_collector_attempt_subjects" }

type AttemptOutbox struct {
	OutboxID           string    `gorm:"column:c_outbox_id;primaryKey"`
	ParentJobItemID    string    `gorm:"column:c_parent_job_item_id"`
	ParentAttemptNo    int32     `gorm:"column:c_parent_attempt_no"`
	Kind               string    `gorm:"column:c_kind"`
	Payload            string    `gorm:"column:c_payload"`
	Status             string    `gorm:"column:c_status"`
	NextAttemptAt      time.Time `gorm:"column:c_next_attempt_at"`
	PublishedJobItemID string    `gorm:"column:c_published_job_item_id"`
	CreateTime         time.Time `gorm:"column:c_ctime"`
	ModifyTime         time.Time `gorm:"column:c_mtime"`
}

func (AttemptOutbox) TableName() string { return "t_collector_attempt_outbox" }

type ProviderRuntime struct {
	ProviderID        string     `gorm:"column:c_provider_id;primaryKey"`
	ScopeKey          string     `gorm:"column:c_scope_key;primaryKey"`
	CircuitState      string     `gorm:"column:c_circuit_state"`
	ConsecutiveErrors int        `gorm:"column:c_consecutive_errors"`
	OpenedAt          *time.Time `gorm:"column:c_opened_at"`
	ProbeInFlight     bool       `gorm:"column:c_probe_in_flight"`
	ModifyTime        time.Time  `gorm:"column:c_mtime"`
}

func (ProviderRuntime) TableName() string { return "t_collector_provider_runtime" }

type MarketGeneration struct {
	GenerationKey string    `gorm:"column:c_generation_key;primaryKey"`
	Epoch         int64     `gorm:"column:c_epoch"`
	Generation    time.Time `gorm:"column:c_generation"`
	Status        string    `gorm:"column:c_status"`
	ModifyTime    time.Time `gorm:"column:c_mtime"`
}

func (MarketGeneration) TableName() string { return "t_collector_market_generations" }

type ControlLeader struct {
	Name       string    `gorm:"column:c_name;primaryKey"`
	OwnerID    string    `gorm:"column:c_owner_id"`
	Epoch      int64     `gorm:"column:c_epoch"`
	ExpiresAt  time.Time `gorm:"column:c_expires_at"`
	ModifyTime time.Time `gorm:"column:c_mtime"`
}

func (ControlLeader) TableName() string { return "t_collector_control_leader" }
