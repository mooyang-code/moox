// Package jobdef defines collector job metadata used by control-plane RPCs and planners.
package jobdef

import (
	"context"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
)

// Planner builds atomic task specs for one collector job definition.
type Planner func(ctx context.Context, rule *domain.TaskRule, params *domain.CollectParams, subjects []domain.DatasetSubject) ([]domain.TaskSpec, error)

// Option is a UI option value exposed through CollectMgr data type configs.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// OptionList is the JSON shape expected by the admin frontend.
type OptionList struct {
	Options []Option `json:"options"`
}

// Support describes an exchange/market/data_type tuple handled by a definition.
type Support struct {
	Exchange   string
	Market     string
	DataType   string
	SourceKind string
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

// Definition describes one collector data type.
type Definition struct {
	ID                int32
	DataType          string
	TypeName          string
	TypeDesc          string
	DataSourceOptions OptionList
	SortOrder         int32
	Version           int32
	Fields            []FieldDefinition
	Supports          []Support
	Planner           Planner
}

// Matches returns whether params can be planned by this definition.
func (d Definition) Matches(params *domain.CollectParams) bool {
	if params == nil {
		return false
	}
	for _, support := range d.Supports {
		if equalFoldTrim(support.Exchange, params.Collector.Exchange) &&
			equalFoldTrim(support.Market, params.Collector.Market) &&
			equalFoldTrim(support.DataType, params.Collector.DataType) {
			return true
		}
	}
	return false
}

func equalFoldTrim(left string, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}
