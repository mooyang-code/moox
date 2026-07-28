package observability

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	defaultBalanceMetricsOnce sync.Once
	defaultBalanceMetrics     *BalanceMetrics
	defaultBalanceMetricsErr  error
)

type BalanceMetrics struct {
	runs                *prometheus.CounterVec
	lastRun             prometheus.Gauge
	lastSuccess         prometheus.Gauge
	maxDifference       prometheus.Gauge
	consecutiveFailures prometheus.Gauge
}

func DefaultBalanceMetrics() (*BalanceMetrics, error) {
	defaultBalanceMetricsOnce.Do(func() {
		defaultBalanceMetrics, defaultBalanceMetricsErr = NewBalanceMetrics(prometheus.DefaultRegisterer)
	})
	return defaultBalanceMetrics, defaultBalanceMetricsErr
}

func NewBalanceMetrics(registerer prometheus.Registerer) (*BalanceMetrics, error) {
	metrics := &BalanceMetrics{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "moox_trade_balance_sync_runs_total",
			Help: "Trade balance synchronization runs grouped by result.",
		}, []string{"result"}),
		lastRun: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "moox_trade_balance_sync_last_run_timestamp_seconds",
			Help: "Unix timestamp of the last balance sync run.",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "moox_trade_balance_sync_last_success_timestamp_seconds",
			Help: "Unix timestamp of the last successful balance sync run.",
		}),
		maxDifference: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "moox_trade_balance_sync_max_difference_ratio",
			Help: "Maximum relative difference between the prior local and venue balances.",
		}),
		consecutiveFailures: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "moox_trade_balance_sync_consecutive_failures",
			Help: "Number of consecutive failed balance synchronization runs.",
		}),
	}
	for _, collector := range []prometheus.Collector{metrics.runs, metrics.lastRun, metrics.lastSuccess, metrics.maxDifference, metrics.consecutiveFailures} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}

func (m *BalanceMetrics) Observe(now time.Time, maxDifference float64, err error) {
	if m == nil {
		return
	}
	result := "success"
	if err != nil {
		result = "error"
	}
	timestamp := float64(now.UTC().Unix())
	m.runs.WithLabelValues(result).Inc()
	m.lastRun.Set(timestamp)
	if err == nil {
		m.lastSuccess.Set(timestamp)
		m.maxDifference.Set(max(maxDifference, 0))
		m.consecutiveFailures.Set(0)
	} else {
		m.consecutiveFailures.Inc()
	}
}
