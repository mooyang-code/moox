package hostmetrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRegistryPersistsAcrossRestartAndRejectsLateObservation(t *testing.T) {
	ctx := context.Background()
	db := openRegistryTestDB(t)
	first := NewRegistry(db)
	t0 := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)

	result, err := first.Observe(ctx, HostObservation{
		AgentID: "agent-a", Hostname: "host-a", BootID: "boot-a",
		OccurredAt: t0, EventID: "event-0",
	})
	require.NoError(t, err)
	require.True(t, result.Updated)
	require.True(t, result.Current)
	require.Nil(t, result.Transition)

	restarted := NewRegistry(db)
	rows, err := restarted.List(ctx, t0.Add(60*time.Second))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "agent-a", rows[0].AgentID)
	require.True(t, rows[0].Reachable)
	require.Equal(t, int64(60), rows[0].StaleSeconds)

	result, err = restarted.Observe(ctx, HostObservation{
		AgentID: "agent-a", Hostname: "host-new", BootID: "boot-new",
		OccurredAt: t0.Add(100 * time.Second), EventID: "event-100",
	})
	require.NoError(t, err)
	require.True(t, result.Updated)
	require.True(t, result.Current)

	result, err = restarted.Observe(ctx, HostObservation{
		AgentID: "agent-a", Hostname: "host-late", BootID: "boot-late",
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

func TestRegistryObserveNewerSampleRecoversUnreachableAgent(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry(openRegistryTestDB(t))
	t0 := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	_, err := registry.Observe(ctx, HostObservation{
		AgentID: "agent-a", Hostname: "host-a", BootID: "boot-a",
		OccurredAt: t0, EventID: "event-0",
	})
	require.NoError(t, err)
	transitions, err := registry.MarkUnreachable(ctx, t0.Add(91*time.Second), 90*time.Second)
	require.NoError(t, err)
	require.Len(t, transitions, 1)

	result, err := registry.Observe(ctx, HostObservation{
		AgentID: "agent-a", Hostname: "host-a", BootID: "boot-b",
		OccurredAt: t0.Add(100 * time.Second), EventID: "event-100",
	})
	require.NoError(t, err)
	require.True(t, result.Updated)
	require.Equal(t, &PresenceTransition{
		AgentID: "agent-a", Hostname: "host-a",
		From: PresenceUnreachable, To: PresenceReachable,
		ObservedAt: t0.Add(100 * time.Second),
	}, result.Transition)
}

func openRegistryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "monitor.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE t_monitor_host_agents (
    c_agent_id TEXT PRIMARY KEY,
    c_hostname TEXT NOT NULL,
    c_boot_id TEXT NOT NULL,
    c_last_seen_at DATETIME NOT NULL,
    c_last_event_id TEXT NOT NULL,
    c_status TEXT NOT NULL CHECK (c_status IN ('reachable', 'unreachable')),
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`).Error)
	return db
}
