// Package resample defines the Collector-local K-line resample job metadata.
package resample

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
)

// NewJobDefinition returns the local K-line resample job definition.
func NewJobDefinition() jobdef.JobDefinition {
	dataSources := jobdef.OptionList{Options: []jobdef.Option{{Value: "binance", Label: "币安"}, {Value: "moox", Label: "MooX"}}}
	return jobdef.JobDefinition{
		ID:                3,
		DataType:          "kline_resample",
		TypeName:          "K线重采样",
		TypeDesc:          "从已落盘K线生成自定义周期K线",
		DataSourceOptions: dataSources,
		SortOrder:         3,
		Version:           1,
		ExecutionMode:     jobdef.ExecutionModeCollectorLocal,
		Fields: []jobdef.FieldDefinition{
			{ID: 5, DataType: "kline_resample", FieldKey: "source_dataset_id", FieldName: "源Dataset", FieldType: "dataset_select", IsRequired: true, DataSourceOptions: dataSources, SortOrder: 1},
			{ID: 6, DataType: "kline_resample", FieldKey: "source_frequency", FieldName: "源周期", FieldType: "frequency_select", IsRequired: true, DataSourceOptions: dataSources, SortOrder: 2},
			{ID: 7, DataType: "kline_resample", FieldKey: "source_series_tag", FieldName: "数据序列", FieldType: "text", IsRequired: true, DataSourceOptions: dataSources, SortOrder: 3},
			{ID: 8, DataType: "kline_resample", FieldKey: "target_dataset_id", FieldName: "目标Dataset", FieldType: "text", IsRequired: true, DataSourceOptions: dataSources, SortOrder: 4},
			{ID: 9, DataType: "kline_resample", FieldKey: "target_frequency", FieldName: "目标周期", FieldType: "text", IsRequired: true, DataSourceOptions: dataSources, SortOrder: 5},
			{ID: 10, DataType: "kline_resample", FieldKey: "settle_delay_ms", FieldName: "收盘等待毫秒", FieldType: "number", DefaultValue: float64(10000), DataSourceOptions: dataSources, SortOrder: 6},
		},
		Supports: []jobdef.Support{
			{Exchange: "binance", Market: "spot", DataType: "kline_resample", SourceKind: "dataset_subjects"},
			{Exchange: "binance", Market: "swap", DataType: "kline_resample", SourceKind: "dataset_subjects"},
			{Exchange: "moox", Market: "spot", DataType: "kline_resample", SourceKind: "dataset_subjects"},
			{Exchange: "moox", Market: "swap", DataType: "kline_resample", SourceKind: "dataset_subjects"},
		},
		Planner: buildTaskSpecs,
	}
}

func buildTaskSpecs(_ context.Context, _ *domain.TaskRule, params *domain.CollectParams, subjects []domain.DatasetSubject) ([]domain.TaskSpec, error) {
	if params == nil || params.Collector.DataType != "kline_resample" {
		return nil, fmt.Errorf("kline resample params are required")
	}
	specs := make([]domain.TaskSpec, 0, len(subjects))
	for _, subject := range subjects {
		if strings.TrimSpace(subject.SubjectID) == "" || (strings.TrimSpace(subject.Status) != "" && !strings.EqualFold(subject.Status, "active")) {
			continue
		}
		specs = append(specs, domain.TaskSpec{
			Provider: params.Provider, MarketType: params.MarketType, DataType: "kline_resample",
			DatasetID: params.TargetDatasetID, SubjectID: strings.TrimSpace(subject.SubjectID), Frequency: params.TargetFrequency,
			Params: map[string]any{
				"source_dataset_id": params.SourceDatasetID,
				"source_frequency":  params.SourceFrequency,
				"source_series_tag": params.SourceSeriesTag,
				"target_dataset_id": params.TargetDatasetID,
				"target_frequency":  params.TargetFrequency,
				"alignment":         params.Alignment,
				"settle_delay_ms":   params.SettleDelayMS,
			},
		})
	}
	return specs, nil
}
