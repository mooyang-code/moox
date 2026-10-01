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

func TestTimerBatchClaimerAcceptsRuntimeClaimWithoutNodeOrRegion(t *testing.T) {
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

	claimer := TimerBatchClaimer{Batches: db.TimerPeriodBatches(), CompletionTimeout: time.Minute}
	response, err := claimer.Claim(ctx, store.TimerPeriodBatchClaimInput{
		SpaceID: task.SpaceID, FunctionName: plan.Assignment.FunctionName, RequestID: "runtime-request",
		GroupID: uint32(plan.Assignment.GroupID), GroupCount: uint32(plan.Assignment.GroupCount),
		BindingHash: timerPeriodBindingHash(plan.Assignment), TickTime: now.Add(time.Second), CompletionTimeout: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, response.Claimed)
}

func TestTimerBatchClaimerTerminalReplaySurvivesPrunedWriteTarget(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Minute)
	plan := timerPlannerPlan(timerPlannerTask(), "timer-run", now)
	db, _ := createTimerClaimerPlan(t, plan, now)
	claimer := TimerBatchClaimer{Batches: db.TimerPeriodBatches(), CompletionTimeout: time.Minute}
	input := timerClaimerInput(plan, now.Add(time.Second), "timer-request")
	claimed, err := claimer.Claim(ctx, input)
	require.NoError(t, err)
	require.True(t, claimed.Claimed)

	batch, err := db.FetchBatches().Get(ctx, plan.Task.SpaceID, claimed.BatchID)
	require.NoError(t, err)
	batch.Status = domain.BatchStatusSucceeded
	completedAt := now.Add(time.Minute)
	batch.CompletedAt = &completedAt
	updated, err := db.FetchBatches().Complete(ctx, batch)
	require.NoError(t, err)
	require.True(t, updated)
	require.NoError(t, db.Tasks().SetEnabled(ctx, plan.Task.SpaceID, plan.Task.TaskID, false))
	pruned, err := db.TaskInstances().PruneDisabledWriteTargets(ctx, plan.Task.SpaceID)
	require.NoError(t, err)
	require.EqualValues(t, 1, pruned)

	replayed, err := claimer.Claim(ctx, input)
	require.NoError(t, err)
	require.False(t, replayed.Claimed, "terminal replay must remain a no-work response after owner target cleanup")
	require.Empty(t, replayed.RequestJSON)
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
	replayed, err := claimer.Claim(ctx, input)
	require.NoError(t, err)
	require.True(t, replayed.Claimed, "same request ID must replay the persisted frozen batch")
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

func TestTimerBatchClaimerRejectsPersistedPeriodDrift(t *testing.T) {
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
	wrongPeriod := now.Add(time.Minute).Format(time.RFC3339Nano)
	persisted.Items[0].TargetDataTime = wrongPeriod
	persisted.Targets[0].TargetDataTime = wrongPeriod
	corruptJSON, err := json.Marshal(persisted)
	require.NoError(t, err)
	setTimerBatchRequestJSON(t, dbPath, batchID, corruptJSON)

	claimer := TimerBatchClaimer{Batches: db.TimerPeriodBatches(), CompletionTimeout: time.Minute}
	input := timerClaimerInput(plan, now.Add(time.Second), "timer-request")
	_, err = claimer.Claim(ctx, input)
	require.ErrorContains(t, err, "manifest period")

	manifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, plan.Snapshot.Key, 0)
	require.NoError(t, err)
	require.Empty(t, manifest.ClaimRequestID, "invalid persisted JSON must not consume the manifest")
	batch, err = db.FetchBatches().Get(ctx, "crypto", batchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusPlanned, batch.Status, "invalid persisted JSON must not dispatch the batch")
}

func TestTimerBatchClaimerRejectsCorruptPersistedRequestBeforeClaim(t *testing.T) {
	tests := []struct {
		name       string
		multiItem  bool
		corrupt    func(*testing.T, []byte) []byte
		errMessage string
	}{
		{
			name: "schedule identity drift",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				request.ScheduleID = "tampered-schedule"
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "batch identity",
		},
		{
			name: "sync point identity drift",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				request.SyncPointID = "tampered-sync-point"
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "period identity",
		},
		{
			name: "unknown field rejected by worker decoder",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(raw, &request))
				request["unrecognized_field"] = json.RawMessage(`true`)
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "unknown field",
		},
		{
			name: "view target drift",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				request.Targets[0].ViewID = "tampered-view"
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "target",
		},
		{
			name: "output field target drift",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				request.Targets[0].OutputFields = `["close"]`
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "target",
		},
		{
			name: "subject identity drift",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				request.Items[0].SubjectID = "ETH-USDT"
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "frozen snapshot",
		},
		{
			name: "symbol identity drift",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				request.Items[0].Symbol = "ETHUSDT"
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "frozen snapshot",
		},
		{
			name: "provider request params drift",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				request.Items[0].BarLimit = 1
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "frozen task instance",
		},
		{
			name: "retry candidate index in initial request",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				request.Items[0].CandidateIndex = 1
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "retry execution state",
		},
		{
			name: "retry source event in initial request",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				request.Items[0].SourceEventID = "retry-event"
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "retry execution state",
		},
		{
			name: "retry rate budget in initial request",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				request.Items[0].RateBudgetRatio = 0.5
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "retry execution state",
		},
		{
			name: "blank item provider",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				request.Items[0].Provider = ""
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "route binding",
		},
		{
			name: "blank item source",
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				request.Items[0].SourceID = ""
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "route binding",
		},
		{
			name:      "frozen batch member removed",
			multiItem: true,
			corrupt: func(t *testing.T, raw []byte) []byte {
				var request Request
				require.NoError(t, json.Unmarshal(raw, &request))
				removedInstanceID := request.Items[1].InstanceID
				request.Items = append(request.Items[:1], request.Items[2:]...)
				targets := request.Targets[:0]
				for _, target := range request.Targets {
					if target.InstanceID != removedInstanceID {
						targets = append(targets, target)
					}
				}
				request.Targets = targets
				corrupt, err := json.Marshal(request)
				require.NoError(t, err)
				return corrupt
			},
			errMessage: "membership",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Minute)
			plan := timerPlannerPlan(timerPlannerTask(), "timer-run", now)
			if tt.multiItem {
				plan = timerPlannerTwoItemPlan(plan)
			}
			db, dbPath := createTimerClaimerPlan(t, plan, now)
			batchID := stableID("crypto", "bars", "1m", now.Format(time.RFC3339Nano), "0", "timer-initial")
			batch, err := db.FetchBatches().Get(ctx, "crypto", batchID)
			require.NoError(t, err)
			setTimerBatchRequestJSON(t, dbPath, batchID, tt.corrupt(t, []byte(batch.RequestJSON)))

			claimer := TimerBatchClaimer{Batches: db.TimerPeriodBatches(), CompletionTimeout: time.Minute}
			_, err = claimer.Claim(ctx, timerClaimerInput(plan, now.Add(time.Second), "timer-request"))
			require.Error(t, err)
			require.ErrorContains(t, err, tt.errMessage)

			manifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, plan.Snapshot.Key, 0)
			require.NoError(t, err)
			require.Empty(t, manifest.ClaimRequestID, "invalid persisted JSON must not consume the manifest")
			batch, err = db.FetchBatches().Get(ctx, "crypto", batchID)
			require.NoError(t, err)
			require.Equal(t, domain.BatchStatusPlanned, batch.Status, "invalid persisted JSON must not dispatch the batch")
		})
	}
}

func TestTimerBatchClaimerRejectsPersistedTaskInstanceIdentityDrift(t *testing.T) {
	tests := []struct {
		name   string
		column string
		value  any
	}{
		{name: "run owner", column: "c_run_id", value: "other-run"},
		{name: "logical provider", column: "c_provider", value: "other-provider"},
		{name: "series tag", column: "c_series_tag", value: "other-tag"},
		{name: "request key", column: "c_request_key", value: "other-request"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Minute)
			plan := timerPlannerPlan(timerPlannerTask(), "timer-run", now)
			db, dbPath := createTimerClaimerPlan(t, plan, now)
			setTimerTaskInstanceField(t, dbPath, plan.Request.Items[0].InstanceID, tt.column, tt.value)

			claimer := TimerBatchClaimer{Batches: db.TimerPeriodBatches(), CompletionTimeout: time.Minute}
			_, err := claimer.Claim(ctx, timerClaimerInput(plan, now.Add(time.Second), "timer-request"))
			require.ErrorContains(t, err, "frozen task instance")

			manifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, plan.Snapshot.Key, 0)
			require.NoError(t, err)
			require.Empty(t, manifest.ClaimRequestID)
			batch, err := db.FetchBatches().Get(ctx, "crypto", stableID("crypto", "bars", "1m", now.Format(time.RFC3339Nano), "0", "timer-initial"))
			require.NoError(t, err)
			require.Equal(t, domain.BatchStatusPlanned, batch.Status)
		})
	}
}

func TestTimerBatchClaimerAllowsAssignmentRefreshAfterPlanning(t *testing.T) {
	ctx := context.Background()
	db := newTimerClaimerStore(t)
	now := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Minute)
	task := timerPlannerTask()
	require.NoError(t, db.Tasks().Create(ctx, task))
	run, err := db.Runs().Create(ctx, task.SpaceID, "scheduled", "1m", &now)
	require.NoError(t, err)
	plan := timerPlannerPlan(task, run.RunID, now)
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
	require.NoError(t, db.TaskInstances().ReplaceMarketFetchAssignments(ctx, task.SpaceID, []string{plan.Assignment.FunctionName}, []store.MarketFetchAssignment{{
		Provider: "binance", SourceID: "spot_http_next", MarketType: "spot", DatasetID: "bars", Frequency: "1m",
		FunctionName: "market-fetch-next", Subjects: []string{"BTC-USDT"},
	}}))

	claimer := TimerBatchClaimer{Batches: db.TimerPeriodBatches(), CompletionTimeout: time.Minute}
	response, err := claimer.Claim(ctx, timerClaimerInput(plan, now.Add(time.Second), "timer-request"))
	require.NoError(t, err)
	require.True(t, response.Claimed, "normal reconciliation updates source/function assignment but not the frozen request key")
}

func TestTimerBatchClaimerAcceptsLogicalStockCNRouteWithActualProvider(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Minute)
	plan := timerPlannerPlan(timerPlannerTask(), "timer-run", now)
	entry := &plan.Snapshot.Entries[0]
	entry.SeriesKey = domain.CanonicalSeriesKey("stockcn_multi", "stockcn", "equity", "000001.SZ", "default")
	entry.SubjectID = "000001.SZ"
	entry.Provider = "stockcn_multi"
	entry.SourceID = "stockcn"
	entry.MarketType = "equity"
	entry.ProviderSymbol = "000001"
	entry.SeriesTag = "default"
	plan.Snapshot.SeriesHash = domain.SeriesSetHash([]string{entry.SeriesKey})
	entry.SeriesHash = plan.Snapshot.SeriesHash
	plan.Request.Provider = "sina"
	plan.Request.SourceID = "stockcn_minute_http"
	plan.Request.RouteProvider = "stockcn_multi"
	plan.Request.MarketType = "equity"
	plan.Request.Items[0].SubjectID = "000001.SZ"
	plan.Request.Items[0].Symbol = "000001"
	plan.Request.Items[0].Provider = "sina"
	plan.Request.Items[0].SourceID = "stockcn_minute_http"
	plan.Request.Items[0].MarketType = "equity"
	plan.Request.Items[0].SeriesHash = plan.Snapshot.SeriesHash
	plan.Instances[0].SubjectID = "000001.SZ"
	plan.Instances[0].ProviderSymbol = "000001"
	plan.Instances[0].Provider = "stockcn_multi"
	plan.Instances[0].SourceID = "stockcn"
	plan.Instances[0].MarketType = "equity"
	plan.Instances[0].SeriesTag = "default"
	logicalItem := plan.Request.Items[0]
	logicalItem.Provider = "stockcn_multi"
	logicalItem.SourceID = "stockcn"
	plan.Instances[0].RequestKey = sharedCollectionItemKey(logicalItem, "1m", now)
	plan.Assignment.Provider = "sina"
	plan.Assignment.RouteProvider = "stockcn_multi"
	plan.Assignment.SourceID = "stockcn_minute_http"
	plan.Assignment.MarketType = "equity"

	db, _ := createTimerClaimerPlan(t, plan, now)
	claimer := TimerBatchClaimer{Batches: db.TimerPeriodBatches(), CompletionTimeout: time.Minute}
	response, err := claimer.Claim(ctx, timerClaimerInput(plan, now.Add(time.Second), "timer-request"))
	require.NoError(t, err)
	require.True(t, response.Claimed, "logical frozen route identity is distinct from the selected actual provider")
}

func TestTimerBatchClaimerIgnoresSiblingTaskWriteTarget(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Minute)
	plan := timerPlannerPlan(timerPlannerTask(), "timer-run", now)
	db, _ := createTimerClaimerPlan(t, plan, now)
	siblingTask := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-b", TaskName: "Task B", DataType: "kline", CollectParams: `{"frequency":"1m"}`, Enabled: true}
	require.NoError(t, db.Tasks().Create(ctx, siblingTask))
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{{
		ID: "target-sibling", SpaceID: "crypto", InstanceID: plan.Request.Items[0].InstanceID,
		TaskID: siblingTask.TaskID, DatasetID: "bars-sibling", SeriesIndex: 0,
		SeriesHash: plan.Snapshot.SeriesHash, ExpectedCount: plan.Snapshot.ExpectedCount, Status: "pending",
	}}))

	claimer := TimerBatchClaimer{Batches: db.TimerPeriodBatches(), CompletionTimeout: time.Minute}
	response, err := claimer.Claim(ctx, timerClaimerInput(plan, now.Add(time.Second), "timer-request"))
	require.NoError(t, err)
	require.True(t, response.Claimed)
}

func TestTimerBatchClaimerRejectsClaimedManifestWithPlannedBatch(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Minute)
	plan := timerPlannerPlan(timerPlannerTask(), "timer-run", now)
	db, dbPath := createTimerClaimerPlan(t, plan, now)
	batchID := stableID("crypto", "bars", "1m", now.Format(time.RFC3339Nano), "0", "timer-initial")
	input := timerClaimerInput(plan, now.Add(time.Second), "timer-request")
	setTimerManifestClaim(t, dbPath, batchID, input.RequestID, now)

	claimer := TimerBatchClaimer{Batches: db.TimerPeriodBatches(), CompletionTimeout: time.Minute}
	_, err := claimer.Claim(ctx, input)
	require.Error(t, err)
	require.ErrorContains(t, err, "claim state")
	batch, err := db.FetchBatches().Get(ctx, "crypto", batchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusPlanned, batch.Status)
}

func timerClaimerInput(plan TimerPeriodPlan, tick time.Time, requestID string) store.TimerPeriodBatchClaimInput {
	return store.TimerPeriodBatchClaimInput{
		SpaceID: plan.Task.SpaceID, FunctionName: plan.Assignment.FunctionName, RequestID: requestID,
		GroupID: uint32(plan.Assignment.GroupID), GroupCount: uint32(plan.Assignment.GroupCount),
		BindingHash: timerPeriodBindingHash(plan.Assignment), TickTime: tick,
		NodeID: plan.Assignment.NodeID, Region: plan.Assignment.Region, CompletionTimeout: time.Minute,
	}
}

func createTimerClaimerPlan(t *testing.T, plan TimerPeriodPlan, now time.Time) (*store.Store, string) {
	t.Helper()
	ctx := context.Background()
	db, dbPath := newTimerClaimerStoreWithPath(t)
	require.NoError(t, db.Tasks().Create(ctx, plan.Task))
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
	return db, dbPath
}

func timerPlannerTwoItemPlan(plan TimerPeriodPlan) TimerPeriodPlan {
	first := plan.Snapshot.Entries[0]
	secondKey := domain.CanonicalSeriesKey("binance", "spot_http", "spot", "ETH-USDT", "")
	seriesHash := domain.SeriesSetHash([]string{first.SeriesKey, secondKey})
	plan.Snapshot.SeriesHash = seriesHash
	plan.Snapshot.ExpectedCount = 2
	plan.Snapshot.Entries[0].SeriesHash = seriesHash
	plan.Snapshot.Entries[0].ExpectedCount = 2
	plan.Snapshot.Entries = append(plan.Snapshot.Entries, domain.PeriodSeriesSnapshotEntry{
		SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: plan.Snapshot.Key.PeriodTime, SeriesIndex: 1,
		SeriesKey: secondKey, SubjectID: "ETH-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot",
		ProviderSymbol: "ETHUSDT", SeriesHash: seriesHash, ExpectedCount: 2,
	})
	plan.Request.Items[0].SeriesHash = seriesHash
	plan.Request.Items[0].ExpectedCount = 2
	plan.Request.Items = append(plan.Request.Items, domain.CollectionItem{
		InstanceID: "instance-new", SubjectID: "ETH-USDT", Symbol: "ETHUSDT", TargetDataTime: plan.Snapshot.Key.PeriodTime.Format(time.RFC3339Nano),
		Provider: "binance", SourceID: "spot_http", MarketType: "spot", DataType: "kline", DatasetID: "bars", Frequency: "1m",
		SeriesIndex: 1, SeriesHash: seriesHash, ExpectedCount: 2,
	})
	plan.Assignment.Subjects = append(plan.Assignment.Subjects, "ETH-USDT")
	targetTime := plan.Snapshot.Key.PeriodTime.UTC()
	plan.Instances = append(plan.Instances, domain.TaskInstance{
		SpaceID: "crypto", InstanceID: "instance-new", RunID: plan.RunID, RequestKey: sharedCollectionItemKey(plan.Request.Items[1], "1m", plan.Snapshot.Key.PeriodTime), Provider: "binance", ProviderSymbol: "ETHUSDT", SourceID: "spot_http",
		MarketType: "spot", DataType: "kline", SubjectID: "ETH-USDT", Frequency: "1m", TargetDataTime: &targetTime, TaskParams: `{}`,
	})
	plan.Targets[0].SeriesHash = seriesHash
	plan.Targets[0].ExpectedCount = 2
	plan.Targets = append(plan.Targets, domain.WriteTarget{
		ID: "target-new", SpaceID: "crypto", InstanceID: "instance-new", TaskID: plan.Task.TaskID, DatasetID: "bars",
		SeriesIndex: 1, SeriesHash: seriesHash, ExpectedCount: 2, Status: "pending",
	})
	return plan
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

func setTimerManifestClaim(t *testing.T, dbPath, batchID, requestID string, claimedAt time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	result := db.Table("t_collector_timer_period_batches").Where("c_space_id = ? AND c_batch_id = ?", "crypto", batchID).
		Updates(map[string]any{"c_claim_request_id": requestID, "c_claimed_at": claimedAt})
	require.NoError(t, result.Error)
	require.EqualValues(t, 1, result.RowsAffected)
}

func setTimerTaskInstanceField(t *testing.T, dbPath, instanceID, column string, value any) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	result := db.Table("t_collector_task_instances").Where("c_space_id = ? AND c_instance_id = ?", "crypto", instanceID).Update(column, value)
	require.NoError(t, result.Error)
	require.EqualValues(t, 1, result.RowsAffected)
}
