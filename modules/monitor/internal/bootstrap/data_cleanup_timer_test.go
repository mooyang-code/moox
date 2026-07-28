package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunMonitorDataCleanupAttemptsEveryStore(t *testing.T) {
	wantErr := errors.New("result cleanup failed")
	var calls []string
	err := runMonitorDataCleanup(context.Background(), monitorDataCleanupOps{
		retention: 24 * time.Hour,
		now:       func() time.Time { return time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC) },
		deleteResults: func(context.Context, time.Time) error {
			calls = append(calls, "results")
			return wantErr
		},
		deleteAlerts: func(context.Context, time.Time) error {
			calls = append(calls, "alerts")
			return nil
		},
		evaluationRetention: 14 * 24 * time.Hour,
		deleteMetricEvaluations: func(context.Context, time.Time, int) (int64, error) {
			calls = append(calls, "evaluations")
			return 0, nil
		},
		pruneDedupe: func(context.Context, time.Time) error {
			calls = append(calls, "dedupe")
			return nil
		},
	})
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, []string{"results", "alerts", "evaluations", "dedupe"}, calls)
}

func TestRunMonitorDataCleanupBoundsMetricEvaluationBatches(t *testing.T) {
	var calls int
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	require.NoError(t, runMonitorDataCleanup(context.Background(), monitorDataCleanupOps{
		now:                 func() time.Time { return now },
		evaluationRetention: 14 * 24 * time.Hour,
		deleteMetricEvaluations: func(_ context.Context, cutoff time.Time, batchSize int) (int64, error) {
			calls++
			assert.Equal(t, now.Add(-14*24*time.Hour), cutoff)
			assert.Equal(t, 500, batchSize)
			return 500, nil
		},
	}))
	assert.Equal(t, 10, calls)
}

func TestRunMonitorDataCleanupStopsAfterShortMetricBatch(t *testing.T) {
	var calls int
	require.NoError(t, runMonitorDataCleanup(context.Background(), monitorDataCleanupOps{
		evaluationRetention: 14 * 24 * time.Hour,
		deleteMetricEvaluations: func(context.Context, time.Time, int) (int64, error) {
			calls++
			return 17, nil
		},
	}))
	assert.Equal(t, 1, calls)
}

func TestRunMonitorDataCleanupAllowsUnavailableStores(t *testing.T) {
	require.NoError(t, runMonitorDataCleanup(context.Background(), monitorDataCleanupOps{}))
}
