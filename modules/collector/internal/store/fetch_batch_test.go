package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

func TestFetchBatchCleanupCountsCommittedRows(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	old := time.Now().Add(-25 * time.Hour)
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "metrics-batch", ScheduleID: "metrics-batch", BatchKind: domain.BatchKindRealtime}
	_, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&domain.BatchInvocation{}).Where("c_batch_id = ?", batch.BatchID).Updates(map[string]any{"c_status": domain.BatchStatusSucceeded, "c_completed_at": old}).Error)
	require.NoError(t, s.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, []string{"instance-secret"}))
	require.NoError(t, s.db.Exec(`CREATE TRIGGER fail_batch_delete BEFORE DELETE ON t_collector_fetch_batches BEGIN SELECT RAISE(ABORT, 'test rollback'); END`).Error)
	items, batches, err := s.FetchBatches().CleanupSpaceWithCounts(ctx, "crypto", time.Now(), 10, 10)
	require.Error(t, err)
	require.Zero(t, items)
	require.Zero(t, batches)
	require.NoError(t, s.db.Exec(`DROP TRIGGER fail_batch_delete`).Error)
	items, batches, err = s.FetchBatches().CleanupSpaceWithCounts(ctx, "crypto", time.Now(), 10, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, items)
	require.EqualValues(t, 1, batches)
	items, batches, err = s.FetchBatches().CleanupSpaceWithCounts(ctx, "crypto", time.Now(), 10, 10)
	require.NoError(t, err)
	require.Zero(t, items)
	require.Zero(t, batches)
}

func TestFetchBatchCleanupDeletesItemsWithExpiredTerminalBatch(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	completedAt := now.Add(-25 * time.Hour)
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "expired-complete", ScheduleID: "expired-complete", BatchKind: domain.BatchKindRealtime, Status: domain.BatchStatusSucceeded, CompletedAt: &completedAt}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, s.db.Model(&domain.BatchInvocation{}).Where("c_space_id = ? AND c_batch_id = ?", "crypto", batch.BatchID).Updates(map[string]any{"c_status": domain.BatchStatusSucceeded, "c_completed_at": completedAt}).Error)
	require.NoError(t, s.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, []string{"retired-instance"}))

	require.NoError(t, s.FetchBatches().Cleanup(ctx, now.Add(-24*time.Hour)))

	_, err = s.FetchBatches().Get(ctx, "crypto", batch.BatchID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var itemCount int64
	require.NoError(t, s.db.Table("t_collector_fetch_batch_items").Where("c_space_id = ? AND c_batch_id = ?", "crypto", batch.BatchID).Count(&itemCount).Error)
	require.Zero(t, itemCount, "batch cleanup must not leave high-volume orphan item rows")
}

func TestFetchBatchCleanupUsesOneRetentionForEveryTerminalStatus(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	completedAt := now.Add(-25 * time.Hour)
	statuses := []domain.BatchStatus{
		domain.BatchStatusSucceeded,
		domain.BatchStatusPartialFailed,
		domain.BatchStatusFailed,
		domain.BatchStatusTimedOut,
	}
	for _, status := range statuses {
		batchID := "expired-" + string(status)
		batch := &domain.BatchInvocation{
			SpaceID: "crypto", BatchID: batchID, ScheduleID: batchID,
			BatchKind: domain.BatchKindRealtime, Status: domain.BatchStatusPlanned,
		}
		created, err := s.FetchBatches().CreatePlanned(ctx, batch)
		if err != nil {
			t.Fatal(err)
		}
		require.True(t, created)
		require.NoError(t, s.db.Model(&domain.BatchInvocation{}).
			Where("c_space_id = ? AND c_batch_id = ?", "crypto", batchID).
			Updates(map[string]any{
				"c_status": status, "c_completed_at": completedAt,
				"c_late_completion": status == domain.BatchStatusTimedOut,
			}).Error)
		require.NoError(t, s.FetchBatches().UpsertItems(ctx, "crypto", batchID, []string{"instance-" + string(status)}))
	}

	require.NoError(t, s.FetchBatches().Cleanup(ctx, now.Add(-24*time.Hour)))

	for _, status := range statuses {
		batchID := "expired-" + string(status)
		_, err := s.FetchBatches().Get(ctx, "crypto", batchID)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound, "terminal batch status %q must use the common 24h retention", status)
		var items int64
		require.NoError(t, s.db.Table("t_collector_fetch_batch_items").Where("c_space_id = ? AND c_batch_id = ?", "crypto", batchID).Count(&items).Error)
		require.Zero(t, items, "terminal batch items must use the common 24h retention")
	}
}

func TestFetchBatchCleanupWaitsForLateCompletionOnlyWithinRetention(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	createTimedOut := func(batchID, instanceID string, completedAt time.Time) *domain.BatchInvocation {
		batch := &domain.BatchInvocation{
			SpaceID: "crypto", BatchID: batchID, ScheduleID: batchID,
			BatchKind: domain.BatchKindRealtime, Frequency: "1m", Status: domain.BatchStatusPlanned,
			RequestJSON: `{"items":[{"instance_id":"` + instanceID + `","source_event_id":"` + batchID + `-retry"}]}`,
		}
		created, err := s.FetchBatches().CreatePlanned(ctx, batch)
		require.NoError(t, err)
		require.True(t, created)
		require.NoError(t, s.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, []string{instanceID}))
		batch.Status = domain.BatchStatusTimedOut
		batch.CompletedAt = &completedAt
		updated, err := s.FetchBatches().Complete(ctx, batch)
		require.NoError(t, err)
		require.True(t, updated)
		return batch
	}
	itemCount := func(batchID string) int64 {
		var count int64
		require.NoError(t, s.db.Table("t_collector_fetch_batch_items").Where("c_space_id = ? AND c_batch_id = ?", "crypto", batchID).Count(&count).Error)
		return count
	}
	recent := createTimedOut("late-scope", "late-instance", now.Add(-23*time.Hour))
	expired := createTimedOut("expired-scope", "expired-instance", now.Add(-25*time.Hour))

	// Two cleanup calls: the first clears items, the second their parents.
	for range 2 {
		require.NoError(t, s.FetchBatches().Cleanup(ctx, now.Add(-24*time.Hour)))
	}
	_, err := s.FetchBatches().Get(ctx, "crypto", recent.BatchID)
	require.NoError(t, err, "a timed-out batch inside the retention window remains eligible for its first late completion")
	require.EqualValues(t, 1, itemCount(recent.BatchID), "completion scope membership must survive while a late completion is accepted")
	_, err = s.FetchBatches().Get(ctx, "crypto", expired.BatchID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "retention bounds the wait for a late completion")
	require.Zero(t, itemCount(expired.BatchID))

	recent.LateCompletion = true
	recent.CompletedAt = &now
	updated, err := s.FetchBatches().CompleteWithEffects(ctx, recent, FetchCompletionEffects{
		Retries: []*domain.RetryItem{{
			SpaceID: "crypto", RetryKey: "late-retry", SourceBatchID: recent.BatchID,
			InstanceID: "late-instance", Status: "pending", Attempt: 1,
			TargetDataTime: now, CreateTime: now, ModifyTime: now,
		}},
	})
	require.NoError(t, err)
	require.True(t, updated, "the first late completion must still pass scope validation and commit its retry effect")
	retry, err := s.FetchRetries().Get(ctx, "crypto", "late-retry")
	require.NoError(t, err)
	require.Equal(t, "pending", retry.Status)
}

func TestFetchBatchCleanupSpaceDoesNotConsumeOtherSpaceBudget(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	completedAt := now.Add(-25 * time.Hour)
	for _, spaceID := range []string{"crypto", "stockcn"} {
		batch := &domain.BatchInvocation{SpaceID: spaceID, BatchID: "expired-" + spaceID, ScheduleID: "expired-" + spaceID, BatchKind: domain.BatchKindRealtime, Status: domain.BatchStatusSucceeded, CompletedAt: &completedAt}
		created, err := s.FetchBatches().CreatePlanned(ctx, batch)
		require.NoError(t, err)
		require.True(t, created)
		require.NoError(t, s.db.Model(&domain.BatchInvocation{}).Where("c_space_id = ? AND c_batch_id = ?", spaceID, batch.BatchID).Updates(map[string]any{"c_status": domain.BatchStatusSucceeded, "c_completed_at": completedAt}).Error)
		require.NoError(t, s.FetchBatches().UpsertItems(ctx, spaceID, batch.BatchID, []string{"instance-" + spaceID}))
	}
	require.NoError(t, s.FetchBatches().CleanupSpace(ctx, "crypto", now.Add(-24*time.Hour), 1, 1))
	for _, spaceID := range []string{"crypto", "stockcn"} {
		var items, batches int64
		require.NoError(t, s.db.Table("t_collector_fetch_batch_items").Where("c_space_id = ?", spaceID).Count(&items).Error)
		require.NoError(t, s.db.Model(&domain.BatchInvocation{}).Where("c_space_id = ?", spaceID).Count(&batches).Error)
		if spaceID == "crypto" {
			require.Zero(t, items)
			require.Zero(t, batches)
		} else {
			require.EqualValues(t, 1, items)
			require.EqualValues(t, 1, batches)
		}
	}
}

func TestFetchBatchCleanupDeletesTerminalTimerItemsButKeepsManifestBatch(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	completedAt := now.Add(-72 * time.Hour)
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "expired-timer", ScheduleID: "expired-timer", BatchKind: domain.BatchKindRealtime, Status: domain.BatchStatusSucceeded, CompletedAt: &completedAt}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, s.db.Model(&domain.BatchInvocation{}).Where("c_space_id = ? AND c_batch_id = ?", "crypto", batch.BatchID).Updates(map[string]any{"c_status": domain.BatchStatusSucceeded, "c_completed_at": completedAt}).Error)
	require.NoError(t, s.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, []string{"retired-timer-instance"}))
	periodTime := now.Add(-time.Minute)
	require.NoError(t, s.db.Create(&domain.TimerPeriodBatch{
		Key: "expired-timer-manifest", SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: periodTime,
		TaskID: "task", FirstRunID: "run", SeriesHash: "hash", ExpectedCount: 1, GroupCount: 1,
		BindingHash: "binding", RouteVersion: "route-v1", BatchID: batch.BatchID, FunctionName: "timer-fn", NodeID: "node",
		Region: "region", DeadlineAt: now.Add(time.Hour),
	}).Error)

	require.NoError(t, s.FetchBatches().Cleanup(ctx, now.Add(-48*time.Hour)))

	var itemCount, batchCount, manifestCount int64
	require.NoError(t, s.db.Table("t_collector_fetch_batch_items").Where("c_space_id = ? AND c_batch_id = ?", "crypto", batch.BatchID).Count(&itemCount).Error)
	require.Zero(t, itemCount, "terminal Timer items are not needed while their compact manifest is retained")
	require.NoError(t, s.db.Model(&domain.BatchInvocation{}).Where("c_space_id = ? AND c_batch_id = ?", "crypto", batch.BatchID).Count(&batchCount).Error)
	require.EqualValues(t, 1, batchCount, "the manifest must retain its terminal batch identity")
	require.NoError(t, s.db.Model(&domain.TimerPeriodBatch{}).Where("c_space_id = ? AND c_batch_id = ?", "crypto", batch.BatchID).Count(&manifestCount).Error)
	require.EqualValues(t, 1, manifestCount)
}

func TestFetchBatchCleanupPagesUseIndexesWithoutSorting(t *testing.T) {
	s := newCollectorStore(t)
	cutoff := time.Now().UTC().Add(-48 * time.Hour)
	for _, query := range []string{fetchBatchCleanupItemsPageSQL, fetchBatchCleanupBatchesPageSQL} {
		var plan []struct {
			Detail string `gorm:"column:detail"`
		}
		require.NoError(t, s.db.Raw("EXPLAIN QUERY PLAN "+query, domain.BatchStatusSucceeded, cutoff, maxFetchBatchCleanupItemRows).Scan(&plan).Error)
		details := make([]string, 0, len(plan))
		for _, row := range plan {
			details = append(details, row.Detail)
		}
		planText := fmt.Sprint(details)
		require.Contains(t, planText, "idx_collector_fetch_batch_terminal_")
		require.NotContains(t, planText, "TEMP B-TREE", "bounded cleanup must not sort its terminal candidate set")
	}
	for _, query := range []string{fetchBatchCleanupItemsPageSpaceSQL, fetchBatchCleanupBatchesPageSpaceSQL} {
		var plan []struct {
			Detail string `gorm:"column:detail"`
		}
		require.NoError(t, s.db.Raw("EXPLAIN QUERY PLAN "+query, "crypto", domain.BatchStatusSucceeded, cutoff, maxFetchBatchCleanupItemRows).Scan(&plan).Error)
		details := make([]string, 0, len(plan))
		for _, row := range plan {
			details = append(details, row.Detail)
		}
		planText := fmt.Sprint(details)
		require.Contains(t, planText, "cleanup_space")
		require.NotContains(t, planText, "TEMP B-TREE")
	}
}

func TestFetchBatchCleanupDeletesOnlyABoundedItemPage(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	completedAt := now.Add(-72 * time.Hour)
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "expired-many", ScheduleID: "expired-many", BatchKind: domain.BatchKindRealtime, Status: domain.BatchStatusSucceeded, CompletedAt: &completedAt}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	count := maxFetchBatchCleanupItemRows + 1
	require.NoError(t, s.db.Exec(`WITH RECURSIVE seq(n) AS (
		SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?
	) INSERT INTO t_collector_fetch_batch_items(c_space_id, c_batch_id, c_instance_id)
	SELECT 'crypto', 'expired-many', 'instance-' || n FROM seq`, count).Error)

	cutoff := now.Add(-48 * time.Hour)
	require.NoError(t, s.FetchBatches().Cleanup(ctx, cutoff))
	var itemCount, batchCount int64
	require.NoError(t, s.db.Table("t_collector_fetch_batch_items").Where("c_space_id = ? AND c_batch_id = ?", "crypto", batch.BatchID).Count(&itemCount).Error)
	require.EqualValues(t, 1, itemCount, "one cleanup transaction must not delete an unbounded child set")
	require.NoError(t, s.db.Model(&domain.BatchInvocation{}).Where("c_space_id = ? AND c_batch_id = ?", "crypto", batch.BatchID).Count(&batchCount).Error)
	require.EqualValues(t, 1, batchCount, "parent batch remains until its child page is fully deleted")

	require.NoError(t, s.FetchBatches().Cleanup(ctx, cutoff))
	require.NoError(t, s.db.Table("t_collector_fetch_batch_items").Where("c_space_id = ? AND c_batch_id = ?", "crypto", batch.BatchID).Count(&itemCount).Error)
	require.Zero(t, itemCount)
	require.NoError(t, s.db.Model(&domain.BatchInvocation{}).Where("c_space_id = ? AND c_batch_id = ?", "crypto", batch.BatchID).Count(&batchCount).Error)
	require.Zero(t, batchCount)
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

func TestListDuePrioritizedReservesHistoricalRecoveryCapacity(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for index := 0; index < 4; index++ {
		deadline := now.Add(-time.Hour)
		periodDeadline := now.Add(-2 * time.Hour)
		created, err := s.FetchBatches().CreatePlanned(ctx, &domain.BatchInvocation{
			SpaceID: "crypto", BatchID: fmt.Sprintf("old-%d", index), ScheduleID: fmt.Sprintf("old-%d", index), Status: domain.BatchStatusPlanned,
			DeadlineAt: &deadline, PeriodDeadlineAt: &periodDeadline,
		})
		require.NoError(t, err)
		require.True(t, created)
	}
	for index := 0; index < 4; index++ {
		deadline := now.Add(-time.Duration(index+1) * time.Minute)
		periodDeadline := now.Add(time.Hour)
		created, err := s.FetchBatches().CreatePlanned(ctx, &domain.BatchInvocation{
			SpaceID: "crypto", BatchID: fmt.Sprintf("recent-%d", index), ScheduleID: fmt.Sprintf("recent-%d", index), Status: domain.BatchStatusPlanned,
			DeadlineAt: &deadline, PeriodDeadlineAt: &periodDeadline,
		})
		require.NoError(t, err)
		require.True(t, created)
	}

	recent, historical, err := s.FetchBatches().ListDuePrioritized(ctx, "crypto", now, 3, 1)
	require.NoError(t, err)
	require.Len(t, recent, 3)
	require.Len(t, historical, 1)
	require.Equal(t, "recent-3", recent[0].BatchID, "the oldest currently-expiring batch is most urgent")
	require.Equal(t, "recent-2", recent[1].BatchID)
	require.Equal(t, "recent-1", recent[2].BatchID)
	require.Equal(t, "old-0", historical[0].BatchID, "historical recovery still makes bounded progress")
}

func TestListDuePrioritizedKeepsOldPeriodRetryHistoricalAfterDeadlineRefresh(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	oldTarget := now.Add(-20 * time.Minute).Format(time.RFC3339Nano)
	requestJSON := fmt.Sprintf(`{"items":[{"target_data_time":%q}]}`, oldTarget)
	plannedAt := now
	deadline := now.Add(-time.Minute)
	periodDeadline := now.Add(-time.Minute)
	batch := &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: "old-period-retry", ScheduleID: "retry:old-period-retry",
		BatchKind: domain.BatchKindRealtime, Status: domain.BatchStatusPlanned, PlannedAt: &plannedAt,
		DeadlineAt: &deadline, PeriodDeadlineAt: &periodDeadline, RequestJSON: requestJSON,
	}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	updated, err := s.FetchBatches().MakePlannedRetryBatchDue(ctx, "crypto", batch.BatchID, now.Add(-time.Second))
	require.NoError(t, err)
	require.True(t, updated)

	recent, historical, err := s.FetchBatches().ListDuePrioritized(ctx, "crypto", now, 1, 1)
	require.NoError(t, err)
	require.Empty(t, recent, "refreshing an old retry batch deadline must not promote its old data period")
	require.Len(t, historical, 1)
	require.Equal(t, batch.BatchID, historical[0].BatchID)
}

func TestListDuePrioritizedQueriesUseDeadlineIndexesWithoutSorting(t *testing.T) {
	s := newCollectorStore(t)
	now := time.Now().UTC()
	queries := []struct {
		query   string
		args    []any
		indexes []string
	}{
		{
			query: `SELECT c_id FROM t_collector_fetch_batches WHERE c_space_id = ? AND c_status = ? AND c_period_deadline_at > ?
				AND c_deadline_at IS NOT NULL AND c_deadline_at <= ? ORDER BY c_period_deadline_at,c_deadline_at,c_id LIMIT ?`,
			args: []any{"crypto", domain.BatchStatusDispatched, now, now, 10}, indexes: []string{"idx_collector_fetch_batch_period_due"},
		},
		{
			query: `SELECT c_id FROM t_collector_fetch_batches WHERE c_space_id = ? AND c_status = ? AND c_period_deadline_at > ? AND (
				(c_deadline_at IS NOT NULL AND c_deadline_at <= ?) OR EXISTS (
					SELECT 1 FROM t_collector_timer_period_batches AS manifests
					WHERE manifests.c_space_id=t_collector_fetch_batches.c_space_id AND manifests.c_batch_id=t_collector_fetch_batches.c_batch_id
					AND manifests.c_claim_request_id='' AND manifests.c_deadline_at <= ?))
				ORDER BY c_period_deadline_at,c_deadline_at,c_id LIMIT ?`,
			args: []any{"crypto", domain.BatchStatusPlanned, now, now, now, 10}, indexes: []string{"idx_collector_fetch_batch_period_due", "idx_collector_fetch_batch_deadline"},
		},
		{
			query: `SELECT c_id FROM t_collector_fetch_batches WHERE c_space_id = ? AND c_status = ? AND (c_period_deadline_at IS NULL OR c_period_deadline_at <= ?)
				AND c_deadline_at IS NOT NULL AND c_deadline_at <= ? ORDER BY c_deadline_at,c_id LIMIT ?`,
			args: []any{"crypto", domain.BatchStatusDispatched, now, now, 10}, indexes: []string{"idx_collector_fetch_batch_due_scope", "idx_collector_fetch_batch_deadline"},
		},
	}
	for _, candidate := range queries {
		var plan []struct {
			Detail string `gorm:"column:detail"`
		}
		require.NoError(t, s.db.Raw("EXPLAIN QUERY PLAN "+candidate.query, candidate.args...).Scan(&plan).Error)
		planText := fmt.Sprint(plan)
		usesDeadlineIndex := false
		for _, index := range candidate.indexes {
			usesDeadlineIndex = usesDeadlineIndex || strings.Contains(planText, index)
		}
		require.True(t, usesDeadlineIndex, "query plan should use a bounded deadline index: %s", planText)
		require.NotContains(t, planText, "TEMP B-TREE", "prioritized recovery must not sort the active batch backlog")
	}
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
		TargetDataTime: now, TaskJSON: `{"market_type":"spot"}`, FailureTargetsJSON: string(failureTargetsJSON), Status: "pending", Attempt: 3,
	}))

	require.NoError(t, s.FetchRetries().MarkPermanent(ctx, "crypto", "retry-a", "retry_budget_exhausted", "upstream timed out"))
	retry, err := s.FetchRetries().Get(ctx, "crypto", "retry-a")
	require.NoError(t, err)
	require.Equal(t, "permanent_failed", retry.Status)
	require.Equal(t, "retry_budget_exhausted", retry.LastErrorType)
	require.Nil(t, retry.NextRetryAt)
	require.Equal(t, domain.PeriodFailureReportPending, retry.PeriodFailureReportState)
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

func TestPermanentRetryWithoutReportPayloadIsTerminalAndCleaned(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	targetsJSON, err := json.Marshal([]domain.WriteTarget{{
		ID: "target-a", SpaceID: "crypto", InstanceID: "instance-a", DatasetID: "bars",
		SeriesIndex: 0, SeriesHash: "hash", ExpectedCount: 1,
	}})
	require.NoError(t, err)

	duplicateTargetsJSON, err := json.Marshal([]domain.WriteTarget{
		{ID: "target-a", SpaceID: "crypto", InstanceID: "instance-a", DatasetID: "bars", SeriesIndex: 0, SeriesHash: "hash", ExpectedCount: 1},
		{ID: " target-a ", SpaceID: "crypto", InstanceID: "instance-a", DatasetID: "bars", SeriesIndex: 0, SeriesHash: "hash", ExpectedCount: 1},
	})
	require.NoError(t, err)
	noncanonicalTargetJSON, err := json.Marshal([]domain.WriteTarget{{
		ID: " target-a ", SpaceID: "crypto", InstanceID: "instance-a", DatasetID: "bars",
		SeriesIndex: 0, SeriesHash: "hash", ExpectedCount: 1,
	}})
	require.NoError(t, err)
	inconsistentRosterJSON, err := json.Marshal([]domain.WriteTarget{
		{ID: "target-a", SpaceID: "crypto", InstanceID: "instance-a", DatasetID: "bars", SeriesIndex: 0, SeriesHash: "hash-a", ExpectedCount: 1},
		{ID: "target-b", SpaceID: "crypto", InstanceID: "instance-b", DatasetID: "bars", SeriesIndex: 0, SeriesHash: "hash-b", ExpectedCount: 1},
	})
	require.NoError(t, err)
	for _, tc := range []struct {
		name        string
		taskJSON    string
		targetsJSON string
		frequency   string
		periodTime  time.Time
	}{
		{name: "malformed task JSON", taskJSON: "not-json", targetsJSON: string(targetsJSON), frequency: "1m", periodTime: now},
		{name: "missing market type", taskJSON: `{"frequency":"1m","target_data_time":"2026-10-02T12:00:00Z"}`, targetsJSON: string(targetsJSON), frequency: "1m", periodTime: now},
		{name: "unsupported market type", taskJSON: `{"market_type":"bogus"}`, targetsJSON: string(targetsJSON), frequency: "1m", periodTime: now},
		{name: "duplicate target IDs", taskJSON: `{"market_type":"spot"}`, targetsJSON: string(duplicateTargetsJSON), frequency: "1m", periodTime: now},
		{name: "noncanonical target ID", taskJSON: `{"market_type":"spot"}`, targetsJSON: string(noncanonicalTargetJSON), frequency: "1m", periodTime: now},
		{name: "inconsistent dataset roster", taskJSON: `{"market_type":"spot"}`, targetsJSON: string(inconsistentRosterJSON), frequency: "1m", periodTime: now},
		{name: "task frequency fallback", taskJSON: `{"market_type":"spot","frequency":"1m"}`, targetsJSON: string(targetsJSON), periodTime: now},
		{name: "task period fallback", taskJSON: `{"market_type":"spot","target_data_time":"2026-10-02T12:00:00Z"}`, targetsJSON: string(targetsJSON), frequency: "1m"},
		{name: "noncanonical retry frequency", taskJSON: `{"market_type":"spot"}`, targetsJSON: string(targetsJSON), frequency: " 1m ", periodTime: now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCollectorStore(t)
			require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
				SpaceID: "crypto", RetryKey: "retry-a", InstanceID: "instance-a", SubjectID: "ETH-USDT", Frequency: tc.frequency,
				TargetDataTime: tc.periodTime, TaskJSON: tc.taskJSON, FailureTargetsJSON: tc.targetsJSON, Status: "pending", Attempt: 3,
			}))

			require.NoError(t, s.FetchRetries().MarkPermanent(ctx, "crypto", "retry-a", "invalid_retry_payload", "invalid retry task"))
			retry, err := s.FetchRetries().Get(ctx, "crypto", "retry-a")
			require.NoError(t, err)
			require.Equal(t, domain.PeriodFailureReportMissedDeadline, retry.PeriodFailureReportState)
			require.Equal(t, "durable period failure snapshot unavailable", retry.PeriodFailureLastError)

			require.NoError(t, s.FetchRetries().Cleanup(ctx, now.Add(time.Hour)))
			_, err = s.FetchRetries().Get(ctx, "crypto", "retry-a")
			require.Error(t, err)
		})
	}
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
			require.ErrorIs(t, err, ErrInvalidCompletionScope)
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
