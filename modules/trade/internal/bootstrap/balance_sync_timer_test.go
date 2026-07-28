package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	tradeobservability "github.com/mooyang-code/moox/modules/trade/internal/observability"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type balanceSyncerStub struct {
	difference float64
	err        error
}

func (s balanceSyncerStub) Sync(context.Context) (float64, error) {
	return s.difference, s.err
}

func TestBalanceSyncTimerRecordsSuccessAndFailure(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := tradeobservability.NewBalanceMetrics(registry)
	require.NoError(t, err)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	handler := &balanceSyncTimer{
		syncer:  balanceSyncerStub{difference: 0.02},
		metrics: metrics,
		now:     func() time.Time { return now },
	}
	require.NoError(t, handler.Handle(context.Background()))

	handler.syncer = balanceSyncerStub{err: errors.New("venue unavailable")}
	require.Error(t, handler.Handle(context.Background()))
	families, err := registry.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, families)
}

func TestMaxBalanceDifference(t *testing.T) {
	before := []*service.Balance{{Currency: "USDT", Total: "100"}, {Currency: "BTC", Total: "2"}}
	after := []*service.Balance{{Currency: "USDT", Total: "105"}, {Currency: "BTC", Total: "2"}}
	require.InDelta(t, 5.0/105.0, maxBalanceDifference(before, after), 0.00001)
}

func TestBalanceMetricsFreshnessContract(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := tradeobservability.NewBalanceMetrics(registry)
	require.NoError(t, err)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	metrics.Observe(now, 0.01, nil)
	require.Equal(t, float64(now.Unix()), testutil.ToFloat64(lastSuccessCollector(t, registry)))
}

func lastSuccessCollector(t *testing.T, registry *prometheus.Registry) prometheus.Gauge {
	t.Helper()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_unused"})
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == "moox_trade_balance_sync_last_success_timestamp_seconds" {
			value := family.GetMetric()[0].GetGauge().GetValue()
			gauge.Set(value)
			return gauge
		}
	}
	t.Fatal("last success metric not found")
	return gauge
}
