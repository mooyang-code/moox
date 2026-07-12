package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetrics_New_ShouldRegisterCountersAndHistograms(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	require.NotNil(t, m)
	assert.NotNil(t, m.Tasks)
	assert.NotNil(t, m.Duration)
}

func TestMetrics_Observe_ValidLabels_ShouldIncrementCounterAndHistogram(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.Observe("factor", "json", "ok", 150*time.Millisecond)

	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	var taskTotal *dto.MetricFamily
	var duration *dto.MetricFamily
	for _, mf := range metricFamilies {
		switch mf.GetName() {
		case "moox_pyruntime_worker_task_total":
			taskTotal = mf
		case "moox_pyruntime_worker_task_duration_seconds":
			duration = mf
		}
	}
	require.NotNil(t, taskTotal)
	require.NotNil(t, duration)
	require.Len(t, taskTotal.Metric, 1)
	assert.Equal(t, 1.0, taskTotal.Metric[0].GetCounter().GetValue())
	require.Len(t, duration.Metric, 1)
	assert.InDelta(t, 0.15, duration.Metric[0].GetHistogram().GetSampleSum(), 0.001)
}
