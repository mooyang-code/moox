package marketfetch

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

func TestTimerBatchClaimerRejectsMismatchedPersistedBatchID(t *testing.T) {
	ctx := context.Background()
	db, dbPath := newTimerClaimerStoreWithPath(t)
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
	batchID := stableID("crypto", "bars", "1m", now.Format(time.RFC3339Nano), "0", "timer-initial")
	batch, err := db.FetchBatches().Get(ctx, "crypto", batchID)
	require.NoError(t, err)

	var persisted Request
	require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &persisted))
	validJSON, err := json.Marshal(persisted)
	require.NoError(t, err)
	persisted.BatchID = " " + batchID + " "
	corruptJSON, err := json.Marshal(persisted)
	require.NoError(t, err)
	setTimerBatchRequestJSON(t, dbPath, batchID, corruptJSON)

	claimer := TimerBatchClaimer{Batches: db.TimerPeriodBatches(), CompletionTimeout: time.Minute}
	input := timerClaimerInput(plan, now.Add(time.Second), "timer-request")
	_, err = claimer.Claim(ctx, input)
	require.ErrorContains(t, err, "batch_id")

	// Restore a valid candidate, claim it, then corrupt its dispatched request to
	// exercise the idempotent replay path with the same request ID.
	setTimerBatchRequestJSON(t, dbPath, batchID, validJSON)
	claimed, err := claimer.Claim(ctx, input)
	require.NoError(t, err)
	require.True(t, claimed.Claimed)
	var claimedRequest Request
	require.NoError(t, json.Unmarshal(claimed.RequestJSON, &claimedRequest))
	claimedRequest.BatchID = " " + batchID + " "
	corruptReplayJSON, err := json.Marshal(claimedRequest)
	require.NoError(t, err)
	setTimerBatchRequestJSON(t, dbPath, batchID, corruptReplayJSON)
	_, err = claimer.Claim(ctx, input)
	require.ErrorContains(t, err, "batch_id")

	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_ID", "0")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_COUNT", "1")
	t.Setenv("MOOX_MARKET_FETCH_BINDING_HASH", timerPeriodBindingHash(plan.Assignment))
	t.Setenv("MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "runtime.local:11003")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_TARGET_NODE", "collector-node")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "storage.local:11003")
	var storageCalls, executeCalls int
	handler := &Handler{
		TimerRuntimeClient: timerRuntimeClientFunc(func(callCtx context.Context, request *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error) {
			claimInput := store.TimerPeriodBatchClaimInput{
				SpaceID: request.GetSpaceId(), FunctionName: request.GetFunctionName(), RequestID: request.GetRequestId(),
				GroupID: request.GetGroupId(), GroupCount: request.GetGroupCount(), BindingHash: request.GetBindingHash(),
				TickTime: time.Unix(request.GetTickTime(), 0).UTC(), NodeID: "timer-node", Region: "ap-singapore", CompletionTimeout: time.Minute,
			}
			result, claimErr := claimer.Claim(callCtx, claimInput)
			if claimErr != nil {
				return &collectorpb.ClaimTimerBatchRsp{RetInfo: &collectorpb.RetInfo{Code: collectorpb.ErrorCode_INNER_ERR, Msg: claimErr.Error()}}, nil
			}
			return &collectorpb.ClaimTimerBatchRsp{RetInfo: &collectorpb.RetInfo{Code: collectorpb.ErrorCode_SUCCESS}, Claimed: result.Claimed, RequestJson: result.RequestJSON}, nil
		}),
		NewStorage: func(string, string, string) (Storage, error) { storageCalls++; return timerHandlerStorage{}, nil },
		Execute: func(context.Context, Request, Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
			executeCalls++
			return &marketfetchpb.MarketFetchBatchCompleted{Status: "succeeded"}, nil
		},
	}
	response, err := handler.HandleTimerAt(ctx, "timer-request", plan.Assignment.FunctionName, now.Add(2*time.Second))
	require.NoError(t, err)
	require.False(t, response.Success)
	require.Contains(t, response.Message, "batch_id")
	require.Zero(t, storageCalls, "a corrupted claimed request must fail before creating the Storage writer")
	require.Zero(t, executeCalls, "a corrupted claimed request must fail before provider execution")
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
	db, _ := newTimerClaimerStoreWithPath(t)
	return db
}

func newTimerClaimerStoreWithPath(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "collector.db")
	db, err := store.Open(&store.Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func setTimerBatchRequestJSON(t *testing.T, dbPath, batchID string, requestJSON []byte) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	result := db.Table("t_collector_fetch_batches").Where("c_space_id = ? AND c_batch_id = ?", "crypto", batchID).Update("c_request_json", string(requestJSON))
	require.NoError(t, result.Error)
	require.EqualValues(t, 1, result.RowsAffected)
}
