package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestScheduledRunRetentionDeletesOnlyUnreferencedOldTerminalSummaries(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)
	old := now.Add(-31 * 24 * time.Hour)

	createRun := func(key, runType string, status domain.RunStatus, modified time.Time) *domain.CollectionRun {
		t.Helper()
		run, err := s.Runs().GetOrCreateScheduled(ctx, "crypto", key, runType, "1m", modified)
		require.NoError(t, err)
		require.NoError(t, s.db.Model(&domain.CollectionRun{}).Where("c_space_id = ? AND c_run_id = ?", run.SpaceID, run.RunID).
			Updates(map[string]any{"c_status": status, "c_mtime": modified}).Error)
		return run
	}

	oldUnreferenced := createRun("scheduled:old-unreferenced", "scheduled", domain.RunStatusSucceeded, old)
	oldReferenced := createRun("scheduled:old-referenced", "scheduled", domain.RunStatusFailed, old.Add(time.Minute))
	oldManual := createRun("manual:old", "manual_replay", domain.RunStatusSucceeded, old)
	oldActive := createRun("scheduled:active", "scheduled", domain.RunStatusActive, old.Add(2*time.Minute))
	recent := createRun("scheduled:recent", "scheduled", domain.RunStatusSucceeded, now)
	latest := createRun("scheduled:latest", "scheduled", domain.RunStatusSucceeded, old.Add(3*time.Minute))

	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "retained-instance", RunID: oldReferenced.RunID, Provider: "binance", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m",
	}}))
	deleted, err := s.Runs().CleanupScheduledTerminalSpace(ctx, "crypto", cutoff, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted, "only the old, unreferenced, non-latest terminal scheduled summary is collectible")

	for _, run := range []*domain.CollectionRun{oldUnreferenced, oldReferenced, oldManual, oldActive, latest, recent} {
		var count int64
		require.NoError(t, s.db.Model(&domain.CollectionRun{}).Where("c_space_id = ? AND c_run_id = ?", run.SpaceID, run.RunID).Count(&count).Error)
		if run == oldUnreferenced {
			require.Zero(t, count)
		} else {
			require.EqualValues(t, 1, count, "protected run %s must remain", run.RunID)
		}
	}
	var instanceCount int64
	require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_instance_id = ?", "retained-instance").Count(&instanceCount).Error)
	require.EqualValues(t, 1, instanceCount, "run cleanup must not delete stable TaskInstance rows")
}

func TestScheduledRunCleanupPageUsesBoundedIndex(t *testing.T) {
	s := newCollectorStore(t)
	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	require.NoError(t, s.db.Raw("EXPLAIN QUERY PLAN "+scheduledRunCleanupPageSQL, time.Now().UTC(), 100).Scan(&plan).Error)
	var details []string
	for _, row := range plan {
		details = append(details, row.Detail)
	}
	planText := fmt.Sprint(details)
	require.Contains(t, planText, "idx_collector_runs_terminal_cleanup")
	require.NotContains(t, planText, "TEMP B-TREE")
	plan = nil
	require.NoError(t, s.db.Raw("EXPLAIN QUERY PLAN "+scheduledRunCleanupPageSpaceSQL, "crypto", time.Now().UTC(), 100).Scan(&plan).Error)
	details = details[:0]
	for _, row := range plan {
		details = append(details, row.Detail)
	}
	planText = fmt.Sprint(details)
	require.Contains(t, planText, "idx_collector_runs_terminal_cleanup_space")
	require.NotContains(t, planText, "TEMP B-TREE")
}

func TestScheduledExecutionRetentionPrunesSupersededDetailsOnly(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-31 * 24 * time.Hour)
	cutoff := now.Add(-24 * time.Hour)

	createRun := func(key, runType string, status domain.RunStatus, modified time.Time) *domain.CollectionRun {
		t.Helper()
		run, err := s.Runs().GetOrCreateScheduled(ctx, "crypto", key, runType, "1m", modified)
		require.NoError(t, err)
		require.NoError(t, s.db.Model(&domain.CollectionRun{}).Where("c_space_id = ? AND c_run_id = ?", run.SpaceID, run.RunID).
			Updates(map[string]any{"c_status": status, "c_mtime": modified}).Error)
		return run
	}
	createExecution := func(run *domain.CollectionRun, instanceID, subject, taskID, targetID string, status int, targetStatus string, modified time.Time) {
		t.Helper()
		require.NoError(t, s.db.Exec(`INSERT OR IGNORE INTO t_collector_tasks(c_space_id,c_task_id,c_task_name,c_data_type,c_enabled,c_collect_params,c_prepare_state) VALUES(?,?,?,?,1,?,'ready')`,
			"crypto", taskID, taskID, "kline", `{}`).Error)
		require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
			SpaceID: "crypto", InstanceID: instanceID, RunID: run.RunID, RequestKey: instanceID,
			Provider: "binance", DataType: "kline", SubjectID: subject, Frequency: "1m", LastExecStatus: status,
		}}))
		require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", instanceID).
			Updates(map[string]any{"c_ctime": modified, "c_mtime": modified}).Error)
		require.NoError(t, s.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{{
			ID: targetID, SpaceID: "crypto", InstanceID: instanceID, TaskID: taskID, DatasetID: "bars", Status: targetStatus,
		}}))
		require.NoError(t, s.db.Model(&domain.WriteTarget{}).Where("c_space_id = ? AND c_write_target_id = ?", "crypto", targetID).
			Update("c_mtime", modified).Error)
	}

	oldSupersededRun := createRun("scheduled:old-superseded", "scheduled", domain.RunStatusSucceeded, old)
	oldSuperseded := createRun("scheduled:old-current", "scheduled", domain.RunStatusSucceeded, old.Add(time.Minute))
	currentRun := createRun("scheduled:current", "scheduled", domain.RunStatusSucceeded, now)
	failedCurrentRun := createRun("scheduled:current-failed", "scheduled", domain.RunStatusPartialFailed, now.Add(time.Minute))
	createExecution(oldSupersededRun, "old-btc", "BTC-USDT", "task-btc", "target-old-btc", domain.InstanceStatusSuccess, "succeeded", old)
	createExecution(currentRun, "current-btc", "BTC-USDT", "task-btc", "target-current-btc", domain.InstanceStatusSuccess, "succeeded", now)
	require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", "old-btc").Update("c_target_data_time", old).Error)
	require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", "old-btc").Update("c_mtime", old).Error)
	require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", "current-btc").Update("c_target_data_time", now).Error)
	completePeriod := testPeriodStorageState(old, domain.PeriodStatusComplete)
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, completePeriod))
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, testPeriodStorageState(now, domain.PeriodStatusComplete)))
	createExecution(oldSupersededRun, "old-disabled", "DISABLED-USDT", "task-disabled", "target-old-disabled", domain.InstanceStatusSuccess, "succeeded", old)
	createExecution(currentRun, "current-disabled", "DISABLED-USDT", "task-disabled", "target-current-disabled", domain.InstanceStatusSuccess, "succeeded", now)
	require.NoError(t, s.db.Model(&domain.CollectionTask{}).Where("c_space_id = ? AND c_task_id = ?", "crypto", "task-disabled").Update("c_enabled", false).Error)
	var disabledTaskEnabled bool
	require.NoError(t, s.db.Raw("SELECT c_enabled FROM t_collector_tasks WHERE c_space_id = ? AND c_task_id = ?", "crypto", "task-disabled").Scan(&disabledTaskEnabled).Error)
	require.False(t, disabledTaskEnabled)
	createExecution(oldSupersededRun, "old-failed-replacement", "FAILED-REPLACEMENT-USDT", "task-failed-replacement", "target-old-failed-replacement", domain.InstanceStatusSuccess, "succeeded", old)
	createExecution(failedCurrentRun, "current-failed-replacement", "FAILED-REPLACEMENT-USDT", "task-failed-replacement", "target-current-failed-replacement", domain.InstanceStatusFailed, "failed", now)
	createExecution(oldSupersededRun, "old-route-a", "MULTI-ROUTE-USDT", "task-multi-route", "target-old-route-a", domain.InstanceStatusSuccess, "succeeded", old)
	createExecution(failedCurrentRun, "failed-route-a", "MULTI-ROUTE-USDT", "task-multi-route", "target-failed-route-a", domain.InstanceStatusFailed, "failed", now)
	createExecution(currentRun, "current-route-b", "MULTI-ROUTE-USDT", "task-multi-route", "target-current-route-b", domain.InstanceStatusSuccess, "succeeded", now)
	for instanceID, route := range map[string]struct{ provider, seriesTag string }{
		"old-route-a":     {provider: "binance", seriesTag: "venue:binance|market:spot|source:spot_http"},
		"failed-route-a":  {provider: "binance", seriesTag: "venue:binance|market:spot|source:spot_http"},
		"current-route-b": {provider: "okx", seriesTag: "venue:okx|market:spot|source:spot_http"},
	} {
		require.NoError(t, s.db.Model(&domain.TaskInstance{}).
			Where("c_space_id = ? AND c_instance_id = ?", "crypto", instanceID).
			Updates(map[string]any{"c_provider": route.provider, "c_market_type": "spot", "c_source_id": "spot_http", "c_series_tag": route.seriesTag}).Error)
	}
	for _, instanceID := range []string{"old-disabled", "old-failed-replacement"} {
		require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", instanceID).
			Update("c_target_data_time", old).Error)
		require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", instanceID).
			Update("c_mtime", old).Error)
	}
	for _, instanceID := range []string{"current-disabled", "current-failed-replacement", "failed-route-a", "current-route-b"} {
		require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", instanceID).
			Update("c_target_data_time", now).Error)
	}
	require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", "old-route-a").
		Update("c_target_data_time", old).Error)
	require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", "old-route-a").
		Update("c_mtime", old).Error)
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, testPeriodStorageState(old, domain.PeriodStatusComplete)))
	missingStatePeriod := old.Add(2 * time.Hour)
	createExecution(oldSupersededRun, "old-missing-state", "MISSING-USDT", "task-missing", "target-old-missing-state", domain.InstanceStatusSuccess, "succeeded", old)
	createExecution(currentRun, "current-missing-state", "MISSING-USDT", "task-missing", "target-current-missing-state", domain.InstanceStatusSuccess, "succeeded", now)
	require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", "old-missing-state").Update("c_target_data_time", missingStatePeriod).Error)
	require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", "old-missing-state").Update("c_mtime", old).Error)
	createExecution(oldSuperseded, "old-retry", "RETRY-USDT", "task-retry", "target-retry", domain.InstanceStatusFailed, "failed", old)
	createExecution(createRun("scheduled:old-batch", "scheduled", domain.RunStatusSucceeded, old.Add(2*time.Minute)), "old-batch", "BATCH-USDT", "task-batch", "target-batch", domain.InstanceStatusFailed, "failed", old)
	createExecution(createRun("scheduled:old-latest", "scheduled", domain.RunStatusSucceeded, old.Add(3*time.Minute)), "old-latest", "LATEST-USDT", "task-latest", "target-latest", domain.InstanceStatusSuccess, "succeeded", old)
	oldWaitingRun := createRun("scheduled:old-waiting", "scheduled", domain.RunStatusSucceeded, old.Add(5*time.Minute))
	createExecution(oldWaitingRun, "old-waiting", "WAITING-USDT", "task-waiting", "target-old-waiting", domain.InstanceStatusSuccess, "succeeded", old)
	createExecution(currentRun, "current-waiting", "WAITING-USDT", "task-waiting", "target-current-waiting", domain.InstanceStatusSuccess, "succeeded", now)
	require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", "old-waiting").Update("c_target_data_time", old).Error)
	waitingPeriod := testPeriodStorageState(old, domain.PeriodStatusWaiting)
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, waitingPeriod))
	manualRun := createRun("manual:old", "manual_replay", domain.RunStatusSucceeded, old.Add(4*time.Minute))
	createExecution(manualRun, "manual-old", "MANUAL-USDT", "task-manual", "target-manual", domain.InstanceStatusSuccess, "succeeded", old)
	require.NoError(t, s.db.Exec(`INSERT INTO t_collector_tasks(c_space_id,c_task_id,c_task_name,c_data_type,c_enabled,c_collect_params,c_prepare_state,c_result_dataset_id) VALUES(?,?,?,?,1,?,'ready',?)`,
		"crypto", "task-resample", "task-resample", "kline_resample", `{}`, "resampled-bars").Error)
	for _, fixture := range []struct {
		run        *domain.CollectionRun
		instanceID string
		targetID   string
		modified   time.Time
	}{{oldSupersededRun, "old-resample", "target-old-resample", old}, {currentRun, "current-resample", "target-current-resample", now}} {
		require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
			SpaceID: "crypto", InstanceID: fixture.instanceID, RunID: fixture.run.RunID, RequestKey: fixture.instanceID,
			Provider: "moox", DataType: "kline_resample", SubjectID: "RESAMPLE-USDT", Frequency: "5m", LastExecStatus: domain.InstanceStatusSuccess,
		}}))
		require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", fixture.instanceID).
			Updates(map[string]any{"c_ctime": fixture.modified, "c_mtime": fixture.modified}).Error)
		require.NoError(t, s.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{{
			ID: fixture.targetID, SpaceID: "crypto", InstanceID: fixture.instanceID, TaskID: "task-resample", DatasetID: "resampled-bars", Status: "succeeded",
		}}))
		require.NoError(t, s.db.Model(&domain.WriteTarget{}).Where("c_space_id = ? AND c_write_target_id = ?", "crypto", fixture.targetID).
			Update("c_mtime", fixture.modified).Error)
	}

	require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "retry-pending", InstanceID: "old-retry", WriteTargetID: "target-retry",
		SubjectID: "RETRY-USDT", Frequency: "1m", TargetDataTime: old, TaskJSON: `{}`, Status: "pending",
	}))
	plannedAt := old
	created, err := s.FetchBatches().CreatePlanned(ctx, &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: "batch-active", ScheduleID: "schedule-active", BatchKind: domain.BatchKindRealtime,
		InstanceID: "old-batch", WriteTargetID: "target-batch", Frequency: "1m", Region: "region", NodeID: "node",
		FunctionName: "function", Status: domain.BatchStatusPlanned, Attempt: 1, PlannedAt: &plannedAt,
	})
	require.NoError(t, err)
	require.True(t, created)
	// One-row windows force the keyset cursor past every protected row.
	targetsDeleted, instancesDeleted := sweepExecutionCleanup(t, s, cutoff, 1)
	require.EqualValues(t, 2, targetsDeleted, "superseded targets should be removed for enabled and disabled tasks")
	require.EqualValues(t, 2, instancesDeleted, "superseded instances become removable after their targets are deleted")

	for _, id := range []string{"old-btc", "current-btc", "old-disabled", "current-disabled", "old-failed-replacement", "current-failed-replacement", "old-route-a", "failed-route-a", "current-route-b", "old-missing-state", "current-missing-state", "old-retry", "old-batch", "old-latest", "old-waiting", "current-waiting", "manual-old", "old-resample", "current-resample"} {
		var count int64
		require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", id).Count(&count).Error)
		if id == "old-btc" || id == "old-disabled" {
			require.Zero(t, count)
		} else {
			require.EqualValues(t, 1, count, "execution %s must be protected", id)
		}
	}
	_, err = s.TaskInstances().GetWriteTarget(ctx, "crypto", "target-old-btc")
	require.Error(t, err)
	_, err = s.TaskInstances().GetWriteTarget(ctx, "crypto", "target-current-btc")
	require.NoError(t, err)
	_, err = s.TaskInstances().GetWriteTarget(ctx, "crypto", "target-old-route-a")
	require.NoError(t, err, "a successful execution on another route must not supersede this route")
	_, err = s.TaskInstances().GetWriteTarget(ctx, "crypto", "target-old-missing-state")
	require.NoError(t, err, "a missing authoritative Storage state must keep the target")
	_, err = s.TaskInstances().GetWriteTarget(ctx, "crypto", "target-old-waiting")
	require.NoError(t, err, "Storage waiting periods must keep their target identity")
	_, err = s.TaskInstances().GetWriteTarget(ctx, "crypto", "target-old-resample")
	require.NoError(t, err, "kline_resample execution history is outside scheduled collector retention")

	runsDeleted, err := s.Runs().CleanupScheduledTerminalSpace(ctx, "crypto", now.Add(-30*24*time.Hour), 100)
	require.NoError(t, err)
	require.Zero(t, runsDeleted, "the old summary remains referenced by the protected missing-state execution")
}

// sweepExecutionCleanup runs complete keyset sweeps: write targets first, then
// the instances they released.
func sweepExecutionCleanup(t *testing.T, s *Store, cutoff time.Time, window int) (targets, instances int64) {
	t.Helper()
	ctx := context.Background()
	for _, step := range []struct {
		deleted *int64
		run     func(RetentionCursor) (int64, RetentionCursor, error)
	}{
		{&targets, func(after RetentionCursor) (int64, RetentionCursor, error) {
			return s.TaskInstances().CleanupScheduledWriteTargetsWindow(ctx, "crypto", cutoff, after, window)
		}},
		{&instances, func(after RetentionCursor) (int64, RetentionCursor, error) {
			return s.TaskInstances().CleanupScheduledInstancesWindow(ctx, "crypto", cutoff, after, window)
		}},
	} {
		cursor := RetentionCursor{}
		for windows := 0; ; windows++ {
			require.Less(t, windows, 1000, "keyset sweep must terminate")
			n, next, err := step.run(cursor)
			require.NoError(t, err)
			*step.deleted += n
			if next.IsZero() {
				break
			}
			require.NotEqual(t, cursor, next, "every window must advance the cursor")
			cursor = next
		}
	}
	return targets, instances
}

func TestScheduledExecutionCleanupWindowsUseBoundedIndexes(t *testing.T) {
	s := newCollectorStore(t)
	cutoff := time.Now().UTC()
	explain := func(query string, args ...any) string {
		var plan []struct {
			Detail string `gorm:"column:detail"`
		}
		require.NoError(t, s.db.Raw("EXPLAIN QUERY PLAN "+query, args...).Scan(&plan).Error)
		return fmt.Sprint(plan)
	}
	for _, windowEnd := range []struct{ query, index string }{
		{scheduledWriteTargetWindowEndSQL, "idx_collector_write_targets_retention_space"},
		{scheduledInstanceWindowEndSQL, "idx_collector_instances_terminal_cleanup_space"},
	} {
		planText := explain(windowEnd.query, "crypto", cutoff, "", 0, 99)
		require.Contains(t, planText, windowEnd.index)
		require.NotContains(t, planText, "TEMP B-TREE")
	}

	upper := "AND (targets.c_mtime, targets.c_id) <= (?, ?)"
	planText := explain(fmt.Sprintf(scheduledWriteTargetCleanupWindowSQL, upper), "crypto", cutoff, "", 0, "z", 1, cutoff, cutoff)
	require.Contains(t, planText, "idx_collector_write_targets_retention_space")
	// A space-only batch scan per candidate made production cleanup take
	// ~70ms per row; references must be resolved through their own indexes.
	require.Contains(t, planText, "idx_collector_fetch_batch_target_ref (c_space_id=? AND c_write_target_id=?)")
	require.Contains(t, planText, "idx_collector_fetch_batch_instance_ref (c_space_id=? AND c_instance_id=?)")
	require.Contains(t, planText, "idx_collector_instances_storage_write (c_space_id=? AND c_subject_id=? AND c_frequency=? AND c_series_tag=?)")
	require.NotContains(t, planText, "TEMP B-TREE")

	upper = "AND (instances.c_mtime, instances.c_id) <= (?, ?)"
	planText = explain(fmt.Sprintf(scheduledInstanceCleanupWindowSQL, upper), "crypto", "", 0, "z", 1, cutoff, cutoff)
	require.Contains(t, planText, "idx_collector_instances_terminal_cleanup_space")
	require.NotContains(t, planText, "TEMP B-TREE")
}
