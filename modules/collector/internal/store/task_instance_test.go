package store

import (
	"context"
	"testing"

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
