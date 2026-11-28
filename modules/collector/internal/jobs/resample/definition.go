// Package resample 定义 Collector 本地 K 线重采样任务的元数据。
package resample

import (
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
)

// NewJobDefinition 返回本地 K 线重采样任务的定义。
func NewJobDefinition() jobdef.JobDefinition {
	return jobdef.JobDefinition{
		DataType: "kline_resample",
		TypeName: "K线重采样",
		TypeDesc: "从已落盘K线生成自定义周期K线",
	}
}
