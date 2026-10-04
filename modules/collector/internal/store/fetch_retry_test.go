package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestFetchRetryCleanupCountsCommittedRows(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, key := range []string{"a", "b"} {
		require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{SpaceID: "crypto", RetryKey: key, Status: "succeeded", TargetDataTime: old, CreateTime: old}))
	}
	require.NoError(t, s.db.Exec(`UPDATE t_collector_fetch_retry_items SET c_mtime = ?`, old).Error)
	require.NoError(t, s.db.Exec(`CREATE TRIGGER fail_retry_delete BEFORE DELETE ON t_collector_fetch_retry_items WHEN OLD.c_retry_key = 'b' BEGIN SELECT RAISE(ABORT, 'test rollback'); END`).Error)
	deleted, err := s.FetchRetries().CleanupSpaceWithCount(ctx, "crypto", time.Now(), 10)
	require.Error(t, err)
	require.Zero(t, deleted)
	require.NoError(t, s.db.Exec(`DROP TRIGGER fail_retry_delete`).Error)
	deleted, err = s.FetchRetries().CleanupSpaceWithCount(ctx, "crypto", time.Now(), 10)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted)
	deleted, err = s.FetchRetries().CleanupSpaceWithCount(ctx, "crypto", time.Now(), 10)
	require.NoError(t, err)
	require.Zero(t, deleted)
}

func TestListDuePrioritizesRecentRetriesAndKeepsHistoricalProgress(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for index := 0; index < 4; index++ {
		nextRetry := now.Add(-time.Minute)
		periodDeadline := now.Add(-time.Hour)
		require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
			SpaceID: "crypto", RetryKey: fmt.Sprintf("old-%d", index), Status: "pending",
			TargetDataTime: now.Add(-time.Hour), PeriodTime: timePtr(now.Add(-2 * time.Hour)), PeriodDeadlineAt: &periodDeadline,
			NextRetryAt: &nextRetry, CreateTime: now,
		}))
	}
	for index := 0; index < 4; index++ {
		nextRetry := now.Add(-time.Second)
		periodDeadline := now.Add(time.Duration(index+1) * time.Minute)
		require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
			SpaceID: "crypto", RetryKey: fmt.Sprintf("recent-%d", index), Status: "pending",
			TargetDataTime: now.Add(-time.Duration(index+1) * time.Minute), PeriodTime: timePtr(now.Add(-time.Minute)), PeriodDeadlineAt: &periodDeadline,
			NextRetryAt: &nextRetry, CreateTime: now.Add(-time.Hour),
		}))
	}

	recent, historical, err := s.FetchRetries().ListDuePrioritized(ctx, "crypto", now, 3, 1)
	require.NoError(t, err)
	require.Len(t, recent, 3)
	require.Len(t, historical, 1)
	require.Equal(t, "recent-0", recent[0].RetryKey, "the newest period must not sit behind historical backlog")
	require.Equal(t, "recent-1", recent[1].RetryKey)
	require.Equal(t, "recent-2", recent[2].RetryKey)
	require.Equal(t, "old-0", historical[0].RetryKey, "reserve part of every page for historical progress")
}

func TestListDuePrioritizedExcludesInFlightRetryKeys(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, key := range []string{"retry-a", "retry-b"} {
		nextRetry := now.Add(-time.Second)
		periodDeadline := now.Add(time.Minute)
		require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
			SpaceID: "crypto", RetryKey: key, Status: "pending", NextRetryAt: &nextRetry,
			TargetDataTime: now, PeriodDeadlineAt: &periodDeadline, CreateTime: now,
		}))
	}

	recent, _, err := s.FetchRetries().ListDuePrioritized(ctx, "crypto", now, 1, 0, "retry-a")
	require.NoError(t, err)
	require.Len(t, recent, 1)
	require.Equal(t, "retry-b", recent[0].RetryKey, "reserved work must not consume the next bounded query window")
}

func TestFetchRetryCleanupDeletesOnlyABoundedTerminalPage(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	count := maxTerminalRetryCleanupRows + 1
	require.NoError(t, s.db.Exec(`WITH RECURSIVE seq(n) AS (
		SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?
	) INSERT INTO t_collector_fetch_retry_items(
		c_space_id,c_retry_key,c_source_batch_id,c_batch_kind,c_subject_id,c_frequency,c_target_data_time,
		c_task_json,c_failure_targets_json,c_attempt,c_status,c_period_failure_report_state,c_period_failure_results_json,c_ctime,c_mtime
	) SELECT 'crypto','cleanup-retry-' || n,'source','realtime','BTC-USDT','1m',?,
		'{}','[]',1,'succeeded','acknowledged','[]',?,? FROM seq`, count, old, old, old).Error)

	cutoff := now.Add(-7 * 24 * time.Hour)
	require.NoError(t, s.FetchRetries().Cleanup(ctx, cutoff))
	var remaining int64
	require.NoError(t, s.db.Model(&domain.RetryItem{}).Where("c_space_id = ?", "crypto").Count(&remaining).Error)
	require.EqualValues(t, 1, remaining, "retry cleanup must bound writes per scheduler pass")
	require.NoError(t, s.FetchRetries().Cleanup(ctx, cutoff))
	require.NoError(t, s.db.Model(&domain.RetryItem{}).Where("c_space_id = ?", "crypto").Count(&remaining).Error)
	require.Zero(t, remaining)
}

func TestFetchRetryCleanupRetainsTerminalGuardReferencedByActiveBatch(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	retry := &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "superseded-active", InstanceID: "shared-instance",
		Status: "pending", TargetDataTime: old, CreateTime: old, ModifyTime: old,
	}
	require.NoError(t, s.FetchRetries().Upsert(ctx, retry))
	batch := &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: "active-retry-batch", ScheduleID: "retry:active-retry-batch",
		BatchKind: domain.BatchKindRealtime, Frequency: "1m", Status: domain.BatchStatusPlanned,
		RequestJSON: `{"items":[{"source_event_id":"superseded-active"}]}`,
	}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, s.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, []string{"shared-instance"}))
	dispatched, err := s.FetchBatches().MarkRetryBatchDispatched(ctx, "crypto", batch.BatchID, "request-1", time.Now().UTC().Add(time.Hour), "region", "node", "function", []string{retry.RetryKey})
	require.NoError(t, err)
	require.True(t, dispatched)
	require.NoError(t, s.FetchRetries().MarkStatus(ctx, "crypto", retry.RetryKey, "superseded"))
	require.NoError(t, s.db.Model(&domain.RetryItem{}).
		Where("c_space_id = ? AND c_retry_key = ?", retry.SpaceID, retry.RetryKey).Update("c_mtime", old).Error)

	deleted, err := s.FetchRetries().CleanupSpaceWithCount(ctx, "crypto", time.Now().UTC(), 10)
	require.NoError(t, err)
	require.Zero(t, deleted, "an active retry batch still needs the superseded retry guard during recovery")
	_, err = s.FetchRetries().Get(ctx, retry.SpaceID, retry.RetryKey)
	require.NoError(t, err)
}

func TestFetchRetryCleanupRetainsRetryReferencedByBatchRequest(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	retry := &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "request-key-only", Status: "succeeded",
		TargetDataTime: old, CreateTime: old, ModifyTime: old,
	}
	require.NoError(t, s.FetchRetries().Upsert(ctx, retry))
	batch := &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: "request-key-batch", ScheduleID: "retry:request-key-batch",
		BatchKind: domain.BatchKindRealtime, Frequency: "1m", Status: domain.BatchStatusPlanned,
		RequestJSON: `{"items":[{"source_event_id":"request-key-only","instance_id":"fallback-instance"}]}`,
	}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, s.db.Model(&domain.RetryItem{}).
		Where("c_space_id = ? AND c_retry_key = ?", retry.SpaceID, retry.RetryKey).
		Update("c_mtime", old).Error)

	deleted, err := s.FetchRetries().CleanupSpaceWithCount(ctx, "crypto", time.Now().UTC(), 10)
	require.NoError(t, err)
	require.Zero(t, deleted, "the batch's exact source_event_id must protect its Retry fence even when projections are empty")
	_, err = s.FetchRetries().Get(ctx, retry.SpaceID, retry.RetryKey)
	require.NoError(t, err)
}

func TestFetchRetryCleanupSkipsMalformedRequestItemsWithoutAbortingPage(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	retry := &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "cleanup-after-poison-request", Status: "succeeded",
		TargetDataTime: old, CreateTime: old, ModifyTime: old,
	}
	require.NoError(t, s.FetchRetries().Upsert(ctx, retry))
	batch := &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: "scalar-request-items", ScheduleID: "scalar-request-items",
		BatchKind: domain.BatchKindRealtime, Frequency: "1m", Status: domain.BatchStatusPlanned,
		RequestJSON: `{"items":["broken"]}`,
	}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, s.db.Model(&domain.RetryItem{}).
		Where("c_space_id = ? AND c_retry_key = ?", retry.SpaceID, retry.RetryKey).
		Update("c_mtime", old).Error)

	deleted, err := s.FetchRetries().CleanupSpaceWithCount(ctx, "crypto", time.Now().UTC(), 10)
	require.NoError(t, err, "a scalar request item must not cause SQLite json_extract to abort cleanup")
	require.EqualValues(t, 1, deleted)
}

func TestFetchRetryCleanupRetainsTimedOutLateCompletionFence(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	retry := &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "timed-out-fence", SourceBatchID: "timed-out-sync-point",
		Status: "succeeded", TargetDataTime: old, CreateTime: old, ModifyTime: old,
	}
	require.NoError(t, s.FetchRetries().Upsert(ctx, retry))
	batch := &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: "timed-out-batch", ScheduleID: "timed-out-schedule",
		BatchKind: domain.BatchKindRealtime, Frequency: "1m", Status: domain.BatchStatusPlanned,
		RequestJSON: `{"sync_point_id":"timed-out-sync-point","items":[{"source_event_id":"another-event","instance_id":"late-instance"}]}`,
		DeadlineAt:  &old,
	}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, s.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, []string{"late-instance"}))
	batch.Status = domain.BatchStatusTimedOut
	batch.CompletedAt = &old
	updated, err := s.FetchBatches().Complete(ctx, batch)
	require.NoError(t, err)
	require.True(t, updated)
	manifest := &domain.TimerPeriodBatch{
		Key: "timed-out-manifest", SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: old,
		TaskID: "timer-task", FirstRunID: "timer-run", SeriesHash: "one", ExpectedCount: 1, GroupCount: 1,
		BindingHash: "binding", RouteVersion: "route", BatchID: batch.BatchID, FunctionName: "market-fetch",
		NodeID: "timer-node", Region: "ap-hongkong", DeadlineAt: old.Add(time.Hour), CreateTime: old,
	}
	require.NoError(t, s.db.Create(manifest).Error)
	require.NoError(t, s.db.Model(&domain.RetryItem{}).
		Where("c_space_id = ? AND c_retry_key = ?", retry.SpaceID, retry.RetryKey).
		Update("c_mtime", old).Error)
	require.NoError(t, s.FetchBatches().Cleanup(ctx, now.Add(-24*time.Hour)))
	var retainedBatchCount int64
	require.NoError(t, s.db.Model(&domain.BatchInvocation{}).
		Where("c_space_id = ? AND c_batch_id = ?", batch.SpaceID, batch.BatchID).Count(&retainedBatchCount).Error)
	require.EqualValues(t, 1, retainedBatchCount, "the waiting Timer manifest keeps its timed-out Batch eligible for one late completion")
	var retainedItemCount int64
	require.NoError(t, s.db.Table("t_collector_fetch_batch_items").
		Where("c_space_id = ? AND c_batch_id = ? AND c_instance_id = ?", batch.SpaceID, batch.BatchID, "late-instance").
		Count(&retainedItemCount).Error)
	require.EqualValues(t, 1, retainedItemCount, "scope membership must remain until the eligible late completion is consumed")

	deleted, err := s.FetchRetries().CleanupSpaceWithCount(ctx, "crypto", now, 10)
	require.NoError(t, err)
	require.Zero(t, deleted, "a timed-out Timer batch still accepts its first late completion and needs the terminal Retry fence")
	_, err = s.FetchRetries().Get(ctx, retry.SpaceID, retry.RetryKey)
	require.NoError(t, err)
	batch.LateCompletion = true
	batch.CompletedAt = &now
	updated, err = s.FetchBatches().CompleteWithEffects(ctx, batch, FetchCompletionEffects{
		RetrySourceGuards: map[string]string{retry.RetryKey: retry.RetryKey},
		Retries: []*domain.RetryItem{{
			SpaceID: "crypto", RetryKey: retry.RetryKey, InstanceID: "late-instance", Status: "pending", Attempt: 2,
		}},
	})
	require.NoError(t, err)
	require.True(t, updated, "the first late completion should be recorded")
	storedRetry, err := s.FetchRetries().Get(ctx, retry.SpaceID, retry.RetryKey)
	require.NoError(t, err)
	require.Equal(t, "succeeded", storedRetry.Status, "the retained terminal fence prevents old completion from reviving retry work")
}

func TestFetchRetryRetentionRemainsIndependentFromTerminalBatchRetention(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	completedAt := now.Add(-48 * time.Hour)
	retry := &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "terminal-retry-2d", Status: "succeeded",
		TargetDataTime: completedAt, CreateTime: completedAt, ModifyTime: completedAt,
	}
	require.NoError(t, s.FetchRetries().Upsert(ctx, retry))
	require.NoError(t, s.db.Model(&domain.RetryItem{}).
		Where("c_space_id = ? AND c_retry_key = ?", retry.SpaceID, retry.RetryKey).
		Updates(map[string]any{"c_ctime": completedAt, "c_mtime": completedAt}).Error)

	require.NoError(t, s.FetchBatches().Cleanup(ctx, now.Add(-24*time.Hour)))
	require.NoError(t, s.FetchRetries().Cleanup(ctx, now.Add(-7*24*time.Hour)))

	var count int64
	require.NoError(t, s.db.Model(&domain.RetryItem{}).
		Where("c_space_id = ? AND c_retry_key = ?", retry.SpaceID, retry.RetryKey).Count(&count).Error)
	require.EqualValues(t, 1, count, "terminal Retry older than the Batch TTL must remain until its independent 7d cutoff")
}

func TestFetchRetryCleanupPagesUseIndexesWithoutSorting(t *testing.T) {
	s := newCollectorStore(t)
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	for _, test := range []struct {
		query string
		index string
	}{
		{query: retryCleanupSucceededPageSQL, index: "idx_collector_fetch_retry_cleanup_succeeded"},
		{query: retryCleanupPermanentPageSQL, index: "idx_collector_fetch_retry_cleanup_permanent"},
	} {
		var plan []struct {
			Detail string `gorm:"column:detail"`
		}
		require.NoError(t, s.db.Raw("EXPLAIN QUERY PLAN "+test.query, cutoff, maxTerminalRetryCleanupRows).Scan(&plan).Error)
		details := make([]string, 0, len(plan))
		for _, row := range plan {
			details = append(details, row.Detail)
		}
		planText := fmt.Sprint(details)
		require.Contains(t, planText, test.index)
		require.NotContains(t, planText, "TEMP B-TREE", "bounded retry cleanup must not sort its terminal candidate set")
	}
	for _, test := range []struct {
		query string
		index string
	}{
		{query: retryCleanupSucceededPageSpaceSQL, index: "idx_collector_fetch_retry_cleanup_succeeded_space"},
		{query: retryCleanupPermanentPageSpaceSQL, index: "idx_collector_fetch_retry_cleanup_permanent_space"},
	} {
		var plan []struct {
			Detail string `gorm:"column:detail"`
		}
		require.NoError(t, s.db.Raw("EXPLAIN QUERY PLAN "+test.query, "crypto", cutoff, maxTerminalRetryCleanupRows).Scan(&plan).Error)
		details := make([]string, 0, len(plan))
		for _, row := range plan {
			details = append(details, row.Detail)
		}
		planText := fmt.Sprint(details)
		require.Contains(t, planText, test.index)
		require.NotContains(t, planText, "TEMP B-TREE")
	}
}

func TestFetchRetryCleanupSpaceKeepsOtherSpaceRows(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	for _, spaceID := range []string{"crypto", "stockcn"} {
		require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
			SpaceID: spaceID, RetryKey: "cleanup-" + spaceID, Status: "succeeded",
			TargetDataTime: old, CreateTime: old, ModifyTime: old,
		}))
		require.NoError(t, s.db.Model(&domain.RetryItem{}).Where("c_space_id = ? AND c_retry_key = ?", spaceID, "cleanup-"+spaceID).Update("c_mtime", old).Error)
	}
	require.NoError(t, s.FetchRetries().CleanupSpace(ctx, "crypto", time.Now().UTC().Add(-7*24*time.Hour), 1))
	for _, spaceID := range []string{"crypto", "stockcn"} {
		var count int64
		require.NoError(t, s.db.Model(&domain.RetryItem{}).Where("c_space_id = ?", spaceID).Count(&count).Error)
		if spaceID == "crypto" {
			require.Zero(t, count)
		} else {
			require.EqualValues(t, 1, count)
		}
	}
}

func TestListDueRecentPageUsesScopedIndex(t *testing.T) {
	s := newCollectorStore(t)
	now := time.Now().UTC()
	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	err := s.db.Raw(`EXPLAIN QUERY PLAN SELECT * FROM t_collector_fetch_retry_items
		WHERE c_space_id = ? AND c_status = ? AND c_next_retry_at IS NOT NULL AND c_next_retry_at <= ? AND c_period_deadline_at IS NOT NULL AND c_period_deadline_at > ?
		ORDER BY c_period_deadline_at ASC, c_next_retry_at ASC LIMIT ?`,
		"crypto", "pending", now, now, 480,
	).Scan(&plan).Error
	require.NoError(t, err)
	var details []string
	for _, row := range plan {
		details = append(details, row.Detail)
	}
	require.Contains(t, fmt.Sprint(details), "idx_collector_fetch_retry_period_due")
	require.NotContains(t, fmt.Sprint(details), "TEMP B-TREE", "recent retry ordering should be covered by the composite index")
}
