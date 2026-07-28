package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type nodeSourceStub struct {
	nodes []store.CloudNode
	err   error
}

func (s nodeSourceStub) ListSCFEventNodes(context.Context) ([]store.CloudNode, error) {
	return s.nodes, s.err
}

func TestSCFMetricsExposeOnlyBoundedStatusLabels(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	fresh, stale := now.Add(-30*time.Second), now.Add(-2*time.Minute)
	registry := prometheus.NewRegistry()
	metrics, err := NewSCFMetrics(registry, nodeSourceStub{nodes: []store.CloudNode{
		{NodeID: "fresh", LastHeartbeatAt: &fresh},
		{NodeID: "stale", LastHeartbeatAt: &stale},
		{NodeID: "unknown"},
	}})
	require.NoError(t, err)
	metrics.now = func() time.Time { return now }
	require.NoError(t, metrics.Refresh(context.Background()))
	metrics.ObserveKeepalive(nil)
	metrics.ObserveKeepalive(errors.New("failed"))

	require.Equal(t, float64(1), testutil.ToFloat64(metrics.nodes.WithLabelValues("online")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.nodes.WithLabelValues("timeout")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.nodes.WithLabelValues("unknown")))
	require.Equal(t, float64(120), testutil.ToFloat64(metrics.oldestAge))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.keepaliveRuns.WithLabelValues("success")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.keepaliveRuns.WithLabelValues("error")))
}
