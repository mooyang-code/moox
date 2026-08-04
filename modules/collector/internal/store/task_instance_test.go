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
