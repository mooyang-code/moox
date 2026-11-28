// Package kline 定义 K 线采集任务的元数据。
package kline

import (
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
)

// NewJobDefinition 返回 K 线采集任务的定义。能采哪些频率不在这里限定：
// 建任务时按行情源目录与路由文件校验（marketwiring.CheckKlineFrequency）。
func NewJobDefinition() jobdef.JobDefinition {
	return jobdef.JobDefinition{
		DataType: "kline",
		TypeName: "K线",
		TypeDesc: "交易所K线行情采集",
		FrequencyOptions: []jobdef.Option{
			{Value: "1m", Label: "1分钟"},
			{Value: "5m", Label: "5分钟"},
			{Value: "15m", Label: "15分钟"},
			{Value: "30m", Label: "30分钟"},
			{Value: "1h", Label: "1小时"},
			{Value: "1d", Label: "1天"},
			{Value: "1w", Label: "1周"},
			{Value: "1mo", Label: "1月"},
		},
	}
}
