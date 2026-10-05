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

func seedSet(t *testing.T, db *store.Store, setID string, members map[string]string) domain.FactorSet {
	t.Helper()
	ctx := context.Background()
	set := domain.FactorSet{
		SetID: setID, SpaceID: "crypto", SourceDatasetID: "dataset_" + setID, Freq: "1m",
		SubjectMode: domain.SubjectModeAll, Subjects: []string{}, ResultDatasetID: "dataset_factor_" + setID,
		Status: domain.SetStatusEnabled,
	}
	require.NoError(t, db.CreateSet(ctx, set))
	for factorID, status := range members {
		if _, err := db.GetFactor(ctx, factorID); err != nil {
			require.NoError(t, db.CreateFactor(ctx, testFactor(factorID)))
		}
		_, err := db.AddMember(ctx, setID, factorID)
		require.NoError(t, err)
		if status == domain.MemberStatusEnabled {
			require.NoError(t, db.SetMemberStatus(ctx, setID, factorID, domain.MemberStatusDisabled, domain.MemberStatusEnabled))
		}
	}
	return set
}

func testFactor(factorID string) domain.FactorDef {
	source := "def compute(frame, params, context): return frame # " + factorID
	return domain.FactorDef{
		FactorID: factorID, Name: factorID, FactorType: domain.FactorTypeTimeSeries,
		SourceCode: source, SourceHash: domain.SourceHash(source),
		InputColumns: []string{"close"}, Outputs: []string{factorID + "_value"}, ParamsJSON: "{}", LookbackPeriods: 1,
	}
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
	_, err := hub.Heartbeat(context.Background(), engineA, domain.EngineStatus{PythonWorkers: 8})
	require.NoError(t, err)
	clock.Advance(time.Minute)

	info, status, ok, err := hub.Engine(context.Background())

	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, info.Online)
	require.Equal(t, int32(8), status.PythonWorkers, "the last status is kept while offline")
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
	_, err := hub.Heartbeat(context.Background(), engineA, domain.EngineStatus{RecentRuns: []domain.SetRunSummary{
		{SetID: "set_a", LastPeriodTime: period.Unix(), LastStatus: "complete", LagSeconds: 1},
	}})
	require.NoError(t, err)
	clock.Advance(time.Minute)

	run := hub.LatestRun("set_a")

	require.Equal(t, "complete", run.LastStatus)
	require.Equal(t, int64(180), run.LagSeconds)
	require.Equal(t, "set_b", hub.LatestRun("set_b").SetID)
}
