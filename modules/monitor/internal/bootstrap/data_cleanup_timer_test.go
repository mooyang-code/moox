package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
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
		pruneChecks: func(_ context.Context, now time.Time) error {
			calls = append(calls, "checks")
			assert.Equal(t, time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC), now)
			return nil
		},
	})
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, []string{"results", "alerts", "dedupe", "series", "arrivals", "checks"}, calls)
}

func TestRunMonitorDataCleanupAllowsUnavailableStores(t *testing.T) {
	require.NoError(t, runMonitorDataCleanup(context.Background(), monitorDataCleanupOps{}))
}

// 数据清理定时任务接上了主机快照历史的清理：早于 7 天的快照被删掉，7 天内的保留。
func TestMonitorDataCleanupPrunesHostSamples(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	samples, err := store.WithDatabase(mgr, hostmetrics.NewSampleStore)
	require.NoError(t, err)
	now := time.Now().UTC()
	for _, at := range []time.Time{now.Add(-8 * 24 * time.Hour), now.Add(-time.Hour)} {
		require.NoError(t, samples.Write(t.Context(), "agent-a", at, &hostmetricpb.HostSnapshot{}))
	}

	ops := buildMonitorDataCleanupOps(config.Default(), &Runtime{Store: mgr, Repositories: mgr.Repositories(), HostSamples: samples})
	require.NotNil(t, ops.pruneHostSamples)
	require.NoError(t, runMonitorDataCleanup(t.Context(), ops))
	left, err := samples.History(t.Context(), "agent-a", now.Add(-30*24*time.Hour), now, 0)
	require.NoError(t, err)
	require.Len(t, left, 1)
}
