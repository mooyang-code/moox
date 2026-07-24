package tick

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
)

// Definition returns the raw exchange-trade Tick collector job definition.
func Definition(jobType string) jobdef.Definition {
	dataSources := jobdef.OptionList{Options: []jobdef.Option{{Value: "binance", Label: "币安"}}}
	return jobdef.Definition{
		ID:                3,
		DataType:          "tick",
		TypeName:          "原始成交Tick",
		TypeDesc:          "交易所原始成交事件采集",
		DataSourceOptions: dataSources,
		SortOrder:         3,
		Version:           1,
		Fields: []jobdef.FieldDefinition{
			{
				ID: 1, DataType: "tick", FieldKey: "inst_type", FieldName: "产品类型", FieldType: "select", IsRequired: true,
				DefaultValue: "SPOT", FieldOptionsJSON: `{"options":[{"value":"SPOT","label":"现货"},{"value":"SWAP","label":"永续合约"}]}`,
				DataSourceOptions: dataSources, SortOrder: 1,
			},
			{
				ID: 2, DataType: "tick", FieldKey: "objects", FieldName: "交易标的", FieldType: "multi_input", IsRequired: true,
				DefaultValue: []any{"*"}, FieldOptionsJSON: `{"placeholder":"输入交易标的，例如 BTCUSDT；选择全部时使用 *"}`,
				DataSourceOptions: dataSources, SortOrder: 2,
			},
		},
		Supports: []jobdef.Support{
			{Exchange: "binance", Market: "spot", DataType: "tick", SourceKind: "dataset_subjects"},
			{Exchange: "binance", Market: "swap", DataType: "tick", SourceKind: "dataset_subjects"},
		},
		Planner: func(ctx context.Context, rule *domain.TaskRule, params *domain.CollectParams, subjects []domain.DatasetSubject) ([]domain.TaskSpec, error) {
			_ = ctx
			_ = rule
			if params.Source.Kind != "dataset_subjects" {
				return nil, fmt.Errorf("tick planner requires dataset_subjects source, got %s", params.Source.Kind)
			}
			return BuildTaskSpecs(params, subjects), nil
		},
	}
}
