package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestTimerPeriodBatchClaimReplayAndTerminalNoWork(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	seedTimerPeriodBatch(t, s, "old", "run-old", period)

	input := timerBatchClaimInput(period.Add(time.Second), "request-one")
	first, err := s.TimerPeriodBatches().Claim(ctx, input)
	require.NoError(t, err)
	require.True(t, first.Claimed)
	require.Equal(t, "batch-old", first.BatchID)
	require.Contains(t, string(first.RequestJSON), `"request_id":"request-one"`)

	replay, err := s.TimerPeriodBatches().Claim(ctx, input)
	require.NoError(t, err)
	require.Equal(t, first, replay, "lost response replay must return the same durable batch")

	batch, err := s.FetchBatches().Get(ctx, "crypto", first.BatchID)
	require.NoError(t, err)
	batch.Status = domain.BatchStatusSucceeded
	completed := time.Now().UTC()
	batch.CompletedAt = &completed
	updated, err := s.FetchBatches().Complete(ctx, batch)
	require.NoError(t, err)
	require.True(t, updated)

	terminalReplay, err := s.TimerPeriodBatches().Claim(ctx, input)
	require.NoError(t, err)
	require.False(t, terminalReplay.Claimed, "terminal request replay must not return work")
	require.Empty(t, terminalReplay.RequestJSON)
}

func TestTimerPeriodBatchRejectsNegativeGroupAndShardIndexes(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	seedTimerPeriodBatch(t, s, "nonnegative", "run-nonnegative", period)
	manifest, err := s.TimerPeriodBatches().GetByPeriod(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}, 0)
	require.NoError(t, err)

	require.Error(t, s.db.Exec(`UPDATE t_collector_timer_period_batches SET c_group_id = -1 WHERE c_key = ?`, manifest.Key).Error)
	require.Error(t, s.db.Exec(`UPDATE t_collector_timer_period_batches SET c_shard_index = -1 WHERE c_key = ?`, manifest.Key).Error)
}

func TestTimerPeriodBatchClaimRejectsBackdatedTickAfterCanonicalDeadline(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Now().UTC().Truncate(time.Minute)
	seedTimerPeriodBatch(t, s, "deadline", "run-deadline", period)
	manifest, err := s.TimerPeriodBatches().GetByPeriod(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}, 0)
	require.NoError(t, err)

	repo := s.TimerPeriodBatches()
	repo.now = func() time.Time { return manifest.DeadlineAt.Add(time.Second) }
	input := timerBatchClaimInput(manifest.DeadlineAt.Add(-time.Second), "late-backdated-request")
	claimed, err := repo.Claim(ctx, input)
	require.NoError(t, err)
	require.False(t, claimed.Claimed, "caller tick_time cannot extend a canonical manifest deadline")
	require.Empty(t, claimed.RequestJSON)

	storedManifest, err := repo.GetByPeriod(ctx, domain.PeriodKey{SpaceID: manifest.SpaceID, DatasetID: manifest.DatasetID, Frequency: manifest.Frequency, PeriodTime: manifest.PeriodTime}, 0)
	require.NoError(t, err)
	require.Empty(t, storedManifest.ClaimRequestID)
	batch, err := s.FetchBatches().Get(ctx, "crypto", manifest.BatchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusPlanned, batch.Status)
}

func TestTimerPeriodBatchDistinctRequestsOnlyOneWins(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	seedTimerPeriodBatch(t, s, "only", "run-only", period)

	const contenders = 12
	var wg sync.WaitGroup
	results := make(chan TimerPeriodBatchClaimResult, contenders)
	errs := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input := timerBatchClaimInput(period.Add(time.Second), fmt.Sprintf("request-%02d", i))
			result, err := s.TimerPeriodBatches().Claim(ctx, input)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	winners := 0
	for result := range results {
		if result.Claimed {
			winners++
		}
	}
	require.Equal(t, 1, winners)
}

func TestTimerPeriodBatchClaimsOldestThenNextOldest(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	seedTimerPeriodBatch(t, s, "old", "run-old", period)
	seedTimerPeriodBatch(t, s, "next", "run-next", period.Add(time.Minute))

	first, err := s.TimerPeriodBatches().Claim(ctx, timerBatchClaimInput(period.Add(30*time.Second), "request-old"))
	require.NoError(t, err)
	require.True(t, first.Claimed)
	require.Equal(t, "batch-old", first.BatchID)
	second, err := s.TimerPeriodBatches().Claim(ctx, timerBatchClaimInput(period.Add(30*time.Second), "request-next"))
	require.NoError(t, err)
	require.True(t, second.Claimed)
	require.Equal(t, "batch-next", second.BatchID)
}

func TestTimerPeriodBatchContinuousNewPeriodsCannotStarveOldEligibleWork(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	seedTimerPeriodBatch(t, s, "old", "run-old", period)
	seedTimerPeriodBatch(t, s, "next", "run-next", period.Add(time.Minute))

	first, err := s.TimerPeriodBatches().Claim(ctx, timerBatchClaimInput(period.Add(30*time.Second), "request-old"))
	require.NoError(t, err)
	require.Equal(t, "batch-old", first.BatchID)
	for index := 0; index < 5; index++ {
		seedTimerPeriodBatch(t, s, fmt.Sprintf("new-%d", index), fmt.Sprintf("run-new-%d", index), period.Add(time.Duration(index+2)*time.Minute))
	}
	second, err := s.TimerPeriodBatches().Claim(ctx, timerBatchClaimInput(period.Add(90*time.Second), "request-next"))
	require.NoError(t, err)
	require.True(t, second.Claimed)
	require.Equal(t, "batch-next", second.BatchID, "newly enqueued periods must not leapfrog older eligible work")
}

func TestTimerPeriodBatchClaimRejectsTaskUpdatedBeforeSchedulerCancellation(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	seedTimerPeriodBatch(t, s, "updated-before-claim", "run-updated", period)
	updatedAt := time.Now().UTC().Add(time.Minute)
	require.NoError(t, s.db.Model(&domain.CollectionTask{}).
		Where("c_space_id = ? AND c_task_id = ?", "crypto", "task-updated-before-claim").
		Update("c_mtime", updatedAt).Error)

	claimed, err := s.TimerPeriodBatches().Claim(ctx, timerBatchClaimInput(period.Add(time.Second), "request-before-tick"))
	require.NoError(t, err)
	require.False(t, claimed.Claimed, "a stale unclaimed manifest must not race the next scheduler cancellation tick")
	require.Empty(t, claimed.RequestJSON)
	canceled, err := s.TimerPeriodBatches().CancelStaleUnclaimed(ctx, "crypto")
	require.NoError(t, err)
	require.EqualValues(t, 1, canceled)
	batch, err := s.FetchBatches().Get(ctx, "crypto", "batch-updated-before-claim")
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusFailed, batch.Status)
}

func TestTimerPeriodBatchCreateManyRejectsIncompleteOrOversizedSetWithoutWrites(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		planCount int
		oversized bool
		wantError string
	}{
		{name: "incomplete frozen series coverage", planCount: 1, wantError: "does not cover every frozen series exactly once"},
		{name: "request size limit", planCount: 2, oversized: true, wantError: "exceeds"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := newCollectorStore(t)
			ctx := context.Background()
			period := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
			require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{
				SpaceID: "crypto", TaskID: "task-create-many", TaskName: "task-create-many",
				DataType: "kline", CollectParams: `{"frequency":"1m"}`, Enabled: true,
			}))
			plans := make([]TimerPeriodBatchPlan, 2)
			for groupID := uint32(0); groupID < 2; groupID++ {
				instanceID := fmt.Sprintf("instance-create-many-%d", groupID)
				batchID := fmt.Sprintf("batch-create-many-%d", groupID)
				requestJSON := `{}`
				if testCase.oversized && groupID == 1 {
					requestJSON = `{"padding":"` + strings.Repeat("x", maxTimerBatchRequestSize) + `"}`
				}
				plans[groupID] = TimerPeriodBatchPlan{
					Manifest: &domain.TimerPeriodBatch{
						Key: fmt.Sprintf("manifest-create-many-%d", groupID), SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period,
						TaskID: "task-create-many", FirstRunID: "run-create-many", SeriesHash: "two-series", ExpectedCount: 2,
						GroupID: groupID, GroupCount: 2, ShardIndex: groupID, BindingHash: "binding", RouteVersion: "route-v1",
						BatchID: batchID, FunctionName: "market-fetch", NodeID: fmt.Sprintf("timer-%d", groupID), Region: "region", DeadlineAt: period.Add(time.Hour),
					},
					Batch: &domain.BatchInvocation{
						SpaceID: "crypto", BatchID: batchID, ScheduleID: batchID, BatchKind: domain.BatchKindRealtime,
						ShardIndex: int(groupID), Frequency: "1m", Status: domain.BatchStatusPlanned, RequestJSON: requestJSON, PlannedCount: 1,
					},
					Instances: []domain.TaskInstance{{
						SpaceID: "crypto", InstanceID: instanceID, RunID: "run-create-many", DataType: "kline", SubjectID: fmt.Sprintf("subject-%d", groupID), Frequency: "1m",
					}},
					Targets: []domain.WriteTarget{{
						ID: fmt.Sprintf("target-create-many-%d", groupID), SpaceID: "crypto", InstanceID: instanceID,
						TaskID: "task-create-many", DatasetID: "bars", SeriesIndex: groupID, SeriesHash: "two-series", ExpectedCount: 2,
					}},
				}
			}
			_, err := s.TimerPeriodBatches().CreateMany(ctx, plans[:testCase.planCount])
			require.ErrorContains(t, err, testCase.wantError)
			manifests, err := s.TimerPeriodBatches().ListByPeriod(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period})
			require.NoError(t, err)
			require.Empty(t, manifests)
			instances, _, err := s.TaskInstances().List(ctx, TaskInstanceFilter{SpaceID: "crypto", CollectionTaskID: "task-create-many", PageSize: 10})
			require.NoError(t, err)
			require.Empty(t, instances)
			for groupID := uint32(0); groupID < 2; groupID++ {
				_, batchErr := s.FetchBatches().Get(ctx, "crypto", fmt.Sprintf("batch-create-many-%d", groupID))
				require.Error(t, batchErr)
			}
		})
	}
}

func TestTimerPeriodBatchCreateManyRejectsTaskModifiedAfterRunCutoff(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-cutoff", TaskName: "task-cutoff", DataType: "kline", Enabled: true}
	require.NoError(t, s.Tasks().Create(ctx, task))
	owner, err := s.Tasks().GetByTaskID(ctx, "crypto", task.TaskID)
	require.NoError(t, err)
	runCutoff := owner.ModifyTime.UTC().Add(time.Second)
	staleModifyTime := runCutoff.Add(time.Second)
	require.NoError(t, s.db.Model(&domain.CollectionTask{}).
		Where("c_space_id = ? AND c_task_id = ?", "crypto", task.TaskID).
		Update("c_mtime", staleModifyTime).Error)

	period := time.Now().UTC().Truncate(time.Minute)
	manifest := &domain.TimerPeriodBatch{
		Key: "manifest-cutoff", SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period,
		TaskID: task.TaskID, FirstRunID: "run-cutoff", SeriesHash: "one-series", ExpectedCount: 1,
		GroupID: 0, GroupCount: 1, ShardIndex: 0, BindingHash: "binding", RouteVersion: "route-v1",
		BatchID: "batch-cutoff", FunctionName: "market-fetch", NodeID: "timer-node", Region: "region", DeadlineAt: period.Add(time.Hour),
	}
	batch := &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: manifest.BatchID, ScheduleID: manifest.BatchID, BatchKind: domain.BatchKindRealtime,
		Frequency: "1m", Status: domain.BatchStatusPlanned, RequestJSON: `{}`, PlannedCount: 1,
	}
	plan := TimerPeriodBatchPlan{
		Manifest: manifest, Batch: batch, OwnerRunCutoff: runCutoff, OwnerTaskModifyTime: owner.ModifyTime,
		Instances: []domain.TaskInstance{{SpaceID: "crypto", InstanceID: "instance-cutoff", RunID: manifest.FirstRunID, DataType: "kline", SubjectID: "BTC-USDT"}},
		Targets:   []domain.WriteTarget{{ID: "target-cutoff", SpaceID: "crypto", InstanceID: "instance-cutoff", TaskID: task.TaskID, DatasetID: "bars", SeriesIndex: 0, SeriesHash: "one-series", ExpectedCount: 1}},
	}
	created, err := s.TimerPeriodBatches().CreateMany(ctx, []TimerPeriodBatchPlan{plan})
	require.NoError(t, err)
	require.Zero(t, created, "an owner update after the run cutoff must invalidate this first-claim plan")
	manifests, err := s.TimerPeriodBatches().ListByPeriod(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period})
	require.NoError(t, err)
	require.Empty(t, manifests)
	_, err = s.FetchBatches().Get(ctx, "crypto", manifest.BatchID)
	require.Error(t, err)
	instances, _, err := s.TaskInstances().List(ctx, TaskInstanceFilter{SpaceID: "crypto", CollectionTaskID: task.TaskID, PageSize: 10})
	require.NoError(t, err)
	require.Empty(t, instances, "stale owner plans must not persist instances or targets")
	targets, err := s.TaskInstances().ListWriteTargets(ctx, "crypto", "instance-cutoff")
	require.NoError(t, err)
	require.Empty(t, targets, "stale owner plans must not persist write targets")
}

func TestTimerPeriodBatchCreateManyUsesTaskInstanceLifecycleUpsert(t *testing.T) {
	for _, status := range []int{domain.InstanceStatusSuccess, domain.InstanceStatusFailed} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := newCollectorStore(t)
			ctx := context.Background()
			task := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-lifecycle", TaskName: "Lifecycle", DataType: "kline", Enabled: true}
			require.NoError(t, s.Tasks().Create(ctx, task))
			period := time.Now().UTC().Truncate(time.Minute)
			instanceID := "shared-lifecycle-instance"
			makePlan := func(period time.Time, suffix string) TimerPeriodBatchPlan {
				targetTime := period.UTC()
				manifest := &domain.TimerPeriodBatch{
					Key: "manifest-lifecycle-" + suffix, SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period,
					TaskID: task.TaskID, FirstRunID: "run-lifecycle-" + suffix, SeriesHash: "one-series", ExpectedCount: 1,
					GroupID: 0, GroupCount: 1, ShardIndex: 0, BindingHash: "binding", RouteVersion: "route-v1",
					BatchID: "batch-lifecycle-" + suffix, FunctionName: "market-fetch", NodeID: "timer-node", Region: "region", DeadlineAt: period.Add(time.Hour),
				}
				return TimerPeriodBatchPlan{
					Manifest:  manifest,
					Batch:     &domain.BatchInvocation{SpaceID: "crypto", BatchID: manifest.BatchID, ScheduleID: manifest.BatchID, BatchKind: domain.BatchKindRealtime, Frequency: "1m", Status: domain.BatchStatusPlanned, RequestJSON: `{}`, PlannedCount: 1},
					Instances: []domain.TaskInstance{{SpaceID: "crypto", InstanceID: instanceID, RunID: manifest.FirstRunID, Provider: "binance", ProviderSymbol: "BTCUSDT", SourceID: "spot_http", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: &targetTime, TaskParams: `{"bar_limit":10}`}},
					Targets:   []domain.WriteTarget{{ID: "target-lifecycle", SpaceID: "crypto", InstanceID: instanceID, TaskID: task.TaskID, DatasetID: "bars", SeriesIndex: 0, SeriesHash: "one-series", ExpectedCount: 1, Status: "pending"}},
				}
			}

			first := makePlan(period, "first")
			created, err := s.TimerPeriodBatches().CreateMany(ctx, []TimerPeriodBatchPlan{first})
			require.NoError(t, err)
			require.Equal(t, 1, created)
			stored, err := s.TaskInstances().Get(ctx, "crypto", instanceID)
			require.NoError(t, err)
			require.Equal(t, domain.InstanceStatusPending, stored.LastExecStatus)
			require.False(t, stored.CreateTime.IsZero())
			require.False(t, stored.ModifyTime.IsZero())

			lastExecTime := time.Now().UTC().Add(-time.Minute)
			lastResult := `{"state":"preserved"}`
			require.NoError(t, s.db.Model(&domain.TaskInstance{}).
				Where("c_space_id = ? AND c_instance_id = ?", "crypto", instanceID).
				Updates(map[string]any{"c_last_exec_status": status, "c_last_exec_time": lastExecTime, "c_result": lastResult}).Error)
			second := makePlan(period.Add(time.Minute), "second")
			created, err = s.TimerPeriodBatches().CreateMany(ctx, []TimerPeriodBatchPlan{second})
			require.NoError(t, err)
			require.Equal(t, 1, created)
			stored, err = s.TaskInstances().Get(ctx, "crypto", instanceID)
			require.NoError(t, err)
			require.Equal(t, status, stored.LastExecStatus, "period insertion must not regress terminal TaskInstance state")
			require.Equal(t, lastExecTime, stored.LastExecTime.UTC())
			require.Equal(t, lastResult, stored.Result)
			require.False(t, stored.CreateTime.IsZero())
		})
	}
}

func TestTimerPeriodBatchSameRequestAcrossCandidatesBindsOnce(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	seedTimerPeriodBatch(t, s, "old", "run-old", period)
	seedTimerPeriodBatch(t, s, "new", "run-new", period.Add(time.Minute))

	const contenders = 10
	var wg sync.WaitGroup
	results := make(chan TimerPeriodBatchClaimResult, contenders)
	errs := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.TimerPeriodBatches().Claim(ctx, timerBatchClaimInput(period.Add(10*time.Second), "shared-request"))
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	claimedBatch := ""
	for result := range results {
		if !result.Claimed {
			continue
		}
		if claimedBatch == "" {
			claimedBatch = result.BatchID
		}
		require.Equal(t, claimedBatch, result.BatchID, "request replay must never bind a second period")
	}
	require.Equal(t, "batch-old", claimedBatch, "oldest eligible period is claimed first")
}

func TestCancelUnclaimedTimerBatchesAfterTaskDisableOrUpdate(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	seedTimerPeriodBatch(t, s, "disabled", "run-disabled", period)
	seedTimerPeriodBatch(t, s, "updated", "run-updated", period.Add(time.Minute))
	seedTimerPeriodBatch(t, s, "claimed", "run-claimed", period.Add(2*time.Minute))

	require.NoError(t, s.Tasks().SetEnabled(ctx, "crypto", "task-disabled", false))
	updatedAt := time.Now().UTC().Add(time.Minute)
	require.NoError(t, s.db.Model(&domain.CollectionTask{}).Where("c_space_id = ? AND c_task_id = ?", "crypto", "task-updated").Update("c_mtime", updatedAt).Error)
	require.NoError(t, s.db.Model(&domain.BatchInvocation{}).Where("c_space_id = ? AND c_batch_id = ?", "crypto", "batch-claimed").Update("c_status", domain.BatchStatusDispatched).Error)
	require.NoError(t, s.db.Model(&domain.TimerPeriodBatch{}).Where("c_space_id = ? AND c_batch_id = ?", "crypto", "batch-claimed").Update("c_claim_request_id", "claimed-request").Error)

	canceled, err := s.TimerPeriodBatches().CancelStaleUnclaimed(ctx, "crypto")
	require.NoError(t, err)
	require.EqualValues(t, 2, canceled)
	for _, batchID := range []string{"batch-disabled", "batch-updated"} {
		batch, getErr := s.FetchBatches().Get(ctx, "crypto", batchID)
		require.NoError(t, getErr)
		require.Equal(t, domain.BatchStatusFailed, batch.Status)
		require.Contains(t, batch.ErrorSummary, "task changed")
	}
	claimed, err := s.FetchBatches().Get(ctx, "crypto", "batch-claimed")
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusDispatched, claimed.Status)
}

func timerBatchClaimInput(tick time.Time, requestID string) TimerPeriodBatchClaimInput {
	return TimerPeriodBatchClaimInput{
		SpaceID: "crypto", FunctionName: "market-fetch", RequestID: requestID,
		GroupID: 0, GroupCount: 1, BindingHash: "binding-v1", TickTime: tick,
		NodeID: "timer-node", Region: "ap-singapore", CompletionTimeout: 70 * time.Second,
	}
}

func seedTimerPeriodBatch(t *testing.T, s *Store, suffix, runID string, period time.Time) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-" + suffix, TaskName: "task-" + suffix,
		DataType: "kline", CollectParams: `{"frequency":"1m"}`, Enabled: true,
	}))
	instanceID := "instance-" + suffix
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: instanceID, RunID: runID, DataType: "kline",
		SubjectID: "BTC-USDT", Frequency: "1m", FunctionName: "market-fetch", TaskParams: "{}",
	}}))
	require.NoError(t, s.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{{
		ID: "target-" + suffix, SpaceID: "crypto", InstanceID: instanceID,
		TaskID: "task-" + suffix, DatasetID: "bars", Status: "pending",
	}}))
	deadline := time.Now().UTC().Add(time.Hour)
	batchID := "batch-" + suffix
	manifest := &domain.TimerPeriodBatch{
		Key: "manifest-" + suffix, SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period,
		TaskID: "task-" + suffix, FirstRunID: runID, SeriesHash: "series-hash", ExpectedCount: 1,
		GroupID: 0, GroupCount: 1, ShardIndex: 0, BindingHash: "binding-v1", RouteVersion: "route-v1",
		BatchID: batchID, FunctionName: "market-fetch", NodeID: "timer-node", Region: "ap-singapore", DeadlineAt: deadline,
	}
	batch := &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: batchID, ScheduleID: "schedule-" + suffix,
		BatchKind: domain.BatchKindRealtime, ShardIndex: 0, Frequency: "1m", Region: "ap-singapore",
		NodeID: "timer-node", FunctionName: "market-fetch", Status: domain.BatchStatusPlanned,
		RequestJSON: `{"batch_id":"` + batchID + `","space_id":"crypto","items":[{}]}`, PlannedCount: 1,
	}
	created, err := s.TimerPeriodBatches().Create(ctx, TimerPeriodBatchPlan{Manifest: manifest, Batch: batch, Instances: []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: instanceID, RunID: runID, DataType: "kline", SubjectID: "BTC-USDT",
		Frequency: "1m", FunctionName: "market-fetch", TaskParams: "{}",
	}}, Targets: []domain.WriteTarget{{
		ID: "target-" + suffix, SpaceID: "crypto", InstanceID: instanceID, TaskID: "task-" + suffix,
		DatasetID: "bars", SeriesIndex: 0, SeriesHash: "series-hash", ExpectedCount: 1, Status: "pending",
	}}})
	require.NoError(t, err)
	require.True(t, created)
}
