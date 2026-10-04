package store

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRunRepositoryReconcilesTimerOwnedRunFromInstances(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	run, err := s.Runs().GetOrCreateScheduled(ctx, "stockcn", "scheduled:timer", "scheduled", "1m", time.Now().UTC())
	require.NoError(t, err)
	instance := domain.TaskInstance{SpaceID: "stockcn", RunID: run.RunID, InstanceID: "cn-1", RequestKey: "request-cn-1", Provider: "stockcn_multi", MarketType: "equity", DataType: "kline", SubjectID: "600000.XSHG", Frequency: "1m"}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	attachTestWriteTarget(t, s, ctx, "stockcn", instance.InstanceID, "task-cn", "bars-cn")

	status, err := s.Runs().Reconcile(ctx, "stockcn", run.RunID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusActive, status)

	require.NoError(t, s.db.WithContext(ctx).Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "stockcn", instance.InstanceID).Update("c_last_exec_status", domain.InstanceStatusSuccess).Error)
	status, err = s.Runs().Reconcile(ctx, "stockcn", run.RunID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, status, "Timer-owned runs must not wait for target completion events")
}

func TestRunRepositoryReconcilesInvokeFanoutTargets(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	run, err := s.Runs().GetOrCreateScheduled(ctx, "crypto", "scheduled:invoke", "scheduled", "1m", time.Now().UTC())
	require.NoError(t, err)
	instance := domain.TaskInstance{SpaceID: "crypto", RunID: run.RunID, InstanceID: "btc", RequestKey: "request-btc", Provider: "binance", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", LastExecStatus: domain.InstanceStatusSuccess}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	attachTestWriteTarget(t, s, ctx, "crypto", instance.InstanceID, "task-a", "bars-a")
	attachTestWriteTarget(t, s, ctx, "crypto", instance.InstanceID, "task-b", "bars-b")
	now := time.Now().UTC()
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "batch-1", ScheduleID: "schedule-1", BatchKind: domain.BatchKindRealtime, ShardIndex: 0, Frequency: "1m", Status: domain.BatchStatusSucceeded, PlannedAt: &now, CompletedAt: &now}
	created, err := s.FetchBatches().CreatePlannedWithItemsForEnabledTargets(ctx, batch, []string{instance.InstanceID})
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, s.db.WithContext(ctx).Model(&domain.BatchInvocation{}).Where("c_space_id = ? AND c_batch_id = ?", "crypto", batch.BatchID).Updates(map[string]any{"c_status": domain.BatchStatusSucceeded, "c_completed_at": now}).Error)

	status, err := s.Runs().Reconcile(ctx, "crypto", run.RunID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusActive, status, "Invoke-owned run waits for independent target states")

	targets, err := s.TaskInstances().ListWriteTargets(ctx, "crypto", instance.InstanceID)
	require.NoError(t, err)
	require.Len(t, targets, 2)
	require.NoError(t, s.TaskInstances().UpdateWriteTargetStatus(ctx, "crypto", targets[0].ID, "succeeded", "", 0))
	require.NoError(t, s.TaskInstances().UpdateWriteTargetStatus(ctx, "crypto", targets[1].ID, "failed", "storage failed", 1))
	status, err = s.Runs().Reconcile(ctx, "crypto", run.RunID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusPartialFailed, status)

	require.NoError(t, s.TaskInstances().UpdateWriteTargetStatus(ctx, "crypto", targets[1].ID, "succeeded", "", 1))
	status, err = s.Runs().Reconcile(ctx, "crypto", run.RunID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusSucceeded, status)
}

func TestRunRepositoryReconcileOpenRunsExpiresStaleScheduledActiveRun(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	run, err := s.Runs().GetOrCreateScheduled(ctx, "crypto", "scheduled:stale-active", "scheduled", "1m", time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.NoError(t, s.Runs().UpdateStatus(ctx, "crypto", run.RunID, domain.RunStatusActive, ""))
	require.NoError(t, s.db.WithContext(ctx).Model(&domain.CollectionRun{}).
		Where("c_space_id = ? AND c_run_id = ?", "crypto", run.RunID).
		Updates(map[string]any{"c_ctime": time.Now().UTC().Add(-11 * time.Minute), "c_mtime": time.Now().UTC().Add(-11 * time.Minute)}).Error)

	require.NoError(t, s.Runs().ReconcileOpenRuns(ctx, "crypto"))
	stored, err := s.Runs().Get(ctx, "crypto", run.RunID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusFailed, stored.Status)
	require.Contains(t, stored.ErrorSummary, "stale scheduled run")
}

func TestRunRepositoryManualRunsAllowSameRequestKey(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	first, err := s.Runs().Create(ctx, "crypto", "manual_replay", "1m", nil)
	require.NoError(t, err)
	second, err := s.Runs().Create(ctx, "crypto", "manual_replay", "1m", nil)
	require.NoError(t, err)
	require.NotEqual(t, first.RunID, second.RunID)

	for _, run := range []*domain.CollectionRun{first, second} {
		require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
			SpaceID: "crypto", RunID: run.RunID, InstanceID: "instance-" + run.RunID,
			RequestKey: "same-provider-request", Provider: "binance", ProviderSymbol: "BTCUSDT",
			MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m",
		}}))
	}
	var count int64
	require.NoError(t, s.db.WithContext(ctx).Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_request_key = ?", "crypto", "same-provider-request").Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func TestRunRepositoryReconcileOpenRunsExpiresStaleEmptyPlannedRuns(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	stale, err := s.Runs().GetOrCreateScheduled(ctx, "crypto", "scheduled:stale-empty", "scheduled", "1m", now.Add(-3*time.Minute))
	require.NoError(t, err)
	recent, err := s.Runs().GetOrCreateScheduled(ctx, "crypto", "scheduled:recent-empty", "scheduled", "1m", now)
	require.NoError(t, err)
	require.NoError(t, s.db.WithContext(ctx).Model(&domain.CollectionRun{}).
		Where("c_space_id = ? AND c_run_id = ?", "crypto", stale.RunID).
		Updates(map[string]any{"c_ctime": now.Add(-3 * time.Minute), "c_mtime": now.Add(-3 * time.Minute)}).Error)

	require.NoError(t, s.Runs().ReconcileOpenRuns(ctx, "crypto"))

	staleAfter, err := s.Runs().Get(ctx, "crypto", stale.RunID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusFailed, staleAfter.Status)
	require.Equal(t, "stale planned run has no instances", staleAfter.ErrorSummary)
	recentAfter, err := s.Runs().Get(ctx, "crypto", recent.RunID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusPlanned, recentAfter.Status)
}
