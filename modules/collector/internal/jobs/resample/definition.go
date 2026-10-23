// Package resample defines the Collector-local K-line resample job metadata.
package resample

import (
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
)

// NewJobDefinition returns the local K-line resample job definition.
func NewJobDefinition() jobdef.JobDefinition {
	dataSources := jobdef.OptionList{Options: []jobdef.Option{{Value: "binance", Label: "币安"}, {Value: "moox", Label: "MooX"}}}
	return jobdef.JobDefinition{
		ID:                3,
		DataType:          "kline_resample",
		TypeName:          "K线重采样",
		TypeDesc:          "从已落盘K线生成自定义周期K线",
		DataSourceOptions: dataSources,
		SortOrder:         3,
		Version:           1,
		ExecutionMode:     jobdef.ExecutionModeCollectorLocal,
		Fields: []jobdef.FieldDefinition{
			{ID: 11, DataType: "kline_resample", FieldKey: "source_task_id", FieldName: "源采集任务", FieldType: "text", IsRequired: true, DataSourceOptions: dataSources, SortOrder: 0},
			{ID: 5, DataType: "kline_resample", FieldKey: "source_dataset_id", FieldName: "源Dataset", FieldType: "dataset_select", DataSourceOptions: dataSources, SortOrder: 1},
			{ID: 6, DataType: "kline_resample", FieldKey: "source_frequency", FieldName: "源周期", FieldType: "frequency_select", DataSourceOptions: dataSources, SortOrder: 2},
			{ID: 7, DataType: "kline_resample", FieldKey: "source_series_tag", FieldName: "数据序列", FieldType: "text", DataSourceOptions: dataSources, SortOrder: 3},
			{ID: 8, DataType: "kline_resample", FieldKey: "target_dataset_id", FieldName: "目标Dataset", FieldType: "text", DataSourceOptions: dataSources, SortOrder: 4},
			{ID: 9, DataType: "kline_resample", FieldKey: "target_frequency", FieldName: "目标周期", FieldType: "text", IsRequired: true, DataSourceOptions: dataSources, SortOrder: 5},
		},
		Supports: []jobdef.Support{
			{Exchange: "binance", Market: "spot", DataType: "kline_resample", SourceKind: "dataset"},
			{Exchange: "binance", Market: "swap", DataType: "kline_resample", SourceKind: "dataset"},
			{Exchange: "moox", Market: "spot", DataType: "kline_resample", SourceKind: "dataset"},
			{Exchange: "moox", Market: "swap", DataType: "kline_resample", SourceKind: "dataset"},
		},
	}
}
