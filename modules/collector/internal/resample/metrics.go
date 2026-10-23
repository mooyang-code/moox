package resample

import "github.com/prometheus/client_golang/prometheus"

// Metrics 是合成的有界运行指标。
type Metrics struct {
	Buckets   prometheus.Counter
	Writes    prometheus.Counter
	Skipped   prometheus.Counter
	Errors    prometheus.Counter
	Fallbacks prometheus.Counter
	Events    prometheus.Counter
}

func NewMetrics(registerer prometheus.Registerer) *Metrics {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	m := &Metrics{
		Buckets:   prometheus.NewCounter(prometheus.CounterOpts{Name: "moox_collector_kline_resample_buckets_total", Help: "Resampled target buckets written."}),
		Writes:    prometheus.NewCounter(prometheus.CounterOpts{Name: "moox_collector_kline_resample_writes_total", Help: "Written local K-line resample rows."}),
		Skipped:   prometheus.NewCounter(prometheus.CounterOpts{Name: "moox_collector_kline_resample_skipped_total", Help: "Subjects skipped because the source window was incomplete."}),
		Errors:    prometheus.NewCounter(prometheus.CounterOpts{Name: "moox_collector_kline_resample_errors_total", Help: "Failed resample bucket executions."}),
		Fallbacks: prometheus.NewCounter(prometheus.CounterOpts{Name: "moox_collector_kline_resample_fallback_buckets_total", Help: "Buckets repaired by the fallback check."}),
		Events:    prometheus.NewCounter(prometheus.CounterOpts{Name: "moox_collector_kline_resample_events_total", Help: "Source period events handled by the resample consumer."}),
	}
	for _, collector := range []prometheus.Collector{m.Buckets, m.Writes, m.Skipped, m.Errors, m.Fallbacks, m.Events} {
		_ = registerer.Register(collector)
	}
	return m
}
