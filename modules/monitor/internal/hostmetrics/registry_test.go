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
	require.NoError(t, db.Exec(schema.SQL()).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	return db
}

func observeAgent(t *testing.T, r *Registry, host, agent string, at time.Time) ObserveResult {
	t.Helper()
	result, err := r.Observe(context.Background(), HostObservation{HostID: host, AgentID: agent, Hostname: agent + "-host", BootID: "boot", OccurredAt: at, EventID: agent + at.String()})
	require.NoError(t, err)
	return result
}

// 只有登记主机上最近上报的采集器才产生失联与恢复事件：旧采集器超时、没有 host_id、未登记主机的采集器都只更新状态。
func TestOnlyTheLatestAgentOfARegisteredHostProducesPresenceTransitions(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry(openRegistryTestDB(t))
	r.SetRegisteredHosts(func(context.Context) (map[string]bool, error) { return map[string]bool{"control": true}, nil })
	t0 := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	observeAgent(t, r, "control", "OLD", t0)
	observeAgent(t, r, "control", "NEW", t0.Add(time.Hour))
	observeAgent(t, r, "", "NOHOST", t0.Add(time.Hour))
	observeAgent(t, r, "gone", "UNREGISTERED", t0.Add(time.Hour))

	transitions, err := r.MarkUnreachable(ctx, t0.Add(time.Hour+4*time.Minute), DefaultHostStaleAfter)
	require.NoError(t, err)
	require.Len(t, transitions, 1, "旧采集器的失联不产生事件")
	require.Equal(t, "NEW", transitions[0].AgentID)
	require.Equal(t, PresenceUnreachable, transitions[0].To)
	for _, agent := range []string{"OLD", "NOHOST", "UNREGISTERED"} {
		status, err := r.Status(ctx, agent)
		require.NoError(t, err)
		require.Equal(t, PresenceUnreachable, status, agent+" 的状态仍然要更新")
	}

	// 最新采集器恢复要通知；被取代的旧采集器回来而新采集器仍在线时不通知。
	back := observeAgent(t, r, "control", "NEW", t0.Add(time.Hour+5*time.Minute))
	require.NotNil(t, back.Transition)
	require.Equal(t, PresenceReachable, back.Transition.To)
	oldBack := observeAgent(t, r, "control", "OLD", t0.Add(time.Hour+6*time.Minute))
	require.Nil(t, oldBack.Transition, "同一主机另有采集器在线时，旧采集器恢复不通知")
	unregisteredBack := observeAgent(t, r, "gone", "UNREGISTERED", t0.Add(time.Hour+6*time.Minute))
	require.Nil(t, unregisteredBack.Transition)
}

// 拓扑还不知道（提供者返回 nil）或读取失败时不过滤，保持原有行为：不能因此漏发告警。
func TestPresenceTransitionsAreNotFilteredWhileTopologyIsUnknown(t *testing.T) {
	ctx := context.Background()
	for name, provider := range map[string]func(context.Context) (map[string]bool, error){
		"unknown": func(context.Context) (map[string]bool, error) { return nil, nil },
		"failed":  func(context.Context) (map[string]bool, error) { return nil, context.DeadlineExceeded },
	} {
		t.Run(name, func(t *testing.T) {
			r := NewRegistry(openRegistryTestDB(t))
			r.SetRegisteredHosts(provider)
			t0 := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
			observeAgent(t, r, "", "ANY", t0)
			transitions, err := r.MarkUnreachable(ctx, t0.Add(10*time.Minute), DefaultHostStaleAfter)
			require.NoError(t, err)
			require.Len(t, transitions, 1)
		})
	}
}

// 旧采集器失联并通知后，同一主机上换了新采集器开始上报：补发主机恢复（沿用旧采集器的告警标识）。
func TestNewAgentOfAnUnreachableRegisteredHostRecoversTheHost(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry(openRegistryTestDB(t))
	r.SetRegisteredHosts(func(context.Context) (map[string]bool, error) { return map[string]bool{"control": true}, nil })
	t0 := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	observeAgent(t, r, "control", "OLD", t0)
	transitions, err := r.MarkUnreachable(ctx, t0.Add(4*time.Minute), DefaultHostStaleAfter)
	require.NoError(t, err)
	require.Len(t, transitions, 1)

	back := observeAgent(t, r, "control", "NEW", t0.Add(10*time.Minute))
	require.NotNil(t, back.Transition, "换了新采集器后主机恢复要通知")
	require.Equal(t, "OLD", back.Transition.AgentID)
	require.Equal(t, PresenceReachable, back.Transition.To)

	// 主机上已有采集器在线时，再来一个新采集器不是恢复。
	other := observeAgent(t, r, "control", "THIRD", t0.Add(11*time.Minute))
	require.Nil(t, other.Transition)
	// 没有登记的主机不产生恢复。
	observeAgent(t, r, "gone", "G1", t0)
	_, err = r.MarkUnreachable(ctx, t0.Add(20*time.Minute), DefaultHostStaleAfter)
	require.NoError(t, err)
	require.Nil(t, observeAgent(t, r, "gone", "G2", t0.Add(30*time.Minute)).Transition)
}
