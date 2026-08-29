package store

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskInstanceRepositoryUpsertKeepsFreshnessForStableTask(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	instance := domain.TaskInstance{
		SpaceID: "crypto", TaskID: "task-1", RuleID: "rule-1", Provider: "binance", MarketType: "spot",
		DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`,
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

func TestTaskInstanceRepositoryUpsertSkipsUnchangedDefinition(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	instance := domain.TaskInstance{
		SpaceID: "crypto", TaskID: "task-1", RuleID: "rule-1", Provider: "binance", MarketType: "spot",
		DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`,
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

func TestTaskInstanceRepositoryTracksSCFAssignmentAndStorageWrite(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	instance := domain.TaskInstance{SpaceID: "crypto", TaskID: "task-1", RuleID: "rule-1", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	require.NoError(t, s.TaskInstances().AssignMarketFetchFunction(ctx, "crypto", "binance", "spot", "bars", "1m", "market-fetch-1", []string{"BTC-USDT"}))
	at := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	updated, err := s.TaskInstances().MarkStorageWrites(ctx, []StorageWriteObservation{{SpaceID: "crypto", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", FunctionName: "market-fetch-1", At: at}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), updated)
	stored, err := s.TaskInstances().Get(ctx, "crypto", "task-1")
	require.NoError(t, err)
	assert.Equal(t, "market-fetch-1", stored.FunctionName)
	assert.Equal(t, domain.InstanceStatusSuccess, stored.LastExecStatus)
	assert.Equal(t, at, stored.LastExecTime.UTC())

	updated, err = s.TaskInstances().MarkStorageWrites(ctx, []StorageWriteObservation{{SpaceID: "crypto", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", FunctionName: "old-function", At: at.Add(time.Minute)}})
	require.NoError(t, err)
	assert.Zero(t, updated)
}

func TestTaskInstanceRepositoryMatchesCanonicalStorageFrequency(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	instance := domain.TaskInstance{SpaceID: "crypto", TaskID: "task-hour", RuleID: "rule-1", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1h", TaskParams: `{}`}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	require.NoError(t, s.TaskInstances().AssignMarketFetchFunction(ctx, "crypto", "binance", "spot", "bars", "1h", "market-fetch-hour", []string{"BTC-USDT"}))
	at := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	updated, err := s.TaskInstances().MarkStorageWrites(ctx, []StorageWriteObservation{{SpaceID: "crypto", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1H", FunctionName: "market-fetch-hour", At: at}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), updated)
}

func TestTaskInstanceRepositoryDeactivatesMissingRuleInstances(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	instances := []domain.TaskInstance{
		{SpaceID: "crypto", TaskID: "keep", RuleID: "rule-1", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`},
		{SpaceID: "crypto", TaskID: "remove", RuleID: "rule-1", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", SubjectID: "ETH-USDT", Frequency: "1m", TaskParams: `{}`},
	}
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, instances))
	require.NoError(t, s.TaskInstances().DeactivateMissingMarketFetchRuleInstances(ctx, "crypto", "rule-1", []string{"keep"}))

	removed, err := s.TaskInstances().Get(ctx, "crypto", "remove")
	require.NoError(t, err)
	assert.True(t, removed.IsDeleted)
	kept, err := s.TaskInstances().Get(ctx, "crypto", "keep")
	require.NoError(t, err)
	assert.False(t, kept.IsDeleted)
}

func TestResampleTaskClaimCompleteUsesStateVersionCAS(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	require.NoError(t, s.TaskRules().Create(ctx, domain.TaskRule{SpaceID: "crypto", RuleID: "rule-1", DataType: "kline_resample", Enabled: true, PrepareState: domain.PrepareStateReady}))
	next := time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC)
	initial := domain.NewResampleTaskResult(next)
	raw, err := initial.Marshal()
	require.NoError(t, err)
	instance := domain.TaskInstance{
		SpaceID: "crypto", TaskID: "resample-btc", RuleID: "rule-1", Provider: "moox", MarketType: "spot",
		DataType: "kline_resample", DatasetID: "derived", SubjectID: "BTC-USDT", Frequency: "4H", TaskParams: `{}`, Result: raw,
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
}

func TestResampleTaskWaitAndRecoverExpiredLease(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	require.NoError(t, s.TaskRules().Create(ctx, domain.TaskRule{SpaceID: "crypto", RuleID: "rule-1", DataType: "kline_resample", Enabled: true, PrepareState: domain.PrepareStateReady}))
	bucket := time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC)
	initial := domain.NewResampleTaskResult(bucket)
	raw, err := initial.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", TaskID: "resample-btc", RuleID: "rule-1", Provider: "moox", DataType: "kline_resample", DatasetID: "derived", SubjectID: "BTC-USDT", Frequency: "4H", Result: raw,
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

func TestResampleBackfillStartIsIdempotentAndConflicts(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	initialResult := domain.NewResampleTaskResult(time.Time{})
	initial, err := initialResult.Marshal()
	require.NoError(t, err)
	for _, taskID := range []string{"btc", "eth"} {
		require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
			SpaceID: "crypto", TaskID: taskID, RuleID: "rule-1", Provider: "moox", DataType: "kline_resample", DatasetID: "derived", SubjectID: taskID, Frequency: "4H", Result: initial,
		}}))
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
