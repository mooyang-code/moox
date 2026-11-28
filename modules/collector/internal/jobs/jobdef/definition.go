// Package jobdef 定义采集任务类型的元数据。
package jobdef

// Option 表示任务表单中的下拉选项。
type Option struct {
	Value string
	Label string
}

// JobDefinition 描述一种采集任务类型。参数的合法组合不在这里声明：
// K 线任务按行情源目录校验，合成任务由 resample.ValidateTaskParams 校验。
type JobDefinition struct {
	DataType         string
	TypeName         string
	TypeDesc         string
	FrequencyOptions []Option
}
