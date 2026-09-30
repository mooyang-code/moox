package marketfetch

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func TestTimerBatchClaimerReturnsPersistedPeriodIdentity(t *testing.T) {
	ctx := context.Background()
	db := newTimerClaimerStore(t)
	now := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Minute)
	task := timerPlannerTask()
	require.NoError(t, db.Tasks().Create(ctx, task))
	plan := timerPlannerPlan(task, "timer-run", now)
	planner := TimerPeriodPlanner{
		Snapshots: db.PeriodSeriesSnapshot(), States: db.PeriodStorageStates(), Batches: db.TimerPeriodBatches(),
		Now: func() time.Time { return now },
		EnsureStorage: func(_ context.Context, snapshot domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error) {
			return timerPlannerStorageState(snapshot, now.Add(time.Hour)), nil
		},
	}
	created, err := planner.Plan(ctx, plan)
	require.NoError(t, err)
	require.True(t, created)

	input := timerClaimerInput(plan, now.Add(time.Second), "timer-request")
	claimer := TimerBatchClaimer{Batches: db.TimerPeriodBatches(), CompletionTimeout: time.Minute}
	response, err := claimer.Claim(ctx, input)
	require.NoError(t, err)
	require.True(t, response.Claimed)
	require.Equal(t, stableID("crypto", "bars", "1m", now.Format(time.RFC3339Nano), "0", "timer-initial"), response.BatchID)
	require.Equal(t, now.Add(time.Hour), response.PeriodDeadlineAt)

	var returned Request
	require.NoError(t, json.Unmarshal(response.RequestJSON, &returned))
	require.Equal(t, "timer-request", returned.RequestID)
	require.Equal(t, plan.Request.Items, returned.Items, "the claimer returns persisted items, not a request rebuilt from current tags")
	require.Len(t, returned.Targets, 1)
	require.Equal(t, "target-old", returned.Targets[0].ID)
	require.Equal(t, "1m", returned.Targets[0].Frequency)
	require.Equal(t, now.Format(time.RFC3339Nano), returned.Targets[0].TargetDataTime)

	batch, err := db.FetchBatches().Get(ctx, "crypto", response.BatchID)
	require.NoError(t, err)
	require.Equal(t, string(response.RequestJSON), batch.RequestJSON)
	require.Equal(t, "timer-request", batch.RequestID)
}

func TestTimerBatchClaimerWithNoCandidateReturnsEmptyWork(t *testing.T) {
	db := newTimerClaimerStore(t)
	claimer := TimerBatchClaimer{Batches: db.TimerPeriodBatches(), CompletionTimeout: time.Minute}
	response, err := claimer.Claim(context.Background(), store.TimerPeriodBatchClaimInput{
		SpaceID: "crypto", FunctionName: "market-fetch", RequestID: "empty-request", GroupID: 0, GroupCount: 1,
		BindingHash: "binding-v1", TickTime: time.Now().UTC(), NodeID: "timer-node", Region: "ap-singapore", CompletionTimeout: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, response.Claimed)
	require.Empty(t, response.RequestJSON)
	require.Zero(t, response.PeriodDeadlineAt)
}

func timerClaimerInput(plan TimerPeriodPlan, tick time.Time, requestID string) store.TimerPeriodBatchClaimInput {
	return store.TimerPeriodBatchClaimInput{
		SpaceID: plan.Task.SpaceID, FunctionName: plan.Assignment.FunctionName, RequestID: requestID,
		GroupID: uint32(plan.Assignment.GroupID), GroupCount: uint32(plan.Assignment.GroupCount),
		BindingHash: timerPeriodBindingHash(plan.Assignment), TickTime: tick,
		NodeID: plan.Assignment.NodeID, Region: plan.Assignment.Region, CompletionTimeout: time.Minute,
	}
}

func newTimerClaimerStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}
