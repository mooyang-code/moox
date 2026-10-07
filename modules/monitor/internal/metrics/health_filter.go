package metrics

import "github.com/mooyang-code/moox/packages/report"

// ViewMetricScope identifies a View whose output summary a configured K-line
// freshness check reads.
type ViewMetricScope struct {
	SpaceID   string
	ViewID    string
	DatasetID string
	Frequency string
}

// IsHealthMetric reports whether Monitor consumes a metric family; the
// definition is shared with the reporter, which sends nothing else.
func IsHealthMetric(name string) bool {
	return report.IsMonitoredMetric(name)
}

func FilterHealthSamples(samples []Sample) []Sample {
	if len(samples) == 0 {
		return samples
	}
	out := make([]Sample, 0, len(samples))
	for _, sample := range samples {
		if IsHealthMetric(sample.MetricName) {
			out = append(out, sample)
		}
	}
	return out
}
