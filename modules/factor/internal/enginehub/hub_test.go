package enginehub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time              { return c.now }
func (c *fakeClock) Advance(delta time.Duration) { c.now = c.now.Add(delta) }

func newTestHub(t *testing.T) (*Hub, *store.Store, *fakeClock) {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	clock := &fakeClock{now: time.Unix(1_000_000, 0).UTC()}
	return New(db, WithClock(clock.Now), WithEngineLeaseTTL(45*time.Second), WithJobLeaseTTL(time.Minute)), db, clock
}

func seedTask(t *testing.T, db *store.Store, taskID string, members map[string]string) domain.FactorTask {
	t.Helper()
	ctx := context.Background()
	set := domain.FactorTask{
		TaskID: taskID, SpaceID: "crypto", Name: "任务" + taskID, SourceDatasetID: "dataset_" + taskID, Freq: "1m",
		ResultDatasetID: "dataset_factor_" + taskID,
		Status:          domain.TaskStatusEnabled,
	}
	require.NoError(t, db.CreateTask(ctx, set))
	for factorID, status := range members {
		if _, err := db.GetFactor(ctx, factorID); err != nil {
			require.NoError(t, db.CreateFactor(ctx, testFactor(factorID)))
		}
		_, err := db.AddMember(ctx, taskID, factorID, factorID, domain.MemberKindFixed, nil)
		require.NoError(t, err)
		if status == domain.MemberStatusEnabled {
			require.NoError(t, db.SetMemberStatus(ctx, taskID, factorID, domain.MemberStatusDisabled, domain.MemberStatusEnabled))
		}
	}
	return set
}

func testFactor(factorID string) domain.FactorDef {
	return domain.MustDefinition(factorID, factorID, "output: close\n")
}

var engineA = domain.EngineIdentity{EngineID: "factor-engine@a", BootID: "boot-a", Version: "v1"}
var engineB = domain.EngineIdentity{EngineID: "factor-engine@b", BootID: "boot-b", Version: "v1"}

func TestHeartbeatGrantsLeaseToFirstEngine(t *testing.T) {
	hub, _, _ := newTestHub(t)

	ttl, err := hub.Heartbeat(context.Background(), engineA, domain.EngineStatus{ConsumerRunning: true})

	require.NoError(t, err)
	require.Equal(t, 45*time.Second, ttl)
	info, status, ok, err := hub.Engine(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, info.Online)
	require.Equal(t, engineA, info.EngineIdentity)
	require.True(t, status.ConsumerRunning)
}

func TestHeartbeatHookRunsOnlyForAcceptedHeartbeats(t *testing.T) {
	hub, _, _ := newTestHub(t)
	calls := 0
	hub.OnHeartbeat(func() { calls++ })
	_, err := hub.Heartbeat(context.Background(), engineA, domain.EngineStatus{})
	require.NoError(t, err)
	require.Equal(t, 1, calls, "accepted heartbeat refreshes the factor dataset metrics")
	_, err = hub.Heartbeat(context.Background(), engineB, domain.EngineStatus{})
	require.ErrorIs(t, err, ErrLeaseConflict)
	require.Equal(t, 1, calls, "a rejected heartbeat carries no progress of the lease holder")
}

func TestHeartbeatRejectsSecondEngineWithinTTL(t *testing.T) {
	hub, _, clock := newTestHub(t)
	_, err := hub.Heartbeat(context.Background(), engineA, domain.EngineStatus{})
	require.NoError(t, err)
	clock.Advance(30 * time.Second)

	_, err = hub.Heartbeat(context.Background(), engineB, domain.EngineStatus{})

	require.ErrorIs(t, err, ErrLeaseConflict)
}

func TestHeartbeatAcceptsNewEngineAfterExpiry(t *testing.T) {
	hub, _, clock := newTestHub(t)
	_, err := hub.Heartbeat(context.Background(), engineA, domain.EngineStatus{})
	require.NoError(t, err)
	clock.Advance(46 * time.Second)

	_, err = hub.Heartbeat(context.Background(), engineB, domain.EngineStatus{})

	require.NoError(t, err)
	info, _, _, err := hub.Engine(context.Background())
	require.NoError(t, err)
	require.Equal(t, engineB.EngineID, info.EngineID)
}

func TestEngineOfflineAfterTTL(t *testing.T) {
	hub, _, clock := newTestHub(t)
	_, err := hub.Heartbeat(context.Background(), engineA, domain.EngineStatus{ComputeWorkers: 8, ExprVersion: "expr/1"})
	require.NoError(t, err)
	clock.Advance(time.Minute)

	info, status, ok, err := hub.Engine(context.Background())

	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, info.Online)
	require.Equal(t, int32(8), status.ComputeWorkers, "the last status is kept while offline")
	require.Equal(t, "expr/1", status.ExprVersion)
}

func TestEngineBeforeFirstHeartbeat(t *testing.T) {
	hub, _, _ := newTestHub(t)

	_, _, ok, err := hub.Engine(context.Background())

	require.NoError(t, err)
	require.False(t, ok)
}

func TestLatestRunRecomputesLag(t *testing.T) {
	hub, _, clock := newTestHub(t)
	period := clock.now.Add(-2 * time.Minute)
	_, err := hub.Heartbeat(context.Background(), engineA, domain.EngineStatus{RecentRuns: []domain.TaskRunSummary{
		{TaskID: "task_a", LastPeriodTime: period.Unix(), LastStatus: "complete", LagSeconds: 1},
	}})
	require.NoError(t, err)
	clock.Advance(time.Minute)

	run := hub.LatestRun("task_a")

	require.Equal(t, "complete", run.LastStatus)
	require.Equal(t, int64(180), run.LagSeconds)
	require.Equal(t, "task_b", hub.LatestRun("task_b").TaskID)
}

func TestHeartbeatRejectsSameEngineFromAnotherBoot(t *testing.T) {
	hub, _, clock := newTestHub(t)
	_, err := hub.Heartbeat(context.Background(), engineA, domain.EngineStatus{})
	require.NoError(t, err)
	copied := engineA
	copied.BootID = "boot-copy"

	_, err = hub.Heartbeat(context.Background(), copied, domain.EngineStatus{})
	require.ErrorIs(t, err, ErrLeaseConflict)

	clock.Advance(46 * time.Second)
	_, err = hub.Heartbeat(context.Background(), copied, domain.EngineStatus{})
	require.NoError(t, err, "a restarted engine takes over once the old boot's lease lapses")
}

func WithClock(now func() time.Time) Option { return func(h *Hub) { h.now = now } }
