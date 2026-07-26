package symbol

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
)

// JobType is the queue routing type for symbol collection.
const JobType = "collect.symbol"

// NewJobDefinition returns the symbol collector job definition.
func NewJobDefinition() jobdef.JobDefinition {
	dataSources := jobdef.OptionList{Options: []jobdef.Option{{Value: "binance", Label: "币安"}}}
	return jobdef.JobDefinition{
		ID:                2,
		JobType:           JobType,
		DataType:          "symbol",
		TypeName:          "标的",
		TypeDesc:          "交易所标的元数据同步",
		DataSourceOptions: dataSources,
		SortOrder:         2,
		Version:           1,
		Fields: []jobdef.FieldDefinition{
			{
				ID:                4,
				DataType:          "symbol",
				FieldKey:          "inst_type",
				FieldName:         "产品类型",
				FieldType:         "select",
				IsRequired:        true,
				DefaultValue:      "SPOT",
				FieldOptionsJSON:  `{"options":[{"value":"SPOT","label":"现货"},{"value":"SWAP","label":"永续合约"}]}`,
				DataSourceOptions: dataSources,
				SortOrder:         1,
			},
		},
		Supports: []jobdef.Support{
			{Exchange: "binance", Market: "spot", DataType: "symbol", SourceKind: "none"},
			{Exchange: "binance", Market: "swap", DataType: "symbol", SourceKind: "none"},
		},
		Planner: func(ctx context.Context, rule *domain.TaskRule, params *domain.CollectParams, subjects []domain.DatasetSubject) ([]domain.TaskSpec, error) {
			_ = ctx
			_ = rule
			_ = subjects
			if params.Source.Kind != "" && params.Source.Kind != "none" {
				return nil, fmt.Errorf("symbol planner requires none source, got %s", params.Source.Kind)
			}
			return BuildTaskSpecs(params), nil
		},
	}
}
