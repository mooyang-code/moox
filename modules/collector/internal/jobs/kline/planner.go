package kline

import (
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
)

// BuildTaskSpecs expands dataset subjects and intervals into K-line atomic tasks.
func BuildTaskSpecs(params *domain.CollectParams, subjects []domain.DatasetSubject) []domain.TaskSpec {
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
					"job_type":          params.Target.JobType,
					"code_package_id":   params.Target.CodePackageID,
					"schedule_interval": params.Schedule.Interval,
					"schedule_timezone": params.Schedule.Timezone,
					"live":              params.Collector.Live,
				},
			})
		}
	}
	return specs
}
