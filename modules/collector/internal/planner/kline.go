package planner

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	klinejob "github.com/mooyang-code/moox/modules/collector/internal/jobs/kline"
	symboljob "github.com/mooyang-code/moox/modules/collector/internal/jobs/symbol"
)

func buildTaskSpecs(ctx context.Context, rule *domain.TaskRule, params *domain.CollectParams, subjects []domain.DatasetSubject) ([]domain.TaskSpec, error) {
	_ = ctx
	switch strings.ToLower(params.Collector.Exchange) + ":" + strings.ToLower(params.Collector.Market) + ":" + strings.ToLower(params.Collector.DataType) {
	case "binance:spot:kline":
		if params.Source.Kind != "dataset_subjects" {
			return nil, fmt.Errorf("kline planner requires dataset_subjects source, got %s", params.Source.Kind)
		}
		return klinejob.BuildTaskSpecs(params, subjects), nil
	case "binance:swap:kline":
		if params.Source.Kind != "dataset_subjects" {
			return nil, fmt.Errorf("kline planner requires dataset_subjects source, got %s", params.Source.Kind)
		}
		return klinejob.BuildTaskSpecs(params, subjects), nil
	case "binance:spot:symbol", "binance:swap:symbol":
		if params.Source.Kind != "" && params.Source.Kind != "none" {
			return nil, fmt.Errorf("symbol planner requires none source, got %s", params.Source.Kind)
		}
		return symboljob.BuildTaskSpecs(params), nil
	default:
		return nil, fmt.Errorf("collector planner not found: %s:%s:%s", params.Collector.Exchange, params.Collector.Market, params.Collector.DataType)
	}
}
