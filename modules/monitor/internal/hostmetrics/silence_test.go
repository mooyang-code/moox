package hostmetrics

import (
	"context"
	"errors"
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
	scanner := NewSilenceScanner(NewRegistry(db), 90*time.Second, PresenceTransitionFunc(func(_ context.Context, transition PresenceTransition) error {
		got = append(got, transition)
		return nil
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

func TestSilenceScannerRetriesFailedTransitionDelivery(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry(openRegistryTestDB(t))
	t0 := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	_, err := registry.Observe(ctx, HostObservation{
		AgentID: "agent-a", Hostname: "host-a", BootID: "boot-a",
		OccurredAt: t0, EventID: "event-0",
	})
	require.NoError(t, err)

	attempts := 0
	scanner := NewSilenceScanner(registry, 90*time.Second, PresenceTransitionFunc(func(context.Context, PresenceTransition) error {
		attempts++
		if attempts == 1 {
			return errors.New("wecom unavailable")
		}
		return nil
	}))
	require.NoError(t, scanner.Scan(ctx, t0.Add(91*time.Second)))
	require.Equal(t, 1, attempts)
	require.Len(t, scanner.pending, 1)
	require.NoError(t, scanner.Scan(ctx, t0.Add(121*time.Second)))
	require.Equal(t, 2, attempts)
	require.Empty(t, scanner.pending)
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
	store.SetPresenceTransitionSink(PresenceTransitionFunc(func(_ context.Context, transition PresenceTransition) error {
		got = append(got, transition)
		return nil
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

// 离线通知发送失败后主机已恢复：待重发的离线通知已被取代，扫描重试时必须丢弃，不能在恢复通知之后再发“失联”。
func TestSilenceScannerDropsPendingOfflineNoticeAfterRecovery(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry(openRegistryTestDB(t))
	t0 := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	_, err := registry.Observe(ctx, HostObservation{AgentID: "agent-a", Hostname: "host-a", BootID: "boot-a", OccurredAt: t0, EventID: "event-0"})
	require.NoError(t, err)

	var delivered []string
	failOffline := true
	scanner := NewSilenceScanner(registry, 90*time.Second, PresenceTransitionFunc(func(_ context.Context, tr PresenceTransition) error {
		if tr.To == PresenceUnreachable && failOffline {
			return errors.New("wecom unavailable")
		}
		delivered = append(delivered, tr.To)
		return nil
	}))
	require.NoError(t, scanner.Scan(ctx, t0.Add(91*time.Second)))
	require.Len(t, scanner.pending, 1)

	// 主机恢复上报，恢复通知成功发出
	result, err := registry.Observe(ctx, HostObservation{AgentID: "agent-a", Hostname: "host-a", BootID: "boot-a", OccurredAt: t0.Add(100 * time.Second), EventID: "event-1"})
	require.NoError(t, err)
	require.NotNil(t, result.Transition)
	require.NoError(t, scanner.sink.HandlePresenceTransition(ctx, *result.Transition))

	failOffline = false
	require.NoError(t, scanner.Scan(ctx, t0.Add(110*time.Second)))
	require.Equal(t, []string{PresenceReachable}, delivered, "过期的离线通知不能在恢复通知之后补发")
	require.Empty(t, scanner.pending)
}

// 旧采集器的失联通知发送失败，随后同一登记主机上的新采集器开始上报：待重发的失联通知作废。
func TestSilenceScannerDropsPendingLostNoticeOnceANewAgentReports(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry(openRegistryTestDB(t))
	registry.SetRegisteredHosts(func(context.Context) (map[string]bool, error) { return map[string]bool{"control": true}, nil })
	t0 := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	observeAgent(t, registry, "control", "OLD", t0)

	var sent []PresenceTransition
	fail := true
	scanner := NewSilenceScanner(registry, 90*time.Second, PresenceTransitionFunc(func(_ context.Context, transition PresenceTransition) error {
		if fail {
			return errors.New("webhook down")
		}
		sent = append(sent, transition)
		return nil
	}))
	require.NoError(t, scanner.Scan(ctx, t0.Add(2*time.Minute)))
	require.Empty(t, sent)

	observeAgent(t, registry, "control", "NEW", t0.Add(3*time.Minute))
	fail = false
	require.NoError(t, scanner.Scan(ctx, t0.Add(3*time.Minute+10*time.Second)))
	require.Empty(t, sent, "主机已由新采集器接替，不能再补发失联通知")
}

// 观测输入（主机快照经 EventBus 送达 Monitor）中断期间，所有主机的 last_seen 都是旧的：
// 冻结期间不判失联，恢复判定之后超时的主机才被判为失联。
func TestSilenceScannerFreezesWhileObservationIsSuspended(t *testing.T) {
	ctx := context.Background()
	db := openRegistryTestDB(t)
	t0 := time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)
	_, err := NewRegistry(db).Observe(ctx, HostObservation{AgentID: "agent-a", Hostname: "host-a", BootID: "boot-a", OccurredAt: t0, EventID: "event-0"})
	require.NoError(t, err)

	var got []PresenceTransition
	scanner := NewSilenceScanner(NewRegistry(db), 90*time.Second, PresenceTransitionFunc(func(_ context.Context, transition PresenceTransition) error {
		got = append(got, transition)
		return nil
	}))
	suspended := true
	scanner.SetSuspended(func(time.Time) bool { return suspended })

	require.NoError(t, scanner.Scan(ctx, t0.Add(10*time.Minute)))
	require.Empty(t, got, "观测输入中断期间不能把主机判为失联")
	status, err := NewRegistry(db).Status(ctx, "agent-a")
	require.NoError(t, err)
	require.Equal(t, PresenceReachable, status, "登记的状态也不能被改成失联")

	suspended = false
	require.NoError(t, scanner.Scan(ctx, t0.Add(10*time.Minute)))
	require.Len(t, got, 1, "解除冻结后，仍然没有样本的主机才判为失联")
	require.Equal(t, PresenceUnreachable, got[0].To)
}
