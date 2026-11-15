package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

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
	ID          int    `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID     string `gorm:"column:c_space_id"`
	TaskID      string `gorm:"column:c_task_id"`
	TaskName    string `gorm:"column:c_task_name"`
	Description string `gorm:"column:c_description"`
	DataType    string `gorm:"column:c_data_type"`
	// Ingest 是 K 线任务的采集方式（IngestStreaming、IngestSCF、IngestLocal），其他任务为空。
	Ingest          string                     `gorm:"column:c_ingest"`
	TagIDs          []string                   `gorm:"-"`
	DefinitionHash  string                     `gorm:"column:c_definition_hash"`
	CollectParams   string                     `gorm:"column:c_collect_params"`
	Enabled         bool                       `gorm:"column:c_enabled"`
	Creator         string                     `gorm:"column:c_creator"`
	PrepareState    CollectionTaskPrepareState `gorm:"column:c_prepare_state"`
	LastError       string                     `gorm:"column:c_last_error"`
	ResultDatasetID string                     `gorm:"column:c_result_dataset_id"`
	ResultViewID    string                     `gorm:"column:c_result_view_id"`
	CreateTime      time.Time                  `gorm:"column:c_ctime"`
	ModifyTime      time.Time                  `gorm:"column:c_mtime"`
}

// DataTypeKline 是直接采集 K 线的任务类型；只有它有采集方式。
const DataTypeKline = "kline"

// 采集方式，一列三值。没有“推送加云函数补采”这个组合。
// 推送的标识符不以 Stream 结尾：该后缀会被 check-event-topology-declarations 当作事件拓扑命名，取值仍是 "stream"。
const (
	// IngestStreaming：collector-stream 接收推送，补采走它同进程的本地执行端。
	IngestStreaming = "stream"
	// IngestSCF：云函数定时拉取，补采走云函数。
	IngestSCF = "scf"
	// IngestLocal：collector-stream 本地定时拉取，补采也走本地执行端。
	IngestLocal = "local"
)

// ValidateIngest 检查采集方式的取值。
func ValidateIngest(ingest string) error {
	switch ingest {
	case IngestStreaming, IngestSCF, IngestLocal:
		return nil
	}
	return fmt.Errorf("采集方式必须是 stream、scf 或 local，收到 %q", ingest)
}

// NormalizeCollectionTaskName removes the presentation whitespace from a task
// name before it is validated or persisted.
func NormalizeCollectionTaskName(name string) string {
	return strings.TrimSpace(name)
}

// ValidateCollectionTaskName validates the user-visible task name. The length
// is measured in Unicode code points rather than bytes.
func ValidateCollectionTaskName(name string) error {
	name = NormalizeCollectionTaskName(name)
	if name == "" {
		return fmt.Errorf("task_name is required")
	}
	length := utf8.RuneCountInString(name)
	if length > 80 {
		return fmt.Errorf("task_name must contain 1-80 Unicode characters")
	}
	return nil
}

// TableName returns the Collector collection-task table.
func (r *CollectionTask) TableName() string {
	return "t_collector_tasks"
}
