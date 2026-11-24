// Package jobdef 定义采集任务类型的元数据（任务表单与执行方式）。
package jobdef

// Option is a UI option value exposed through CollectMgr data type configs.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// OptionList is the JSON shape expected by the admin frontend.
type OptionList struct {
	Options []Option `json:"options"`
}

// ExecutionMode separates queue-backed exchange jobs from Collector-local jobs.
type ExecutionMode string

const (
	ExecutionModeCloudInvoke    ExecutionMode = "cloud_invoke"
	ExecutionModeCollectorLocal ExecutionMode = "collector_local"
)

func (m ExecutionMode) Valid() bool {
	return m == ExecutionModeCloudInvoke || m == ExecutionModeCollectorLocal
}

// FieldDefinition describes one rule form field for a collector data type.
type FieldDefinition struct {
	ID                int32
	DataType          string
	FieldKey          string
	FieldName         string
	FieldType         string
	IsRequired        bool
	DefaultValue      any
	FieldOptionsJSON  string
	DataSourceOptions OptionList
	SortOrder         int32
}

// JobDefinition 描述一种采集任务类型。参数的合法组合不在这里声明：K 线任务按行情源目录校验，
// 合成任务由 resample.ValidateTaskParams 校验。
type JobDefinition struct {
	ID                int32
	DataType          string
	TypeName          string
	TypeDesc          string
	DataSourceOptions OptionList
	SortOrder         int32
	Version           int32
	Fields            []FieldDefinition
	ExecutionMode     ExecutionMode
}
