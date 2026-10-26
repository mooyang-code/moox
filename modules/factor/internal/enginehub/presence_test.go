package enginehub

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/stretchr/testify/require"
)

// 引擎在线状态按最近心跳判断，与 45 秒的租约无关；没有启用的因子任务时不需要引擎；
// 最近心跳落库，管理端重启后引擎一直没回来仍判为离线。
func TestPresenceUsesLastHeartbeatAndSurvivesRestart(t *testing.T) {
	hub, db, clock := newTestHub(t)
	hub.heartbeats = db
	ctx := context.Background()

	state, _, _, err := hub.Presence(ctx)
	require.NoError(t, err)
	require.Equal(t, report.PresenceNotRequired, state, "没有启用的因子任务")

	seedTask(t, db, "s1", nil)
	state, engineID, _, err := hub.Presence(ctx)
	require.NoError(t, err)
	require.Equal(t, report.PresenceOffline, state, "有启用的因子任务但从未收到心跳")
	require.Empty(t, engineID)

	id := domain.EngineIdentity{EngineID: "factor-engine@m1", BootID: "b1"}
	_, err = hub.Heartbeat(ctx, id, domain.EngineStatus{})
	require.NoError(t, err)
	clock.Advance(90 * time.Second) // 租约已过期，但心跳在 2 分钟内
	state, engineID, _, err = hub.Presence(ctx)
	require.NoError(t, err)
	require.Equal(t, report.PresenceOnline, state)
	require.Equal(t, "factor-engine@m1", engineID)

	clock.Advance(time.Minute)
	state, _, _, err = hub.Presence(ctx)
	require.NoError(t, err)
	require.Equal(t, report.PresenceOffline, state, "超过 2 分钟没有心跳")

	// 管理端重启：新 hub 没有内存状态，从库里读到最近心跳，仍判为离线并带出引擎 ID。
	restarted := New(db, WithClock(clock.Now), WithHeartbeatStore(db))
	state, engineID, last, err := restarted.Presence(ctx)
	require.NoError(t, err)
	require.Equal(t, report.PresenceOffline, state)
	require.Equal(t, "factor-engine@m1", engineID)
	require.Equal(t, time.Unix(1_000_000, 0).UTC(), last)
}

// 同一引擎的心跳 30 秒内只落库一次；换了引擎立即落库。
func TestHeartbeatPersistenceIsThrottled(t *testing.T) {
	hub, db, clock := newTestHub(t)
	hub.heartbeats = db
	ctx := context.Background()
	id := domain.EngineIdentity{EngineID: "factor-engine@m1", BootID: "b1"}
	_, err := hub.Heartbeat(ctx, id, domain.EngineStatus{})
	require.NoError(t, err)
	first := clock.Now()
	clock.Advance(10 * time.Second)
	_, err = hub.Heartbeat(ctx, id, domain.EngineStatus{})
	require.NoError(t, err)
	saved, ok, err := db.LastEngineHeartbeat(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, first, saved.At)

	clock.Advance(50 * time.Second) // 租约已过期，换一个引擎
	other := domain.EngineIdentity{EngineID: "factor-engine@m2", BootID: "b2"}
	_, err = hub.Heartbeat(ctx, other, domain.EngineStatus{})
	require.NoError(t, err)
	saved, _, err = db.LastEngineHeartbeat(ctx)
	require.NoError(t, err)
	require.Equal(t, "factor-engine@m2", saved.EngineID)
	require.Equal(t, clock.Now(), saved.At)
}
