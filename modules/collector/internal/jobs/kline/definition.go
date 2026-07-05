package kline

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
)

// Definition returns the K-line collector job definition.
func Definition(jobType string) jobdef.Definition {
	dataSources := jobdef.OptionList{Options: []jobdef.Option{{Value: "binance", Label: "币安"}}}
	return jobdef.Definition{
		ID:                1,
		DataType:          "kline",
		TypeName:          "K线",
		TypeDesc:          "交易所K线行情采集",
		DataSourceOptions: dataSources,
		SortOrder:         1,
		Version:           1,
		Fields: []jobdef.FieldDefinition{
			{
				ID:                1,
				DataType:          "kline",
				FieldKey:          "inst_type",
				FieldName:         "产品类型",
				FieldType:         "select",
				IsRequired:        true,
				DefaultValue:      "SPOT",
				FieldOptionsJSON:  `{"options":[{"value":"SPOT","label":"现货"},{"value":"SWAP","label":"永续合约"}]}`,
				DataSourceOptions: dataSources,
				SortOrder:         1,
			},
			{
				ID:                2,
				DataType:          "kline",
				FieldKey:          "objects",
				FieldName:         "交易标的",
				FieldType:         "multi_input",
				IsRequired:        true,
				DefaultValue:      []any{"*"},
				FieldOptionsJSON:  `{"placeholder":"输入交易标的，例如 BTCUSDT；选择全部时使用 *"}`,
				DataSourceOptions: dataSources,
				SortOrder:         2,
			},
			{
				ID:                3,
				DataType:          "kline",
				FieldKey:          "intervals",
				FieldName:         "K线周期",
				FieldType:         "multi_select",
				IsRequired:        true,
				DefaultValue:      []any{"1m"},
				FieldOptionsJSON:  `{"options":[{"value":"1m","label":"1分钟"},{"value":"3m","label":"3分钟"},{"value":"5m","label":"5分钟"},{"value":"15m","label":"15分钟"},{"value":"30m","label":"30分钟"},{"value":"1h","label":"1小时"},{"value":"2h","label":"2小时"},{"value":"4h","label":"4小时"},{"value":"6h","label":"6小时"},{"value":"12h","label":"12小时"},{"value":"1d","label":"1天"},{"value":"1w","label":"1周"},{"value":"1M","label":"1月"}]}`,
				DataSourceOptions: dataSources,
				SortOrder:         3,
			},
		},
		Supports: []jobdef.Support{
			{Exchange: "binance", Market: "spot", DataType: "kline", SourceKind: "dataset_subjects"},
			{Exchange: "binance", Market: "swap", DataType: "kline", SourceKind: "dataset_subjects"},
		},
		Planner: func(ctx context.Context, rule *domain.TaskRule, params *domain.CollectParams, subjects []domain.DatasetSubject) ([]domain.TaskSpec, error) {
			_ = ctx
			_ = rule
			if params.Source.Kind != "dataset_subjects" {
				return nil, fmt.Errorf("kline planner requires dataset_subjects source, got %s", params.Source.Kind)
			}
			return BuildTaskSpecs(params, subjects), nil
		},
	}
}
