package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func attachTestWriteTarget(t *testing.T, s *Store, ctx context.Context, spaceID, instanceID, taskID, datasetID string) {
	t.Helper()
	require.NoError(t, s.db.WithContext(ctx).Exec(`INSERT OR IGNORE INTO t_collector_tasks(c_space_id,c_task_id,c_task_name,c_data_type,c_enabled,c_prepare_state) VALUES(?,?,?,?,1,'ready')`, spaceID, taskID, taskID, "kline").Error)
	require.NoError(t, s.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{{
		ID: "wt-" + instanceID + "-" + taskID, SpaceID: spaceID, InstanceID: instanceID,
		TaskID: taskID, DatasetID: datasetID, Status: "pending",
	}}))
}

func TestTaskInstanceRepositoryUpsertKeepsFreshnessForStableTask(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	instance := domain.TaskInstance{
		SpaceID: "crypto", InstanceID: "task-1", Provider: "binance", MarketType: "spot",
		DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`,
	}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	first, err := s.TaskInstances().Get(ctx, "crypto", "task-1")
	require.NoError(t, err)
	require.Nil(t, first.LastExecTime)

	instance.TaskParams = `{"batch_size":10}`
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	stored, err := s.TaskInstances().Get(ctx, "crypto", "task-1")
	require.NoError(t, err)
	assert.Equal(t, first.LastExecTime, stored.LastExecTime)
	assert.Equal(t, "1m", stored.Frequency)
	assert.Equal(t, "binance", stored.Provider)
}

func TestTaskInstanceRepositoryRejectsWriteTargetWithoutSpace(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task", TaskName: "Task", DataType: "kline", Enabled: true,
	}))
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "instance", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m",
	}}))
	err := s.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{{
		ID: "target", InstanceID: "instance", TaskID: "task", DatasetID: "bars",
	}})
	require.ErrorIs(t, err, gorm.ErrInvalidData)
	var count int64
	require.NoError(t, s.db.Model(&domain.WriteTarget{}).Where("c_write_target_id = ?", "target").Count(&count).Error)
	require.Zero(t, count, "a target without Space ownership must not be persisted")
}

func TestTaskInstanceRepositoryPreservesAssignedSourceOnPlannerUpsert(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	initial := domain.TaskInstance{
		SpaceID: "stockcn", InstanceID: "stock-task",
		Provider: "stockcn_multi", SourceID: "stockcn_http", MarketType: "equity",
		DataType: "kline", SubjectID: "600000.XSHG",
		Frequency: "1m", TaskParams: `{}`,
	}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{initial}))
	plannerDefinition := initial
	plannerDefinition.SourceID = ""
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{plannerDefinition}))
	stored, err := s.TaskInstances().Get(ctx, initial.SpaceID, initial.InstanceID)
	require.NoError(t, err)
	assert.Equal(t, initial.SourceID, stored.SourceID)
}

func TestTaskInstanceRepositoryUpsertSkipsUnchangedDefinition(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	instance := domain.TaskInstance{
		SpaceID: "crypto", InstanceID: "task-1", Provider: "binance", MarketType: "spot",
		DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`,
	}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	first, err := s.TaskInstances().Get(ctx, "crypto", "task-1")
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	second, err := s.TaskInstances().Get(ctx, "crypto", "task-1")
	require.NoError(t, err)
	assert.Equal(t, first.ModifyTime, second.ModifyTime)
}

func TestTaskInstanceRepositoryUpsertBatchesLargeCatalogue(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	instances := make([]domain.TaskInstance, 0, 1200)
	for index := 0; index < 1200; index++ {
		instances = append(instances, domain.TaskInstance{
			SpaceID: "stockcn", InstanceID: fmt.Sprintf("task-%04d", index),
			Provider: "stockcn_multi", MarketType: "equity", DataType: "kline",
			SubjectID: fmt.Sprintf("600%03d.XSHG", index), Frequency: "1m", TaskParams: `{}`,
		})
	}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, instances))
	_, total, err := s.TaskInstances().List(ctx, TaskInstanceFilter{SpaceID: "stockcn", Page: 1, PageSize: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(len(instances)), total)
}

func TestTaskInstanceListFiltersSharedInstanceThroughWriteTargets(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	instance := domain.TaskInstance{
		SpaceID: "crypto", InstanceID: "shared-btc", RunID: "run-1", RequestKey: "request-btc",
		Provider: "binance", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m",
	}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	attachTestWriteTarget(t, s, ctx, "crypto", instance.InstanceID, "task-a", "bars-a")
	attachTestWriteTarget(t, s, ctx, "crypto", instance.InstanceID, "task-b", "bars-b")

	for _, filter := range []TaskInstanceFilter{
		{SpaceID: "crypto", CollectionTaskID: "task-a"},
		{SpaceID: "crypto", CollectionTaskID: "task-b"},
		{SpaceID: "crypto", DatasetID: "bars-a"},
		{SpaceID: "crypto", DatasetID: "bars-b"},
	} {
		instances, total, err := s.TaskInstances().List(ctx, filter)
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Len(t, instances, 1)
		require.Equal(t, instance.InstanceID, instances[0].InstanceID)
	}
}

func TestTaskInstanceFilterMatchesExactCollectionTaskIDAndUsesIndex(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	instances := []domain.TaskInstance{
		{SpaceID: "crypto", InstanceID: "instance-exact", Provider: "binance", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m"},
		{SpaceID: "crypto", InstanceID: "instance-substring", Provider: "binance", DataType: "kline", SubjectID: "ETH-USDT", Frequency: "1m"},
	}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, instances))
	attachTestWriteTarget(t, s, ctx, "crypto", "instance-exact", "task-a", "bars-a")
	attachTestWriteTarget(t, s, ctx, "crypto", "instance-substring", "prefix-task-a-suffix", "bars-b")

	rows, total, err := s.TaskInstances().List(ctx, TaskInstanceFilter{
		SpaceID: "crypto", CollectionTaskID: "task-a", Page: 1, PageSize: 10,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "instance-exact", rows[0].InstanceID)

	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	err = s.db.Raw(`EXPLAIN QUERY PLAN SELECT instance.c_instance_id FROM t_collector_task_instances AS instance
		WHERE instance.c_space_id = ? AND EXISTS (
			SELECT 1 FROM t_collector_instance_write_targets AS targets
			WHERE targets.c_space_id = instance.c_space_id
			  AND targets.c_instance_id = instance.c_instance_id
			  AND targets.c_task_id = ?
		)`, "crypto", "task-a").Scan(&plan).Error
	require.NoError(t, err)
	var details []string
	for _, row := range plan {
		details = append(details, row.Detail)
	}
	assert.Contains(t, fmt.Sprint(details), "sqlite_autoindex_t_collector_instance_write_targets_2")
}

func TestTaskInstanceUpsertSkipsParentTaskDisabledDuringDelete(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "task-disabled", TaskName: "Disabled", DataType: "kline", Enabled: false, CollectParams: `{}`}))
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "instance-disabled", Provider: "binance", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", CreateTime: now,
	}}))
	_, total, err := s.TaskInstances().List(ctx, TaskInstanceFilter{SpaceID: "crypto", CollectionTaskID: "task-disabled"})
	require.NoError(t, err)
	require.Zero(t, total)
}

func TestResampleTaskClaimCompleteUsesStateVersionCAS(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "rule-1", DataType: "kline_resample", Enabled: true, PrepareState: domain.PrepareStateReady}))
	next := time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC)
	initial := domain.NewResampleTaskResult(next)
	raw, err := initial.Marshal()
	require.NoError(t, err)
	instance := domain.TaskInstance{
		SpaceID: "crypto", InstanceID: "resample-btc", Provider: "moox", MarketType: "spot",
		DataType: "kline_resample", SubjectID: "BTC-USDT", Frequency: "4h", TaskParams: `{}`, Result: raw,
	}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))

	claims, err := s.TaskInstances().ClaimDueResampleTasks(ctx, next.Add(4*time.Hour+10*time.Second), domain.ResampleOriginRealtime, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	assert.Equal(t, domain.ResampleTaskStateRunning, claims[0].Result.State)
	assert.Equal(t, int64(1), claims[0].Result.StateVersion)
	assert.Equal(t, next, *claims[0].Result.ActiveBucket)

	completed, err := s.TaskInstances().CompleteResampleTask(ctx, "crypto", "resample-btc", claims[0].Result.StateVersion, next, next.Add(4*time.Hour), "hash-1")
	require.NoError(t, err)
	assert.True(t, completed)
	completed, err = s.TaskInstances().CompleteResampleTask(ctx, "crypto", "resample-btc", claims[0].Result.StateVersion, next, next.Add(4*time.Hour), "hash-1")
	require.NoError(t, err)
	assert.False(t, completed)

	stored, err := s.TaskInstances().Get(ctx, "crypto", "resample-btc")
	require.NoError(t, err)
	result, err := domain.ParseResampleTaskResult(stored.Result)
	require.NoError(t, err)
	assert.Equal(t, domain.ResampleTaskStateIdle, result.State)
	assert.Equal(t, int64(2), result.StateVersion)
	assert.Equal(t, next.Add(4*time.Hour), *result.RealtimeNextBucket)
	assert.Equal(t, "hash-1", result.LastInputHash)

	repairBucket := next.Add(4 * time.Hour)
	repairClaim, claimed, err := s.TaskInstances().ClaimResampleTask(ctx, "crypto", "resample-btc", result.StateVersion, domain.ResampleOriginRepair, repairBucket, next.Add(8*time.Hour), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	completed, err = s.TaskInstances().CompleteResampleTask(ctx, "crypto", "resample-btc", repairClaim.Result.StateVersion, repairBucket, repairBucket.Add(4*time.Hour), "hash-2")
	require.NoError(t, err)
	assert.True(t, completed)
	stored, err = s.TaskInstances().Get(ctx, "crypto", "resample-btc")
	require.NoError(t, err)
	result, err = domain.ParseResampleTaskResult(stored.Result)
	require.NoError(t, err)
	assert.Equal(t, repairBucket.Add(4*time.Hour), *result.RepairNextBucket)
	assert.Equal(t, next.Add(4*time.Hour), *result.RealtimeNextBucket)
}

func TestResampleTaskWaitAndRecoverExpiredLease(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "rule-1", DataType: "kline_resample", Enabled: true, PrepareState: domain.PrepareStateReady}))
	bucket := time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC)
	initial := domain.NewResampleTaskResult(bucket)
	raw, err := initial.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "resample-btc", Provider: "moox", DataType: "kline_resample", SubjectID: "BTC-USDT", Frequency: "4h", Result: raw,
	}}))

	claims, err := s.TaskInstances().ClaimDueResampleTasks(ctx, bucket.Add(4*time.Hour+time.Second), domain.ResampleOriginRealtime, 1, time.Second)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	retryAt := bucket.Add(4*time.Hour + time.Minute)
	updated, err := s.TaskInstances().WaitResampleSource(ctx, "crypto", "resample-btc", claims[0].Result.StateVersion, 1, retryAt, "last source bar missing")
	require.NoError(t, err)
	assert.True(t, updated)

	claims, err = s.TaskInstances().ClaimDueResampleTasks(ctx, retryAt.Add(-time.Millisecond), domain.ResampleOriginRealtime, 1, time.Second)
	require.NoError(t, err)
	assert.Empty(t, claims)
	claims, err = s.TaskInstances().ClaimDueResampleTasks(ctx, retryAt, domain.ResampleOriginRealtime, 1, time.Second)
	require.NoError(t, err)
	require.Len(t, claims, 1)

	recovered, err := s.TaskInstances().RecoverExpiredResampleLeases(ctx, retryAt.Add(2*time.Second), 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), recovered)
	stored, err := s.TaskInstances().Get(ctx, "crypto", "resample-btc")
	require.NoError(t, err)
	result, err := domain.ParseResampleTaskResult(stored.Result)
	require.NoError(t, err)
	assert.Equal(t, domain.ResampleTaskStateWaitingSource, result.State)
	assert.Nil(t, result.LeaseUntil)
}

func TestResampleBackfillSourceWaitReleasesRealtimeTask(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	bucket := time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC)
	result := domain.NewResampleTaskResult(bucket)
	result.Backfill = &domain.ResampleBackfill{RequestID: "bf-1", Start: bucket, End: bucket.Add(time.Hour), NextBucket: bucket, State: domain.ResampleBackfillRunning}
	raw, err := result.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "resample-btc", DataType: "kline_resample", SubjectID: "BTC", Frequency: "5m", Result: raw,
	}}))
	claims, err := s.TaskInstances().ClaimDueResampleTasks(ctx, bucket, domain.ResampleOriginBackfill, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	retryAt := bucket.Add(time.Minute)
	updated, err := s.TaskInstances().WaitResampleSource(ctx, "crypto", "resample-btc", claims[0].Result.StateVersion, 1, retryAt, "source bar missing")
	require.NoError(t, err)
	require.True(t, updated)
	stored, err := s.TaskInstances().Get(ctx, "crypto", "resample-btc")
	require.NoError(t, err)
	updatedResult, err := domain.ParseResampleTaskResult(stored.Result)
	require.NoError(t, err)
	assert.Equal(t, domain.ResampleTaskStateIdle, updatedResult.State)
	assert.Empty(t, updatedResult.ActiveOrigin)
	assert.Equal(t, domain.ResampleBackfillWaitingSource, updatedResult.Backfill.State)
	assert.Equal(t, retryAt, *updatedResult.Backfill.NextRetryAt)
	realtimeClaims, err := s.TaskInstances().ClaimDueResampleTasks(ctx, bucket.Add(5*time.Minute+time.Second), domain.ResampleOriginRealtime, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, realtimeClaims, 1)
}

func TestResampleBackfillFailureDoesNotFailRealtimeTask(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	bucket := time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC)
	result := domain.NewResampleTaskResult(bucket)
	result.Backfill = &domain.ResampleBackfill{RequestID: "bf-1", Start: bucket, End: bucket.Add(time.Hour), NextBucket: bucket, State: domain.ResampleBackfillRunning}
	raw, err := result.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "resample-btc", DataType: "kline_resample", SubjectID: "BTC", Frequency: "5m", Result: raw,
	}}))
	claims, err := s.TaskInstances().ClaimDueResampleTasks(ctx, bucket, domain.ResampleOriginBackfill, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	updated, err := s.TaskInstances().FailResampleBackfillTask(ctx, "crypto", "resample-btc", claims[0].Result.StateVersion, "source retention expired")
	require.NoError(t, err)
	require.True(t, updated)
	stored, err := s.TaskInstances().Get(ctx, "crypto", "resample-btc")
	require.NoError(t, err)
	updatedResult, err := domain.ParseResampleTaskResult(stored.Result)
	require.NoError(t, err)
	assert.Equal(t, domain.ResampleTaskStateIdle, updatedResult.State)
	assert.Equal(t, domain.ResampleBackfillFailed, updatedResult.Backfill.State)
	realtimeClaims, err := s.TaskInstances().ClaimDueResampleTasks(ctx, bucket.Add(5*time.Minute+time.Second), domain.ResampleOriginRealtime, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, realtimeClaims, 1)
}

func TestResampleBackfillLeaseRecoveryReleasesRealtimeTask(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	bucket := time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC)
	result := domain.NewResampleTaskResult(bucket)
	result.Backfill = &domain.ResampleBackfill{RequestID: "bf-1", Start: bucket, End: bucket.Add(time.Hour), NextBucket: bucket, State: domain.ResampleBackfillRunning}
	raw, err := result.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "resample-btc", DataType: "kline_resample", SubjectID: "BTC", Frequency: "5m", Result: raw,
	}}))
	claims, err := s.TaskInstances().ClaimDueResampleTasks(ctx, bucket, domain.ResampleOriginBackfill, 1, time.Second)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	recovered, err := s.TaskInstances().RecoverExpiredResampleLeases(ctx, bucket.Add(2*time.Second), 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), recovered)
	stored, err := s.TaskInstances().Get(ctx, "crypto", "resample-btc")
	require.NoError(t, err)
	updatedResult, err := domain.ParseResampleTaskResult(stored.Result)
	require.NoError(t, err)
	assert.Equal(t, domain.ResampleTaskStateIdle, updatedResult.State)
	assert.Equal(t, domain.ResampleBackfillWaitingSource, updatedResult.Backfill.State)
	realtimeClaims, err := s.TaskInstances().ClaimDueResampleTasks(ctx, bucket.Add(5*time.Minute+time.Second), domain.ResampleOriginRealtime, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, realtimeClaims, 1)
}

func TestResampleBackfillStartResetsRetentionExpiredRealtimeCursor(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	result := domain.NewResampleTaskResult(old)
	result.State = domain.ResampleTaskStateFailed
	result.LastError = "source Dataset retention expired for bucket 2020-01-01T00:00:00Z"
	result.Backfill = &domain.ResampleBackfill{RequestID: "old", Start: old, End: old.Add(time.Hour), NextBucket: old.Add(time.Hour), State: domain.ResampleBackfillFailed}
	raw, err := result.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "rule-1", TaskName: "rule-1", DataType: "kline_resample", Enabled: true, PrepareState: domain.PrepareStateReady}))
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "resample-btc", DataType: "kline_resample", SubjectID: "BTC", Frequency: "5m", Result: raw,
	}}))
	attachTestWriteTarget(t, s, ctx, "crypto", "resample-btc", "rule-1", "derived")
	now := time.Now().UTC().Truncate(5 * time.Minute)
	request := domain.ResampleBackfillRequest{RequestID: "new", Start: now.Add(-10 * time.Minute), End: now}
	updated, err := s.TaskInstances().StartResampleBackfill(ctx, "crypto", "rule-1", request)
	require.NoError(t, err)
	assert.Equal(t, int64(1), updated)
	stored, err := s.TaskInstances().Get(ctx, "crypto", "resample-btc")
	require.NoError(t, err)
	updatedResult, err := domain.ParseResampleTaskResult(stored.Result)
	require.NoError(t, err)
	assert.NotEqual(t, old, *updatedResult.RealtimeNextBucket)
	assert.Equal(t, domain.ResampleBackfillRunning, updatedResult.Backfill.State)
}

func TestLatestClosedRealtimeBucketUsesEpochGrid(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 34, 0, 0, time.UTC)
	got := latestClosedRealtimeBucket(now, 5*time.Hour)
	assert.Equal(t, time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC), got)
	assert.True(t, domain.IsEpochTimeAligned(got, 5*time.Hour))
	boundary := latestClosedRealtimeBucket(time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC), 5*time.Hour)
	assert.Equal(t, time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC), boundary)
}

func TestResampleRepairLeaseRecoveryReturnsToIdle(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "rule-1", DataType: "kline_resample", Enabled: true, PrepareState: domain.PrepareStateReady}))
	bucket := time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC)
	initial := domain.NewResampleTaskResult(bucket)
	raw, err := initial.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{SpaceID: "crypto", InstanceID: "repair-btc", Provider: "moox", DataType: "kline_resample", SubjectID: "BTC-USDT", Frequency: "4h", Result: raw}}))
	_, claimed, err := s.TaskInstances().ClaimResampleTask(ctx, "crypto", "repair-btc", 0, domain.ResampleOriginRepair, bucket, bucket, time.Second)
	require.NoError(t, err)
	require.True(t, claimed)
	recovered, err := s.TaskInstances().RecoverExpiredResampleLeases(ctx, bucket.Add(2*time.Second), 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), recovered)
	stored, err := s.TaskInstances().Get(ctx, "crypto", "repair-btc")
	require.NoError(t, err)
	result, err := domain.ParseResampleTaskResult(stored.Result)
	require.NoError(t, err)
	assert.Equal(t, domain.ResampleTaskStateIdle, result.State)
	assert.Empty(t, result.ActiveOrigin)
	assert.Nil(t, result.ActiveBucket)
}

func TestResampleRepairSkipAdvancesCursorWithoutFailure(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "rule-1", DataType: "kline_resample", Enabled: true, PrepareState: domain.PrepareStateReady}))
	bucket := time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC)
	initial := domain.NewResampleTaskResult(bucket)
	raw, err := initial.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{SpaceID: "crypto", InstanceID: "repair-btc", Provider: "moox", DataType: "kline_resample", SubjectID: "BTC-USDT", Frequency: "4h", Result: raw}}))
	claim, claimed, err := s.TaskInstances().ClaimResampleTask(ctx, "crypto", "repair-btc", 0, domain.ResampleOriginRepair, bucket, bucket, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	next := bucket.Add(4 * time.Hour)
	updated, err := s.TaskInstances().SkipResampleRepairTask(ctx, "crypto", "repair-btc", claim.Result.StateVersion, bucket, next, "source retention expired")
	require.NoError(t, err)
	require.True(t, updated)
	stored, err := s.TaskInstances().Get(ctx, "crypto", "repair-btc")
	require.NoError(t, err)
	result, err := domain.ParseResampleTaskResult(stored.Result)
	require.NoError(t, err)
	assert.Equal(t, domain.ResampleTaskStateIdle, result.State)
	assert.Equal(t, next, *result.RepairNextBucket)
	assert.NotEqual(t, domain.ResampleTaskStateFailed, result.State)
}

func TestResampleBackfillStartIsIdempotentAndConflicts(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	initialResult := domain.NewResampleTaskResult(time.Time{})
	initial, err := initialResult.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "rule-1", TaskName: "rule-1", DataType: "kline_resample", Enabled: true, PrepareState: domain.PrepareStateReady}))
	for _, taskID := range []string{"btc", "eth"} {
		require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
			SpaceID: "crypto", InstanceID: taskID, Provider: "moox", DataType: "kline_resample", SubjectID: taskID, Frequency: "4h", Result: initial,
		}}))
		attachTestWriteTarget(t, s, ctx, "crypto", taskID, "rule-1", "derived")
	}
	request := domain.ResampleBackfillRequest{
		RequestID: "bf-1", Start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
	}
	updated, err := s.TaskInstances().StartResampleBackfill(ctx, "crypto", "rule-1", request)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated)
	updated, err = s.TaskInstances().StartResampleBackfill(ctx, "crypto", "rule-1", request)
	require.NoError(t, err)
	assert.Zero(t, updated)
	request.RequestID = "bf-2"
	_, err = s.TaskInstances().StartResampleBackfill(ctx, "crypto", "rule-1", request)
	require.ErrorIs(t, err, ErrResampleBackfillConflict)
}

func TestResampleUpsertResetsCursorWhenSubjectIsReactivated(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	oldResultValue := domain.NewResampleTaskResult(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	oldResult, err := oldResultValue.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "btc", Provider: "moox", MarketType: "spot", DataType: "kline_resample", SubjectID: "BTC", Frequency: "4h", Result: oldResult,
	}}))
	require.NoError(t, s.TaskInstances().db.Exec("UPDATE t_collector_task_instances SET c_is_deleted = 1 WHERE c_space_id = ? AND c_instance_id = ?", "crypto", "btc").Error)
	newResultValue := domain.NewResampleTaskResult(time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC))
	newResult, err := newResultValue.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "btc", Provider: "moox", MarketType: "spot", DataType: "kline_resample", SubjectID: "BTC", Frequency: "4h", Result: newResult,
	}}))
	current, err := s.TaskInstances().Get(ctx, "crypto", "btc")
	require.NoError(t, err)
	require.Equal(t, newResult, current.Result)
}
