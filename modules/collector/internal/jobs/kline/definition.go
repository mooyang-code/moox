// Package kline 定义 K 线采集任务的表单元数据。
package kline

import (
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
)

// NewJobDefinition 返回 K 线采集任务的定义。能采哪些频率不在这里写：建任务时按行情源目录与路由文件校验
// （marketwiring.CheckKlineFrequency）。
func NewJobDefinition() jobdef.JobDefinition {
	dataSources := jobdef.OptionList{Options: []jobdef.Option{{Value: "binance", Label: "币安"}}}
	return jobdef.JobDefinition{
		ID:                1,
		DataType:          "kline",
		TypeName:          "K线",
		TypeDesc:          "交易所K线行情采集",
		DataSourceOptions: dataSources,
		SortOrder:         1,
		Version:           1,
		ExecutionMode:     jobdef.ExecutionModeCloudInvoke,
		Fields: []jobdef.FieldDefinition{
			{
				ID:                3,
				DataType:          "kline",
				FieldKey:          "frequency",
				FieldName:         "K线周期",
				FieldType:         "select",
				IsRequired:        true,
				DefaultValue:      "1m",
				FieldOptionsJSON:  `{"options":[{"value":"1m","label":"1分钟"},{"value":"5m","label":"5分钟"},{"value":"15m","label":"15分钟"},{"value":"30m","label":"30分钟"},{"value":"1h","label":"1小时"},{"value":"1d","label":"1天"},{"value":"1w","label":"1周"},{"value":"1mo","label":"1月"}]}`,
				DataSourceOptions: dataSources,
				SortOrder:         1,
			},
		},
	}
}
