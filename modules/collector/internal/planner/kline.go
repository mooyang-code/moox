package planner

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
)

func buildTaskSpecs(ctx context.Context, rule *domain.TaskRule, params *domain.CollectParams, subjects []domain.DatasetSubject) ([]domain.TaskSpec, error) {
	_ = ctx
	switch strings.ToLower(params.Collector.Exchange) + ":" + strings.ToLower(params.Collector.Market) + ":" + strings.ToLower(params.Collector.DataType) {
	case "binance:spot:kline":
		if params.Source.Kind != "dataset_subjects" {
			return nil, fmt.Errorf("kline planner requires dataset_subjects source, got %s", params.Source.Kind)
		}
		return buildKlineTaskSpecs(rule, params, subjects), nil
	case "binance:swap:kline":
		if params.Source.Kind != "dataset_subjects" {
			return nil, fmt.Errorf("kline planner requires dataset_subjects source, got %s", params.Source.Kind)
		}
		return buildKlineTaskSpecs(rule, params, subjects), nil
	case "binance:spot:symbol", "binance:swap:symbol":
		if params.Source.Kind != "" && params.Source.Kind != "none" {
			return nil, fmt.Errorf("symbol planner requires none source, got %s", params.Source.Kind)
		}
		return buildSymbolTaskSpecs(rule, params), nil
	default:
		return nil, fmt.Errorf("collector planner not found: %s:%s:%s", params.Collector.Exchange, params.Collector.Market, params.Collector.DataType)
	}
}

func buildKlineTaskSpecs(rule *domain.TaskRule, params *domain.CollectParams, subjects []domain.DatasetSubject) []domain.TaskSpec {
	_ = rule
	intervals := params.Collector.Intervals
	if len(intervals) == 0 {
		intervals = []string{"1m"}
	}
	specs := make([]domain.TaskSpec, 0, len(subjects)*len(intervals))
	for _, subject := range subjects {
		symbol := strings.TrimSpace(subject.ExternalSymbol)
		if symbol == "" {
			symbol = strings.TrimSpace(subject.SubjectID)
		}
		if symbol == "" {
			continue
		}
		for _, interval := range intervals {
			if strings.TrimSpace(interval) == "" {
				continue
			}
			specs = append(specs, domain.TaskSpec{
				Exchange:  params.Collector.Exchange,
				Market:    params.Collector.Market,
				DataType:  params.Collector.DataType,
				DatasetID: params.Target.DatasetID,
				SubjectID: subject.SubjectID,
				Symbol:    symbol,
				Interval:  interval,
				Params: map[string]any{
					"exchange":          params.Collector.Exchange,
					"market":            params.Collector.Market,
					"data_type":         params.Collector.DataType,
					"dataset_id":        params.Target.DatasetID,
					"subject_id":        subject.SubjectID,
					"symbol":            symbol,
					"interval":          interval,
					"workload_type":     params.Target.WorkloadType,
					"deployment_id":     params.Target.DeploymentID,
					"schedule_interval": params.Schedule.Interval,
					"schedule_timezone": params.Schedule.Timezone,
				},
			})
		}
	}
	return specs
}

func buildSymbolTaskSpecs(rule *domain.TaskRule, params *domain.CollectParams) []domain.TaskSpec {
	_ = rule
	return []domain.TaskSpec{
		{
			Exchange:  params.Collector.Exchange,
			Market:    params.Collector.Market,
			DataType:  params.Collector.DataType,
			DatasetID: params.Target.DatasetID,
			Params: map[string]any{
				"exchange":          params.Collector.Exchange,
				"market":            params.Collector.Market,
				"data_type":         params.Collector.DataType,
				"dataset_id":        params.Target.DatasetID,
				"workload_type":     params.Target.WorkloadType,
				"deployment_id":     params.Target.DeploymentID,
				"schedule_interval": params.Schedule.Interval,
				"schedule_timezone": params.Schedule.Timezone,
			},
		},
	}
}
