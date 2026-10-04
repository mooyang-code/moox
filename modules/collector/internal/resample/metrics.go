package resample

import "github.com/prometheus/client_golang/prometheus"

// Metrics exposes bounded operational signals for local resample work.
type Metrics struct {
	Claims  prometheus.Counter
	Writes  prometheus.Counter
	Retries prometheus.Counter
	Errors  prometheus.Counter
}

func NewMetrics(registerer prometheus.Registerer) *Metrics {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	m := &Metrics{
		Claims:  prometheus.NewCounter(prometheus.CounterOpts{Name: "moox_collector_kline_resample_claims_total", Help: "Claimed local K-line resample buckets."}),
		Writes:  prometheus.NewCounter(prometheus.CounterOpts{Name: "moox_collector_kline_resample_writes_total", Help: "Written local K-line resample rows."}),
		Retries: prometheus.NewCounter(prometheus.CounterOpts{Name: "moox_collector_kline_resample_retries_total", Help: "Source-window retries for local K-line resample."}),
		Errors:  prometheus.NewCounter(prometheus.CounterOpts{Name: "moox_collector_kline_resample_errors_total", Help: "Failed local K-line resample buckets."}),
	}
	for _, collector := range []prometheus.Collector{m.Claims, m.Writes, m.Retries, m.Errors} {
		_ = registerer.Register(collector)
	}
	return m
}
