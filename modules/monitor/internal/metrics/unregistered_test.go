package metrics

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUnregisteredProducersRecordsAndExpires(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	tracker := NewUnregisteredProducers()
	tracker.Record("collector", "compute-1", "collector@compute-1", "v1", now.Add(-time.Minute))
	tracker.Record("collector", "compute-1", "collector@compute-1", "v2", now)
	tracker.Record("archive", "storage", "archive@storage", "v1", now.Add(-UnregisteredWindow-time.Second))
	tracker.Record("", "storage", "x", "v1", now)

	got := tracker.List(now)
	require.Len(t, got, 1, "超过保留时长的记录不再列出，空服务名不记录")
	require.Equal(t, "collector", got[0].ServiceName)
	require.Equal(t, "compute-1", got[0].NodeID)
	require.Equal(t, "v2", got[0].Version)
	require.Equal(t, now.Add(-time.Minute), got[0].FirstSeenAt)
	require.Equal(t, now, got[0].LastSeenAt)

	require.Empty(t, tracker.List(now.Add(UnregisteredWindow+time.Second)))
	var nilTracker *UnregisteredProducers
	nilTracker.Record("collector", "control", "collector@control", "v1", now)
	require.Empty(t, nilTracker.List(now))
}

func TestUnregisteredProducersIsBounded(t *testing.T) {
	now := time.Now().UTC()
	tracker := NewUnregisteredProducers()
	for i := 0; i < maxUnregisteredProducers+20; i++ {
		tracker.Record(fmt.Sprintf("svc-%03d", i), "node", "", "v", now)
	}
	require.Len(t, tracker.List(now), maxUnregisteredProducers)
}
