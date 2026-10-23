package domain

import "time"

// Subject is the small metadata projection used when expanding Dataset tags.
type Subject struct {
	SubjectID string
	Name      string
	Status    string
	// Pair 是标的属性里的交易所交易对（币安永续合约），为空表示与交易所 symbol 相同。
	Pair string
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

// WriteTarget 是一次采集请求里某个任务结果数据集的写入目标（序号、hash、总数随请求下发）。
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
