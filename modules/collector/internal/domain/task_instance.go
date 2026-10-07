package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

const (
	InstanceStatusPending = 1
	InstanceStatusSuccess = 2
	InstanceStatusFailed  = 3
)

// Subject is the small metadata projection used when expanding Dataset tags.
type Subject struct {
	SubjectID string
	Name      string
	Status    string
}

// DatasetSubject is the subject projection consumed by job planners. Provider
// symbols are derived from SubjectID by the selected source adapter and are
// deliberately not persisted in this type.
type DatasetSubject struct {
	SubjectID   string
	SubjectName string
	Status      string
}

// TaskSpec is an adapter output before persistence fields are added.
type TaskSpec struct {
	RouteID    string
	Provider   string
	SourceID   string
	MarketType string
	DataType   string
	DatasetID  string
	SubjectID  string
	Frequency  string
	Params     map[string]any
}

// TaskInstance is one provider request execution. Task/Dataset ownership lives in WriteTarget.
type TaskInstance struct {
	ID      int    `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID string `gorm:"column:c_space_id"`
	// InstanceID is the stable executable identity for one subject/frequency.
	InstanceID     string     `gorm:"column:c_instance_id"`
	RunID          string     `gorm:"column:c_run_id"`
	RequestKey     string     `gorm:"column:c_request_key"`
	Provider       string     `gorm:"column:c_provider"`
	ProviderSymbol string     `gorm:"column:c_provider_symbol"`
	MarketType     string     `gorm:"column:c_market_type"`
	DataType       string     `gorm:"column:c_data_type"`
	SubjectID      string     `gorm:"column:c_subject_id"`
	Frequency      string     `gorm:"column:c_frequency"`
	TargetDataTime *time.Time `gorm:"column:c_target_data_time"`
	SourceID       string     `gorm:"column:c_source_id"`
	SeriesTag      string     `gorm:"column:c_series_tag"`
	FunctionName   string     `gorm:"column:c_function_name"`
	LastExecStatus int        `gorm:"column:c_last_exec_status"`
	TaskParams     string     `gorm:"column:c_task_params"`
	ExecuteAt      time.Time  `gorm:"-"`
	LastExecTime   *time.Time `gorm:"column:c_last_exec_time"`
	Result         string     `gorm:"column:c_result"`
	IsDeleted      bool       `gorm:"column:c_is_deleted"`
	CreateTime     time.Time  `gorm:"column:c_ctime"`
	ModifyTime     time.Time  `gorm:"column:c_mtime"`
}

// WriteTarget is the task-specific destination of a shared TaskInstance.
// Fetch status belongs to TaskInstance; write/retry status belongs here.
type WriteTarget struct {
	ID             string     `gorm:"column:c_write_target_id;primaryKey" json:"write_target_id"`
	SpaceID        string     `gorm:"column:c_space_id" json:"space_id"`
	InstanceID     string     `gorm:"column:c_instance_id" json:"instance_id"`
	TaskID         string     `gorm:"column:c_task_id" json:"task_id"`
	DatasetID      string     `gorm:"column:c_dataset_id" json:"dataset_id"`
	ViewID         string     `gorm:"column:c_view_id" json:"view_id,omitempty"`
	OutputFields   string     `gorm:"column:c_output_fields_json" json:"output_fields_json,omitempty"`
	SeriesIndex    uint32     `gorm:"column:c_series_index" json:"series_index"`
	SeriesHash     string     `gorm:"column:c_series_hash" json:"series_hash"`
	ExpectedCount  uint32     `gorm:"column:c_expected_count" json:"expected_count"`
	Frequency      string     `gorm:"-" json:"frequency"`
	TargetDataTime string     `gorm:"-" json:"target_data_time"`
	Status         string     `gorm:"column:c_status" json:"status"`
	Attempt        int        `gorm:"column:c_attempt" json:"attempt"`
	LastError      string     `gorm:"column:c_last_error" json:"last_error,omitempty"`
	NextRetryAt    *time.Time `gorm:"column:c_next_retry_at" json:"next_retry_at,omitempty"`
}

func (w *WriteTarget) TableName() string { return "t_collector_instance_write_targets" }

// TableName returns the Collector task instance table.
func (i *TaskInstance) TableName() string {
	return "t_collector_task_instances"
}

// StableResampleTaskID includes the selected source series because a target
// subject can otherwise be backed by multiple venue streams.
func StableResampleTaskID(spaceID string, collectionTaskID string, spec TaskSpec, sourceSeriesTag string) string {
	parts := []string{
		spaceID,
		collectionTaskID,
		spec.Provider,
		spec.MarketType,
		spec.DataType,
		spec.DatasetID,
		spec.SubjectID,
		spec.Frequency,
		strings.TrimSpace(sourceSeriesTag),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])[:32]
}
