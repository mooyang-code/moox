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
	JobTypeCollectKline  = "collect.kline"
	JobTypeCollectSymbol = "collect.symbol"
)

// Definition describes one collector data type.
type Definition = jobdef.Definition

// FieldDefinition describes one rule form field for a collector data type.
type FieldDefinition = jobdef.FieldDefinition

var definitions = []Definition{
	kline.Definition(JobTypeCollectKline),
	symbol.Definition(JobTypeCollectSymbol),
}

// ListDefinitions returns collector data type definitions in UI sort order.
func ListDefinitions() []Definition {
	out := make([]Definition, len(definitions))
	copy(out, definitions)
	return out
}

// DefinitionByDataType returns one collector data type definition.
func DefinitionByDataType(dataType string) (Definition, bool) {
	dataType = strings.ToLower(strings.TrimSpace(dataType))
	for _, definition := range definitions {
		if definition.DataType == dataType {
			return definition, true
		}
	}
	return Definition{}, false
}

// BuildTaskSpecs dispatches atomic task planning to the matching job definition.
func BuildTaskSpecs(ctx context.Context, rule *domain.TaskRule, params *domain.CollectParams, subjects []domain.DatasetSubject) ([]domain.TaskSpec, error) {
	if params == nil {
		return nil, fmt.Errorf("collect params are required")
	}
	for _, definition := range definitions {
		if definition.Matches(params) {
			return definition.Planner(ctx, rule, params, subjects)
		}
	}
	return nil, fmt.Errorf("collector planner not found: %s:%s:%s", params.Collector.Exchange, params.Collector.Market, params.Collector.DataType)
}
