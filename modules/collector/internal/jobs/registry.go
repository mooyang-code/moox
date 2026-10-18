// Package jobs contains Collector job definitions used by planners and the control plane.
package jobs

import (
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/kline"
	resamplejob "github.com/mooyang-code/moox/modules/collector/internal/jobs/resample"
)

// JobDefinition describes one collector job type.
type JobDefinition = jobdef.JobDefinition

// FieldDefinition describes one rule form field for a collector data type.
type FieldDefinition = jobdef.FieldDefinition

type ExecutionMode = jobdef.ExecutionMode

const (
	ExecutionModeCloudInvoke    = jobdef.ExecutionModeCloudInvoke
	ExecutionModeCollectorLocal = jobdef.ExecutionModeCollectorLocal
)

var jobDefinitions = []JobDefinition{
	kline.NewJobDefinition(),
	resamplejob.NewJobDefinition(),
}

// ListJobDefinitions returns collector job definitions in UI sort order.
func ListJobDefinitions() []JobDefinition {
	out := make([]JobDefinition, len(jobDefinitions))
	copy(out, jobDefinitions)
	return out
}

// JobDefinitionByDataType returns one collector job definition.
func JobDefinitionByDataType(dataType string) (JobDefinition, bool) {
	dataType = strings.ToLower(strings.TrimSpace(dataType))
	for _, definition := range jobDefinitions {
		if definition.DataType == dataType {
			return definition, true
		}
	}
	return JobDefinition{}, false
}
