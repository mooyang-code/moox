package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFetchBatchCompletionLateSuccessOnlyCancelsPendingRetry(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, item := range []*domain.RetryItem{
		{SpaceID: "crypto", RetryKey: "pending", Status: "pending", Attempt: 1, CreateTime: now},
		{SpaceID: "crypto", RetryKey: "dispatched", Status: "dispatched", Attempt: 1, CreateTime: now},
	} {
		require.NoError(t, s.FetchRetries().Upsert(ctx, item))
	}
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "late", Status: domain.BatchStatusPlanned, CompletedAt: &now}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	batch.Status = domain.BatchStatusTimedOut
	updated, err := s.FetchBatches().Complete(ctx, batch)
	require.NoError(t, err)
	require.True(t, updated)
	batch.LateCompletion = true
	updated, err = s.FetchBatches().CompleteWithEffects(ctx, batch, FetchCompletionEffects{CancelPendingRetryKeys: []string{"pending", "dispatched"}})
	require.NoError(t, err)
	require.True(t, updated)

	pending, err := s.FetchRetries().Get(ctx, "crypto", "pending")
	require.NoError(t, err)
	dispatched, err := s.FetchRetries().Get(ctx, "crypto", "dispatched")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", pending.Status)
	assert.Equal(t, "dispatched", dispatched.Status)
}

func TestDueMarketFetchRecordsAreIsolatedBySpace(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, spaceID := range []string{"crypto", "research"} {
		batch := &domain.BatchInvocation{SpaceID: spaceID, BatchID: "batch-" + spaceID, Status: domain.BatchStatusPlanned, DeadlineAt: &now}
		created, err := s.FetchBatches().CreatePlanned(ctx, batch)
		require.NoError(t, err)
		require.True(t, created)
		require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{SpaceID: spaceID, RetryKey: "retry-" + spaceID, Status: "pending", NextRetryAt: &now, CreateTime: now}))
	}
	batches, err := s.FetchBatches().ListDue(ctx, "crypto", now, 10)
	require.NoError(t, err)
	require.Len(t, batches, 1)
	assert.Equal(t, "crypto", batches[0].SpaceID)
	retries, err := s.FetchRetries().ListDue(ctx, "crypto", now, 10)
	require.NoError(t, err)
	require.Len(t, retries, 1)
	assert.Equal(t, "crypto", retries[0].SpaceID)
}

func TestListDueUsesCanonicalDeadlineForUnclaimedTimerManifest(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	batch := &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: "timer-period", BatchKind: domain.BatchKindRealtime,
		Status: domain.BatchStatusPlanned, DeadlineAt: timePtr(now.Add(3 * time.Hour)), RequestJSON: `{"items":[]}`,
	}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, s.db.Create(&domain.TimerPeriodBatch{
		Key: "timer-manifest", SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: now.Add(-time.Minute),
		TaskID: "task", FirstRunID: "run", SeriesHash: "hash", ExpectedCount: 1, GroupCount: 1,
		BindingHash: "binding", RouteVersion: "route-v1", BatchID: "timer-period", FunctionName: "timer-fn", NodeID: "node",
		Region: "region", DeadlineAt: now.Add(2 * time.Hour),
	}).Error)

	due, err := s.FetchBatches().ListDue(ctx, "crypto", now, 10)
	require.NoError(t, err)
	require.Empty(t, due, "unclaimed planned manifests must use the canonical period deadline, not the batch completion deadline")

	due, err = s.FetchBatches().ListDue(ctx, "crypto", now.Add(2*time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, due, 1, "canonical deadline recovery must include an unclaimed manifest even when its batch deadline differs")
}

func TestMarkDispatchedToNodeUpdatesFailoverRouting(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "failover", Status: domain.BatchStatusPlanned, NodeID: "node-a", Region: "ap-beijing", FunctionName: "fn-a"}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	updated, err := s.FetchBatches().MarkDispatchedToNode(ctx, "crypto", "failover", "request-b", now.Add(time.Minute), "ap-shanghai", "node-b", "fn-b")
	require.NoError(t, err)
	require.True(t, updated)
	stored, err := s.FetchBatches().Get(ctx, "crypto", "failover")
	require.NoError(t, err)
	assert.Equal(t, domain.BatchStatusDispatched, stored.Status)
	assert.Equal(t, "ap-shanghai", stored.Region)
	assert.Equal(t, "node-b", stored.NodeID)
	assert.Equal(t, "fn-b", stored.FunctionName)
	assert.Equal(t, "request-b", stored.RequestID)
}

func TestFetchBatchCompletionMarksDispatchedRetryPermanent(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	retry := &domain.RetryItem{SpaceID: "crypto", RetryKey: "delisted", Status: "dispatched", Attempt: 2, CreateTime: now}
	require.NoError(t, s.FetchRetries().Upsert(ctx, retry))
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "retry-child", Status: domain.BatchStatusPlanned, CompletedAt: &now}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	batch.Status = domain.BatchStatusFailed
	updated, err := s.FetchBatches().CompleteWithEffects(ctx, batch, FetchCompletionEffects{PermanentRetryKeys: []string{"delisted"}})
	require.NoError(t, err)
	require.True(t, updated)
	stored, err := s.FetchRetries().Get(ctx, "crypto", "delisted")
	require.NoError(t, err)
	assert.Equal(t, "permanent_failed", stored.Status)
}

func TestMarkRetryBatchDispatchedPreservesTerminalKeysChangedAfterPreflight(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "retry-atomic-mismatch", Status: domain.BatchStatusPlanned, PlannedAt: &now}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	for _, key := range []string{"retry-a", "retry-b"} {
		require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{SpaceID: "crypto", RetryKey: key, Status: "pending", Attempt: 1, CreateTime: now}))
	}
	eligible, err := s.FetchBatches().PrepareRetryBatchDispatch(ctx, "crypto", batch.BatchID, []string{"retry-a", "retry-b"})
	require.NoError(t, err)
	require.True(t, eligible)
	require.NoError(t, s.FetchRetries().MarkStatus(ctx, "crypto", "retry-b", "superseded"))

	updated, err := s.FetchBatches().MarkRetryBatchDispatched(ctx, "crypto", batch.BatchID, "invoke-request", now.Add(time.Minute), "region", "node", "function", []string{"retry-a", "retry-b"})
	require.NoError(t, err)
	require.True(t, updated, "the accepted remote invocation must be recorded even if a retry key terminalized after preflight")
	storedBatch, err := s.FetchBatches().Get(ctx, "crypto", batch.BatchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusDispatched, storedBatch.Status)
	for key, want := range map[string]string{"retry-a": "dispatched", "retry-b": "superseded"} {
		stored, getErr := s.FetchRetries().Get(ctx, "crypto", key)
		require.NoError(t, getErr)
		require.Equal(t, want, stored.Status, "only retries still pending after the Invoke response may transition to dispatched")
	}
}

func TestMarkRetryBatchDispatchedCASFalseLeavesRetryKeysUnchanged(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "retry-atomic-terminal", Status: domain.BatchStatusPlanned, PlannedAt: &now}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	for _, key := range []string{"retry-a", "retry-b"} {
		require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{SpaceID: "crypto", RetryKey: key, Status: "pending", Attempt: 1, CreateTime: now}))
	}
	completed := *batch
	completed.Status = domain.BatchStatusSucceeded
	completed.CompletedAt = &now
	updated, err := s.FetchBatches().CompleteWithEffects(ctx, &completed, FetchCompletionEffects{SucceededRetryKeys: []string{"retry-a", "retry-b"}})
	require.NoError(t, err)
	require.True(t, updated)

	updated, err = s.FetchBatches().MarkRetryBatchDispatched(ctx, "crypto", batch.BatchID, "late-invoke-request", now.Add(time.Minute), "region", "node", "function", []string{"retry-a", "retry-b"})
	require.NoError(t, err)
	require.False(t, updated, "terminal batch must lose planned->dispatched CAS")
	for _, key := range []string{"retry-a", "retry-b"} {
		stored, getErr := s.FetchRetries().Get(ctx, "crypto", key)
		require.NoError(t, getErr)
		require.Equal(t, "succeeded", stored.Status, "CAS miss must not regress completion state")
	}
}

func TestMarkRetryBatchDispatchedRacingCompletionKeepsTerminalRetryState(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "retry-atomic-race", Status: domain.BatchStatusPlanned, PlannedAt: &now}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	keys := []string{"retry-a", "retry-b", "retry-c"}
	for _, key := range keys {
		require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{SpaceID: "crypto", RetryKey: key, Status: "pending", Attempt: 1, CreateTime: now}))
	}
	start := make(chan struct{})
	type result struct {
		updated bool
		err     error
	}
	dispatchResult := make(chan result, 1)
	completionResult := make(chan result, 1)
	go func() {
		<-start
		updated, err := s.FetchBatches().MarkRetryBatchDispatched(ctx, "crypto", batch.BatchID, "invoke-request", now.Add(time.Minute), "region", "node", "function", keys)
		dispatchResult <- result{updated: updated, err: err}
	}()
	go func() {
		<-start
		completed := *batch
		completed.Status = domain.BatchStatusSucceeded
		completed.CompletedAt = &now
		updated, err := s.FetchBatches().CompleteWithEffects(ctx, &completed, FetchCompletionEffects{SucceededRetryKeys: keys})
		completionResult <- result{updated: updated, err: err}
	}()
	close(start)
	dispatch := <-dispatchResult
	completion := <-completionResult
	require.NoError(t, dispatch.err)
	require.NoError(t, completion.err)
	require.True(t, completion.updated)
	storedBatch, err := s.FetchBatches().Get(ctx, "crypto", batch.BatchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusSucceeded, storedBatch.Status)
	for _, key := range keys {
		stored, getErr := s.FetchRetries().Get(ctx, "crypto", key)
		require.NoError(t, getErr)
		require.Equal(t, "succeeded", stored.Status, "completion must win without later dispatch status regression")
	}
}

func TestPermanentRetryFailurePersistsUntilReportedAndMarksRuntimeFailed(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "task-a", TaskName: "Task A", DataType: "kline", Enabled: true}))
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{SpaceID: "crypto", InstanceID: "instance-a", SubjectID: "ETH-USDT", Frequency: "1m", TaskParams: `{}`}}))
	require.NoError(t, s.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{{ID: "target-a", SpaceID: "crypto", InstanceID: "instance-a", TaskID: "task-a", DatasetID: "bars", Status: "pending"}}))
	failureTargetsJSON, err := json.Marshal([]domain.WriteTarget{{ID: "target-a", SpaceID: "crypto", InstanceID: "instance-a", TaskID: "task-a", DatasetID: "bars", SeriesIndex: 0, SeriesHash: "hash", ExpectedCount: 1}})
	require.NoError(t, err)
	require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "retry-a", InstanceID: "instance-a", SubjectID: "ETH-USDT", Frequency: "1m",
		TargetDataTime: now, FailureTargetsJSON: string(failureTargetsJSON), Status: "pending", Attempt: 3,
	}))

	require.NoError(t, s.FetchRetries().MarkPermanent(ctx, "crypto", "retry-a", "retry_budget_exhausted", "upstream timed out"))
	retry, err := s.FetchRetries().Get(ctx, "crypto", "retry-a")
	require.NoError(t, err)
	require.Equal(t, "permanent_failed", retry.Status)
	require.Equal(t, "retry_budget_exhausted", retry.LastErrorType)
	require.Nil(t, retry.NextRetryAt)
	instance, err := s.TaskInstances().Get(ctx, "crypto", "instance-a")
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusFailed, instance.LastExecStatus)
	target, err := s.TaskInstances().GetWriteTarget(ctx, "crypto", "target-a")
	require.NoError(t, err)
	require.Equal(t, "failed", target.Status)

	// A stale cleanup pass must preserve the row until Storage acknowledges it.
	require.NoError(t, s.db.Model(&domain.RetryItem{}).Where("c_space_id = ? AND c_retry_key = ?", "crypto", "retry-a").Update("c_mtime", now.Add(-8*24*time.Hour)).Error)
	require.NoError(t, s.FetchRetries().Cleanup(ctx, now.Add(-7*24*time.Hour)))
	_, err = s.FetchRetries().Get(ctx, "crypto", "retry-a")
	require.NoError(t, err)
	require.NoError(t, s.FetchRetries().ApplyPeriodFailureReportResults(ctx, "crypto", "retry-a", []domain.PeriodFailureTargetResult{{
		WriteTargetID: "target-a", SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: now,
		SeriesHash: "hash", ExpectedCount: 1, SeriesIndex: 0, Disposition: "recorded", ObservedAt: now,
	}}, ""))
	require.NoError(t, s.FetchRetries().Cleanup(ctx, now.Add(time.Hour)))
	_, err = s.FetchRetries().Get(ctx, "crypto", "retry-a")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestFetchBatchCompletionSupersedesOlderPendingRetry(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, item := range []*domain.RetryItem{
		{SpaceID: "crypto", RetryKey: "older", InstanceID: "shared-btc", RetryScope: "fetch", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: now.Add(-time.Minute), Status: "pending", CreateTime: now},
		{SpaceID: "crypto", RetryKey: "newer", InstanceID: "shared-btc", RetryScope: "fetch", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: now.Add(time.Minute), Status: "pending", CreateTime: now},
	} {
		require.NoError(t, s.FetchRetries().Upsert(ctx, item))
	}
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "realtime", Status: domain.BatchStatusPlanned, CompletedAt: &now}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	batch.Status = domain.BatchStatusSucceeded
	updated, err := s.FetchBatches().CompleteWithEffects(ctx, batch, FetchCompletionEffects{SupersedePendingRetries: []MarketFetchRetrySupersede{{SpaceID: "crypto", InstanceID: "shared-btc", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: now}}})
	require.NoError(t, err)
	require.True(t, updated)
	older, err := s.FetchRetries().Get(ctx, "crypto", "older")
	require.NoError(t, err)
	newer, err := s.FetchRetries().Get(ctx, "crypto", "newer")
	require.NoError(t, err)
	assert.Equal(t, "superseded", older.Status)
	assert.Equal(t, "pending", newer.Status)
}

func TestFetchBatchCompletionSupersedesOnlyOlderWriteTargetRetries(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.September, 28, 6, 0, 0, 0, time.UTC)
	for _, item := range []*domain.RetryItem{
		{SpaceID: "crypto", RetryKey: "target-older", InstanceID: "instance-a", WriteTargetID: "target-a", RetryScope: "write_target", TargetDataTime: now.Add(-time.Minute), Status: "pending"},
		{SpaceID: "crypto", RetryKey: "target-same", InstanceID: "instance-a", WriteTargetID: "target-a", RetryScope: "write_target", TargetDataTime: now, Status: "dispatched"},
		{SpaceID: "crypto", RetryKey: "target-newer", InstanceID: "instance-a", WriteTargetID: "target-a", RetryScope: "write_target", TargetDataTime: now.Add(time.Minute), Status: "pending"},
		{SpaceID: "crypto", RetryKey: "other-instance", InstanceID: "instance-b", WriteTargetID: "target-a", RetryScope: "write_target", TargetDataTime: now.Add(-time.Minute), Status: "pending"},
	} {
		require.NoError(t, s.FetchRetries().Upsert(ctx, item))
	}
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "write-target-realtime", Status: domain.BatchStatusPlanned, CompletedAt: &now}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	batch.Status = domain.BatchStatusSucceeded
	updated, err := s.FetchBatches().CompleteWithEffects(ctx, batch, FetchCompletionEffects{SupersedePendingRetries: []MarketFetchRetrySupersede{{
		SpaceID: "crypto", InstanceID: "instance-a", WriteTargetID: "target-a", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: now,
	}}})
	require.NoError(t, err)
	require.True(t, updated)
	for key, want := range map[string]string{
		"target-older":   "superseded",
		"target-same":    "superseded",
		"target-newer":   "pending",
		"other-instance": "pending",
	} {
		item, getErr := s.FetchRetries().Get(ctx, "crypto", key)
		require.NoError(t, getErr)
		assert.Equal(t, want, item.Status, key)
	}
}

func seedCompletionAtomicityTest(t *testing.T, s *Store, secondInstance bool) (*domain.BatchInvocation, domain.TaskInstance, []domain.WriteTarget) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "task-a", TaskName: "Task A", DataType: "kline", Enabled: true}))
	if secondInstance {
		require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "task-b", TaskName: "Task B", DataType: "kline", Enabled: true}))
	}
	instances := []domain.TaskInstance{{SpaceID: "crypto", InstanceID: "instance-a", Provider: "binance", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`}}
	if secondInstance {
		instances = append(instances, domain.TaskInstance{SpaceID: "crypto", InstanceID: "instance-b", Provider: "binance", MarketType: "spot", DataType: "kline", SubjectID: "ETH-USDT", Frequency: "1m", TaskParams: `{}`})
	}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, instances))
	targets := []domain.WriteTarget{{ID: "target-a", SpaceID: "crypto", InstanceID: "instance-a", TaskID: "task-a", DatasetID: "bars-a", Status: "pending"}}
	if secondInstance {
		targets = append(targets, domain.WriteTarget{ID: "target-b", SpaceID: "crypto", InstanceID: "instance-b", TaskID: "task-b", DatasetID: "bars-b", Status: "pending"})
	}
	require.NoError(t, s.TaskInstances().UpsertWriteTargets(ctx, targets))
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "atomic-batch", ScheduleID: "schedule", BatchKind: domain.BatchKindRealtime, Frequency: "1m", Status: domain.BatchStatusPlanned, PlannedCount: len(instances), CompletedAt: &now}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	ids := make([]string, 0, len(instances))
	for _, instance := range instances {
		ids = append(ids, instance.InstanceID)
	}
	require.NoError(t, s.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, ids))
	batch.Status = domain.BatchStatusSucceeded
	return batch, instances[0], targets
}

func assertCompletionRolledBack(t *testing.T, s *Store, batchID, targetID string) {
	t.Helper()
	ctx := context.Background()
	batch, err := s.FetchBatches().Get(ctx, "crypto", batchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusPlanned, batch.Status)
	if targetID != "" {
		target, err := s.TaskInstances().GetWriteTarget(ctx, "crypto", targetID)
		require.NoError(t, err)
		require.Equal(t, "pending", target.Status)
	}
}

func TestCompleteWithEffectsRejectsInvalidAssociationsWithoutSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *Store, *domain.BatchInvocation, []domain.WriteTarget) FetchCompletionEffects
	}{
		{
			name: "instance not in batch item",
			setup: func(t *testing.T, s *Store, batch *domain.BatchInvocation, targets []domain.WriteTarget) FetchCompletionEffects {
				require.NoError(t, s.db.WithContext(context.Background()).Exec("DELETE FROM t_collector_fetch_batch_items WHERE c_space_id = ? AND c_batch_id = ?", "crypto", batch.BatchID).Error)
				return FetchCompletionEffects{InstanceUpdates: []MarketFetchInstanceUpdate{{SpaceID: "crypto", InstanceID: "instance-a", SubjectID: "BTC-USDT", Frequency: "1m", Status: domain.InstanceStatusSuccess, At: time.Now().UTC()}}}
			},
		},
		{
			name: "target dataset mismatch",
			setup: func(_ *testing.T, _ *Store, _ *domain.BatchInvocation, targets []domain.WriteTarget) FetchCompletionEffects {
				return FetchCompletionEffects{WriteTargetUpdates: []WriteTargetStatusEffect{{SpaceID: "crypto", InstanceID: "instance-a", WriteTargetID: targets[0].ID, DatasetID: "wrong-dataset", Status: "succeeded"}}}
			},
		},
		{
			name: "target belongs to another instance",
			setup: func(_ *testing.T, _ *Store, _ *domain.BatchInvocation, targets []domain.WriteTarget) FetchCompletionEffects {
				return FetchCompletionEffects{WriteTargetUpdates: []WriteTargetStatusEffect{{SpaceID: "crypto", InstanceID: "instance-b", WriteTargetID: targets[0].ID, DatasetID: targets[0].DatasetID, Status: "succeeded"}}}
			},
		},
		{
			name: "space mismatch",
			setup: func(_ *testing.T, _ *Store, _ *domain.BatchInvocation, targets []domain.WriteTarget) FetchCompletionEffects {
				return FetchCompletionEffects{WriteTargetUpdates: []WriteTargetStatusEffect{{SpaceID: "research", InstanceID: "instance-a", WriteTargetID: targets[0].ID, DatasetID: targets[0].DatasetID, Status: "succeeded"}}}
			},
		},
		{
			name: "detached target",
			setup: func(t *testing.T, s *Store, _ *domain.BatchInvocation, targets []domain.WriteTarget) FetchCompletionEffects {
				require.NoError(t, s.TaskInstances().DeleteWriteTargetsByTask(context.Background(), "crypto", targets[0].TaskID))
				return FetchCompletionEffects{WriteTargetUpdates: []WriteTargetStatusEffect{{SpaceID: "crypto", InstanceID: "instance-a", WriteTargetID: targets[0].ID, DatasetID: targets[0].DatasetID, Status: "succeeded"}}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCollectorStore(t)
			batch, _, targets := seedCompletionAtomicityTest(t, s, true)
			effects := tc.setup(t, s, batch, targets)
			updated, err := s.FetchBatches().CompleteWithEffects(context.Background(), batch, effects)
			require.Error(t, err)
			require.False(t, updated, "CAS must roll back with the association validation")
			targetID := targets[0].ID
			if tc.name == "detached target" {
				targetID = ""
			}
			assertCompletionRolledBack(t, s, batch.BatchID, targetID)
		})
	}
}

func TestCompleteWithEffectsRollsBackAllEffectsOnMidTransactionFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger string
		effects func(*domain.BatchInvocation, domain.TaskInstance, []domain.WriteTarget) FetchCompletionEffects
	}{
		{
			name:    "retry insert failure",
			trigger: `CREATE TRIGGER fail_completion_retry BEFORE INSERT ON t_collector_fetch_retry_items BEGIN SELECT RAISE(ABORT, 'retry insert failure'); END`,
			effects: func(batch *domain.BatchInvocation, instance domain.TaskInstance, targets []domain.WriteTarget) FetchCompletionEffects {
				now := time.Now().UTC()
				return FetchCompletionEffects{
					WriteTargetUpdates: []WriteTargetStatusEffect{{SpaceID: "crypto", InstanceID: instance.InstanceID, WriteTargetID: targets[0].ID, DatasetID: targets[0].DatasetID, Status: "succeeded"}},
					Retries:            []*domain.RetryItem{{SpaceID: "crypto", RetryKey: "retry-fail", SourceBatchID: batch.BatchID, InstanceID: instance.InstanceID, RetryScope: "fetch", SubjectID: instance.SubjectID, Frequency: instance.Frequency, Status: "pending", CreateTime: now}},
				}
			},
		},
		{
			name:    "instance update failure",
			trigger: `CREATE TRIGGER fail_completion_instance BEFORE UPDATE OF c_last_exec_status ON t_collector_task_instances BEGIN SELECT RAISE(ABORT, 'instance update failure'); END`,
			effects: func(_ *domain.BatchInvocation, instance domain.TaskInstance, targets []domain.WriteTarget) FetchCompletionEffects {
				return FetchCompletionEffects{
					WriteTargetUpdates: []WriteTargetStatusEffect{{SpaceID: "crypto", InstanceID: instance.InstanceID, WriteTargetID: targets[0].ID, DatasetID: targets[0].DatasetID, Status: "succeeded"}},
					InstanceUpdates:    []MarketFetchInstanceUpdate{{SpaceID: "crypto", InstanceID: instance.InstanceID, SubjectID: instance.SubjectID, Frequency: instance.Frequency, Status: domain.InstanceStatusSuccess, At: time.Now().UTC(), Result: `{}`}},
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCollectorStore(t)
			batch, instance, targets := seedCompletionAtomicityTest(t, s, false)
			require.NoError(t, s.db.Exec(tc.trigger).Error)
			updated, err := s.FetchBatches().CompleteWithEffects(context.Background(), batch, tc.effects(batch, instance, targets))
			require.Error(t, err)
			require.False(t, updated)
			assertCompletionRolledBack(t, s, batch.BatchID, targets[0].ID)
			stored, getErr := s.TaskInstances().Get(context.Background(), "crypto", instance.InstanceID)
			require.NoError(t, getErr)
			require.Equal(t, domain.InstanceStatusPending, stored.LastExecStatus)
			_, retryErr := s.FetchRetries().Get(context.Background(), "crypto", "retry-fail")
			require.Error(t, retryErr)
		})
	}
}

func TestCompleteWithEffectsDuplicateTerminalCompletionDoesNotApplyEffects(t *testing.T) {
	s := newCollectorStore(t)
	batch, _, targets := seedCompletionAtomicityTest(t, s, false)
	updated, err := s.FetchBatches().CompleteWithEffects(context.Background(), batch, FetchCompletionEffects{WriteTargetUpdates: []WriteTargetStatusEffect{{SpaceID: "crypto", InstanceID: "instance-a", WriteTargetID: targets[0].ID, DatasetID: targets[0].DatasetID, Status: "succeeded"}}})
	require.NoError(t, err)
	require.True(t, updated)

	batch.Status = domain.BatchStatusFailed
	updated, err = s.FetchBatches().CompleteWithEffects(context.Background(), batch, FetchCompletionEffects{WriteTargetUpdates: []WriteTargetStatusEffect{{SpaceID: "crypto", InstanceID: "instance-a", WriteTargetID: targets[0].ID, DatasetID: targets[0].DatasetID, Status: "failed", LastError: "duplicate must not apply"}}})
	require.NoError(t, err)
	require.False(t, updated)
	target, err := s.TaskInstances().GetWriteTarget(context.Background(), "crypto", targets[0].ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", target.Status)
}

func TestCompleteWithEffectsRejectsNonterminalStatus(t *testing.T) {
	s := newCollectorStore(t)
	batch, _, targets := seedCompletionAtomicityTest(t, s, false)
	batch.Status = domain.BatchStatusDispatched
	updated, err := s.FetchBatches().CompleteWithEffects(context.Background(), batch, FetchCompletionEffects{
		WriteTargetUpdates: []WriteTargetStatusEffect{{SpaceID: "crypto", InstanceID: "instance-a", WriteTargetID: targets[0].ID, DatasetID: targets[0].DatasetID, Status: "succeeded"}},
	})
	require.ErrorContains(t, err, "not terminal")
	require.False(t, updated)
	assertCompletionRolledBack(t, s, batch.BatchID, targets[0].ID)
}

func TestCompleteWithEffectsRechecksTerminalRetrySourceAtomically(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	batch, instance, targets := seedCompletionAtomicityTest(t, s, false)
	now := time.Now().UTC()
	const retryKey = "retry-a"
	require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: retryKey, InstanceID: instance.InstanceID, Status: "succeeded", Attempt: 2, CreateTime: now,
	}))
	require.NoError(t, s.TaskInstances().UpdateWriteTargetStatus(ctx, "crypto", targets[0].ID, "succeeded", "", 1))
	require.NoError(t, s.db.WithContext(ctx).Model(&domain.TaskInstance{}).
		Where("c_space_id = ? AND c_instance_id = ?", "crypto", instance.InstanceID).
		Update("c_last_exec_status", domain.InstanceStatusSuccess).Error)

	batch.Status = domain.BatchStatusFailed
	batch.CompletedAt = &now
	updated, err := s.FetchBatches().CompleteWithEffects(ctx, batch, FetchCompletionEffects{
		RetrySourceGuards: map[string]string{retryKey: retryKey},
		WriteTargetUpdates: []WriteTargetStatusEffect{{
			SpaceID: "crypto", RetrySourceKey: retryKey, InstanceID: instance.InstanceID,
			WriteTargetID: targets[0].ID, DatasetID: targets[0].DatasetID, Status: "failed", LastError: "stale failure",
		}},
		Retries:              []*domain.RetryItem{{SpaceID: "crypto", RetryKey: retryKey, InstanceID: instance.InstanceID, Status: "pending", Attempt: 3, CreateTime: now}},
		PermanentRetryKeys:   []string{retryKey},
		PermanentRetryErrors: map[string]RetryTerminalError{retryKey: {ErrorType: "invalid", ErrorSummary: "stale failure"}},
		InstanceUpdates:      []MarketFetchInstanceUpdate{{SpaceID: "crypto", RetrySourceKey: retryKey, InstanceID: instance.InstanceID, SubjectID: instance.SubjectID, Frequency: instance.Frequency, At: now, Status: domain.InstanceStatusFailed, Result: `{"error_type":"invalid"}`}},
	})
	require.NoError(t, err)
	require.True(t, updated, "the stale batch is terminalized even though its retry effects are suppressed")

	storedRetry, err := s.FetchRetries().Get(ctx, "crypto", retryKey)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", storedRetry.Status)
	storedTarget, err := s.TaskInstances().GetWriteTarget(ctx, "crypto", targets[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", storedTarget.Status)
	storedInstance, err := s.TaskInstances().Get(ctx, "crypto", instance.InstanceID)
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusSuccess, storedInstance.LastExecStatus)
}
