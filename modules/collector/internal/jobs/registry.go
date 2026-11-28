// Package jobs 汇总 Collector 支持的采集任务类型元数据。
package jobs

import (
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/kline"
	resamplejob "github.com/mooyang-code/moox/modules/collector/internal/jobs/resample"
)

// JobDefinition 描述一种采集任务类型。
type JobDefinition = jobdef.JobDefinition

// Option 表示任务表单中的下拉选项。
type Option = jobdef.Option

var jobDefinitions = []JobDefinition{
	kline.NewJobDefinition(),
	resamplejob.NewJobDefinition(),
}

// ListJobDefinitions 按界面展示顺序返回全部任务类型定义。
func ListJobDefinitions() []JobDefinition {
	out := make([]JobDefinition, len(jobDefinitions))
	copy(out, jobDefinitions)
	return out
}

// JobDefinitionByDataType 按类型标识查找任务类型定义。
func JobDefinitionByDataType(dataType string) (JobDefinition, bool) {
	dataType = strings.ToLower(strings.TrimSpace(dataType))
	for _, definition := range jobDefinitions {
		if definition.DataType == dataType {
			return definition, true
		}
	}
	return JobDefinition{}, false
}
