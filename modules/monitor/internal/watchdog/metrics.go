package watchdog

import (
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	defaultMetricsOnce sync.Once
	defaultMetrics     *Metrics
	defaultMetricsErr  error
)

type Metrics struct {
	checks      *prometheus.CounterVec
	lastRun     *prometheus.GaugeVec
	lastSuccess *prometheus.GaugeVec
	latency     *prometheus.HistogramVec
}

func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	metrics := &Metrics{
		checks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "moox_monitor_watchdog_checks_total",
			Help: "Monitor watchdog checks grouped by kind and result.",
		}, []string{"kind", "result"}),
		lastRun: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "moox_monitor_watchdog_last_run_timestamp_seconds",
			Help: "Unix timestamp of the last watchdog run.",
		}, []string{"kind"}),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "moox_monitor_watchdog_last_success_timestamp_seconds",
			Help: "Unix timestamp of the last successful watchdog run.",
		}, []string{"kind"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "moox_monitor_watchdog_latency_seconds",
			Help:    "Monitor watchdog check latency.",
			Buckets: prometheus.DefBuckets,
		}, []string{"kind"}),
	}
	if err := registerer.Register(metrics.checks); err != nil {
		return nil, err
	}
	if err := registerer.Register(metrics.lastRun); err != nil {
		return nil, err
	}
	if err := registerer.Register(metrics.lastSuccess); err != nil {
		return nil, err
	}
	if err := registerer.Register(metrics.latency); err != nil {
		return nil, err
	}
	return metrics, nil
}

func DefaultMetrics() (*Metrics, error) {
	defaultMetricsOnce.Do(func() {
		defaultMetrics, defaultMetricsErr = NewMetrics(prometheus.DefaultRegisterer)
	})
	return defaultMetrics, defaultMetricsErr
}

func (m *Metrics) Observe(check domain.Check, result domain.CheckResult) {
	if m == nil {
		return
	}
	kind := check.Kind
	if kind != domain.CheckKindHTTP && kind != domain.CheckKindTCP {
		kind = "external"
	}
	outcome := "error"
	if result.Success {
		outcome = "success"
	}
	observedAt := result.CheckedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	m.checks.WithLabelValues(kind, outcome).Inc()
	m.lastRun.WithLabelValues(kind).Set(float64(observedAt.Unix()))
	if result.Success {
		m.lastSuccess.WithLabelValues(kind).Set(float64(observedAt.Unix()))
	}
	m.latency.WithLabelValues(kind).Observe(max(float64(result.LatencyMS)/1000, 0))
}
