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
	ID             string     `json:"write_target_id"`
	SpaceID        string     `json:"space_id"`
	InstanceID     string     `json:"instance_id"`
	TaskID         string     `json:"task_id"`
	DatasetID      string     `json:"dataset_id"`
	ViewID         string     `json:"view_id,omitempty"`
	OutputFields   string     `json:"output_fields_json,omitempty"`
	SeriesIndex    uint32     `json:"series_index"`
	SeriesHash     string     `json:"series_hash"`
	ExpectedCount  uint32     `json:"expected_count"`
	Frequency      string     `json:"frequency"`
	TargetDataTime string     `json:"target_data_time"`
	Status         string     `json:"status"`
	Attempt        int        `json:"attempt"`
	LastError      string     `json:"last_error,omitempty"`
	NextRetryAt    *time.Time `json:"next_retry_at,omitempty"`
}
