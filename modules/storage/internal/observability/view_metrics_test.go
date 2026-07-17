package observability

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestViewMetricsRecordFixedOutcomeDimensions(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewViewMetrics(registry)
	require.NoError(t, err)

	metrics.IncDeriveInFlight()
	metrics.ObserveDerive("time_series", "error")
	metrics.ObserveBatch("duckdb", "error", 25*time.Millisecond)
	metrics.ObserveDelivery("nak", "success")
	metrics.IncRedelivery()
	metrics.DecDeriveInFlight()

	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.deriveTotal.WithLabelValues("time_series", "error")))
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.deliveryTotal.WithLabelValues("nak", "success")))
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.redeliveryTotal))
	assert.Equal(t, float64(0), testutil.ToFloat64(metrics.deriveInFlight))
}
