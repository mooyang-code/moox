// Package jobs contains collector JobItem definitions and handler registration constants.
package jobs

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/kline"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/symbol"
)

const (
	JobTypeCollectKline  = kline.JobType
	JobTypeCollectSymbol = symbol.JobType
)

// JobDefinition describes one collector job type.
type JobDefinition = jobdef.JobDefinition

// FieldDefinition describes one rule form field for a collector data type.
type FieldDefinition = jobdef.FieldDefinition

var jobDefinitions = []JobDefinition{
	kline.NewJobDefinition(),
	symbol.NewJobDefinition(),
}

// ListJobDefinitions returns collector job definitions in UI sort order.
func ListJobDefinitions() []JobDefinition {
	out := make([]JobDefinition, len(jobDefinitions))
	copy(out, jobDefinitions)
	return out
}

// SupportedJobTypes returns stable queue routing types handled by Collector SCF.
func SupportedJobTypes() []string {
	out := make([]string, 0, len(jobDefinitions))
	for _, definition := range jobDefinitions {
		out = append(out, definition.JobType)
	}
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

// BuildTaskSpecs dispatches atomic task planning to the matching job definition.
func BuildTaskSpecs(ctx context.Context, rule *domain.TaskRule, params *domain.CollectParams, subjects []domain.DatasetSubject) ([]domain.TaskSpec, error) {
	if params == nil {
		return nil, fmt.Errorf("collect params are required")
	}
	for _, definition := range jobDefinitions {
		if definition.Matches(params) {
			return definition.Planner(ctx, rule, params, subjects)
		}
	}
	return nil, fmt.Errorf("collector planner not found: %s:%s:%s", params.Collector.Exchange, params.Collector.Market, params.Collector.DataType)
}
