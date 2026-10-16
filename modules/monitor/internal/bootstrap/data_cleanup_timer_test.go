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
		pruneDedupe: func(context.Context, time.Time) error {
			calls = append(calls, "dedupe")
			return nil
		},
		pruneSeries: func(context.Context, time.Time) error {
			calls = append(calls, "series")
			return nil
		},
		pruneArrivals: func(_ context.Context, cutoff time.Time) error {
			calls = append(calls, "arrivals")
			// 到达记录固定保留 7 天，不跟检查结果的保留天数走。
			assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), cutoff)
			return nil
		},
	})
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, []string{"results", "alerts", "dedupe", "series", "arrivals"}, calls)
}

func TestRunMonitorDataCleanupAllowsUnavailableStores(t *testing.T) {
	require.NoError(t, runMonitorDataCleanup(context.Background(), monitorDataCleanupOps{}))
}
