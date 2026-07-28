package hostmetrics

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSilenceScannerTransitionsOnceAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	db := openRegistryTestDB(t)
	t0 := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	registry := NewRegistry(db)
	_, err := registry.Observe(ctx, HostObservation{
		AgentID: "agent-a", Hostname: "host-a", BootID: "boot-a",
		OccurredAt: t0, EventID: "event-0",
	})
	require.NoError(t, err)

	var got []PresenceTransition
	scanner := NewSilenceScanner(NewRegistry(db), 90*time.Second, PresenceTransitionFunc(func(_ context.Context, transition PresenceTransition) {
		got = append(got, transition)
	}))
	require.NoError(t, scanner.Scan(ctx, t0.Add(90*time.Second)))
	require.Empty(t, got, "an agent is stale only after the full threshold")
	require.NoError(t, scanner.Scan(ctx, t0.Add(91*time.Second)))
	require.Equal(t, []PresenceTransition{{
		AgentID: "agent-a", Hostname: "host-a",
		From: PresenceReachable, To: PresenceUnreachable,
		ObservedAt: t0.Add(91 * time.Second),
	}}, got)

	require.NoError(t, scanner.Scan(ctx, t0.Add(121*time.Second)))
	require.Len(t, got, 1, "an unreachable agent must not transition every scan")

	rows, err := NewRegistry(db).List(ctx, t0.Add(121*time.Second))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.False(t, rows[0].Reachable)
	require.Equal(t, int64(121), rows[0].StaleSeconds)
}

func TestStorePublishesRecoveryTransitionAndDoesNotRegressLatestView(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry(openRegistryTestDB(t))
	t0 := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	_, err := registry.Observe(ctx, HostObservation{
		AgentID: validHostMetric().GetAgentId(), Hostname: "host", BootID: "boot",
		OccurredAt: t0, EventID: "event-0",
	})
	require.NoError(t, err)
	_, err = registry.MarkUnreachable(ctx, t0.Add(91*time.Second), 90*time.Second)
	require.NoError(t, err)

	var got []PresenceTransition
	store := NewStore(&fakeSnapshotWriter{}, nil)
	store.SetRegistry(registry)
	store.SetPresenceTransitionSink(PresenceTransitionFunc(func(_ context.Context, transition PresenceTransition) {
		got = append(got, transition)
	}))

	newer := validHostMessage(t)
	newer.OccurredAt = timestamppb.New(t0.Add(100 * time.Second))
	newer.EventId = "0190f4d0-7b1c-7f45-9a3e-7c28f6479a74"
	require.NoError(t, store.Persist(ctx, newer, validHostMetric()))
	require.Len(t, got, 1)
	require.Equal(t, PresenceReachable, got[0].To)

	late := validHostMessage(t)
	late.OccurredAt = timestamppb.New(t0.Add(50 * time.Second))
	late.EventId = "0190f4d0-7b1c-7f45-9a3e-7c28f6479a75"
	require.NoError(t, store.Persist(ctx, late, validHostMetric()))

	rows, err := store.ListAgents(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, t0.Add(100*time.Second).Format(time.RFC3339Nano), rows[0].LastSeenAt)
	require.True(t, rows[0].Reachable)
}
