package symbol

import "github.com/mooyang-code/moox/modules/collector/internal/domain"

// BuildTaskSpecs creates the single instrument-list atomic task for a rule.
func BuildTaskSpecs(params *domain.CollectParams) []domain.TaskSpec {
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
				"schedule_interval": params.Schedule.Interval,
			},
		},
	}
}
