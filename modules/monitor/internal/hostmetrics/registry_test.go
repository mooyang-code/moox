package hostmetrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRegistryPersistsAcrossRestartAndRejectsLateObservation(t *testing.T) {
	ctx := context.Background()
	db := openRegistryTestDB(t)
	first := NewRegistry(db)
	t0 := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)

	result, err := first.Observe(ctx, HostObservation{
		HostID: "host-a", Hostname: "host-a", BootID: "boot-a",
		OccurredAt: t0, EventID: "event-0",
	})
	require.NoError(t, err)
	require.True(t, result.Updated)
	require.True(t, result.Current)

	restarted := NewRegistry(db)
	rows, err := restarted.List(ctx, t0.Add(60*time.Second))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "host-a", rows[0].HostID)
	require.True(t, rows[0].Reachable)
	require.Equal(t, int64(60), rows[0].StaleSeconds)

	result, err = restarted.Observe(ctx, HostObservation{
		HostID: "host-a", Hostname: "host-new", BootID: "boot-new",
		OccurredAt: t0.Add(100 * time.Second), EventID: "event-100",
	})
	require.NoError(t, err)
	require.True(t, result.Updated)
	require.True(t, result.Current)

	result, err = restarted.Observe(ctx, HostObservation{
		HostID: "host-a", Hostname: "host-late", BootID: "boot-late",
		OccurredAt: t0.Add(50 * time.Second), EventID: "event-50",
	})
	require.NoError(t, err)
	require.False(t, result.Updated)
	require.False(t, result.Current)

	rows, err = restarted.List(ctx, t0.Add(110*time.Second))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "host-new", rows[0].Hostname)
	require.Equal(t, "boot-new", rows[0].BootID)
	require.Equal(t, "event-100", rows[0].LastEventID)
	require.Equal(t, t0.Add(100*time.Second), rows[0].LastSeenAt)
}

// 是否在线只看距最近一次上报多久：超过停报时限即不在线，新的上报一到就恢复，不另存状态。
func TestRegistryReachabilityFollowsLastSeen(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry(openRegistryTestDB(t))
	t0 := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	observeAgent(t, registry, "control", t0)

	rows, err := registry.List(ctx, t0.Add(DefaultHostStaleAfter+time.Second))
	require.NoError(t, err)
	require.False(t, rows[0].Reachable)

	observeAgent(t, registry, "control", t0.Add(4*time.Minute))
	rows, err = registry.List(ctx, t0.Add(4*time.Minute+time.Second))
	require.NoError(t, err)
	require.True(t, rows[0].Reachable)
}

func openRegistryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "monitor.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.SQL()).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	return db
}

func observeAgent(t *testing.T, r *Registry, host string, at time.Time) ObserveResult {
	t.Helper()
	result, err := r.Observe(context.Background(), HostObservation{HostID: host, Hostname: host + "-name", BootID: "boot", OccurredAt: at, EventID: host + at.String()})
	require.NoError(t, err)
	return result
}
