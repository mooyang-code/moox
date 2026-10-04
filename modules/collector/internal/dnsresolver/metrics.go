package dnsresolver

import "github.com/prometheus/client_golang/prometheus"

// Metrics exposes coordinator-level source/freshness diagnostics. The DNS
// route payload remains deliberately unchanged; these values are for the
// Collector process and its health/metrics consumers only.
type Metrics struct {
	Refreshes prometheus.Counter
	Failures  prometheus.Counter
	Routes    prometheus.Gauge
	Age       prometheus.Gauge
	Source    *prometheus.GaugeVec
}

func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	m := &Metrics{
		Refreshes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "moox_collector_dns_resolver_refreshes_total",
			Help: "DNS resolver snapshot refresh attempts.",
		}),
		Failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "moox_collector_dns_resolver_failures_total",
			Help: "DNS resolver refreshes with a remote or local error.",
		}),
		Routes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "moox_collector_dns_resolver_route_count",
			Help: "Number of DNS routes in the current Collector snapshot.",
		}),
		Age: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "moox_collector_dns_resolver_route_age_seconds",
			Help: "Age in seconds of the oldest route in the current snapshot.",
		}),
		Source: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "moox_collector_dns_resolver_source",
			Help: "Current DNS snapshot source (1 for the selected source).",
		}, []string{"source"}),
	}
	for _, collector := range []prometheus.Collector{m.Refreshes, m.Failures, m.Routes, m.Age, m.Source} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Metrics) observe(status Status) {
	if m == nil {
		return
	}
	m.Refreshes.Inc()
	if status.LastErrorCategory != "" {
		m.Failures.Inc()
	}
	m.Routes.Set(float64(status.RouteCount))
	m.Age.Set(status.RouteAgeSeconds)
	for _, source := range []string{"trade", "local", "hybrid", "retained", "none", "unavailable"} {
		value := 0.0
		if source == status.Source {
			value = 1
		}
		m.Source.WithLabelValues(source).Set(value)
	}
}
