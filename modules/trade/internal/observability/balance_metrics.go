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
	mu                  sync.Mutex
	accounts            map[string]balanceState
}

type balanceState struct {
	lastRun             float64
	lastSuccess         float64
	maxDifference       float64
	consecutiveFailures float64
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
			Help: "Maximum relative difference between the prior local and Exchange balances.",
		}),
		consecutiveFailures: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "moox_trade_balance_sync_consecutive_failures",
			Help: "Number of consecutive failed balance synchronization runs.",
		}),
		accounts: make(map[string]balanceState),
	}
	for _, collector := range []prometheus.Collector{metrics.runs, metrics.lastRun, metrics.lastSuccess, metrics.maxDifference, metrics.consecutiveFailures} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}

func (m *BalanceMetrics) Observe(
	exchangeAccountID string,
	now time.Time,
	maxDifference float64,
	err error,
) {
	if m == nil || exchangeAccountID == "" {
		return
	}
	result := "success"
	if err != nil {
		result = "error"
	}
	timestamp := float64(now.UTC().Unix())
	m.runs.WithLabelValues(result).Inc()
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.accounts[exchangeAccountID]
	state.lastRun = timestamp
	if err == nil {
		state.lastSuccess = timestamp
		state.maxDifference = max(maxDifference, 0)
		state.consecutiveFailures = 0
	} else {
		state.consecutiveFailures++
	}
	m.accounts[exchangeAccountID] = state
	m.updateAggregate()
}

func (m *BalanceMetrics) Remove(exchangeAccountID string) {
	if m == nil || exchangeAccountID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.accounts, exchangeAccountID)
	m.updateAggregate()
}

func (m *BalanceMetrics) updateAggregate() {
	var latestRun, earliestSuccess, maximumDifference, maximumFailures float64
	for _, state := range m.accounts {
		latestRun = max(latestRun, state.lastRun)
		if earliestSuccess == 0 ||
			(state.lastSuccess > 0 && state.lastSuccess < earliestSuccess) {
			earliestSuccess = state.lastSuccess
		}
		maximumDifference = max(maximumDifference, state.maxDifference)
		maximumFailures = max(maximumFailures, state.consecutiveFailures)
	}
	m.lastRun.Set(latestRun)
	m.lastSuccess.Set(earliestSuccess)
	m.maxDifference.Set(maximumDifference)
	m.consecutiveFailures.Set(maximumFailures)
}
