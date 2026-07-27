package store

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskInstanceRepository_UpsertListAndStatus(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.TaskInstances()
	ctx := context.Background()

	instance := domain.TaskInstance{
		SpaceID: "crypto", TaskID: "task-1", RuleID: "rule-1",
		Exchange: "binance", Market: "spot", DataType: "symbol",
		DatasetID: "ds-1", SubjectID: "BTC-USDT", Symbol: "BTCUSDT",
		Interval: "1h", TaskParams: `{}`, CloudJobItemID: "item-current",
	}
	require.NoError(t, repo.UpsertMany(ctx, []domain.TaskInstance{instance}))

	instances, total, err := repo.List(ctx, TaskInstanceFilter{
		SpaceID: "crypto", Exchange: "binance", Symbol: "BTC", Page: 1, PageSize: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, instances, 1)
	assert.Equal(t, "task-1", instances[0].TaskID)

	updated, err := repo.UpdateStatus(ctx, "crypto", "task-1", "item-current", "node-a", 2, `{"ok":true}`)
	require.NoError(t, err)
	assert.True(t, updated)

	status := 2
	instances, total, err = repo.List(ctx, TaskInstanceFilter{
		SpaceID: "crypto", LastExecNode: "node-a", LastExecStatus: &status,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)

	updated, err = repo.UpdateStatus(ctx, "crypto", "missing", "item-missing", "node-a", 2, `{}`)
	require.NoError(t, err)
	assert.False(t, updated)
}

func TestTaskInstanceRepository_UpdateStatusMatchesCurrentJobItemID(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.TaskInstances()
	ctx := context.Background()
	instance := domain.TaskInstance{
		SpaceID: "crypto", TaskID: "task-1", CloudJobItemID: "item-new", RuleID: "rule-1",
		Exchange: "binance", Market: "spot", DataType: "symbol", TaskParams: `{}`,
		LastExecNode: "node-original", LastExecStatus: domain.InstanceStatusPending, Result: `{"state":"original"}`,
	}
	require.NoError(t, repo.UpsertMany(ctx, []domain.TaskInstance{instance}))

	updated, err := repo.UpdateStatus(
		ctx, "crypto", "task-1", "item-new", "node-new", domain.InstanceStatusSuccess, `{"state":"new"}`,
	)
	require.NoError(t, err)
	assert.True(t, updated)

	instances, _, err := repo.List(ctx, TaskInstanceFilter{SpaceID: "crypto", TaskID: "task-1"})
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "node-new", instances[0].LastExecNode)
	assert.Equal(t, domain.InstanceStatusSuccess, instances[0].LastExecStatus)
	assert.JSONEq(t, `{"state":"new"}`, instances[0].Result)
}

func TestTaskInstanceRepository_UpdateStatusIgnoresStaleJobItemID(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.TaskInstances()
	ctx := context.Background()
	instance := domain.TaskInstance{
		SpaceID: "crypto", TaskID: "task-1", CloudJobItemID: "item-new", RuleID: "rule-1",
		Exchange: "binance", Market: "spot", DataType: "symbol", TaskParams: `{}`,
		LastExecNode: "node-current", LastExecStatus: domain.InstanceStatusPending, Result: `{"state":"current"}`,
	}
	require.NoError(t, repo.UpsertMany(ctx, []domain.TaskInstance{instance}))

	updated, err := repo.UpdateStatus(
		ctx, "crypto", "task-1", "item-old", "node-old", domain.InstanceStatusFailed, `{"state":"old"}`,
	)
	require.NoError(t, err)
	assert.False(t, updated)

	instances, _, err := repo.List(ctx, TaskInstanceFilter{SpaceID: "crypto", TaskID: "task-1"})
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "node-current", instances[0].LastExecNode)
	assert.Equal(t, domain.InstanceStatusPending, instances[0].LastExecStatus)
	assert.JSONEq(t, `{"state":"current"}`, instances[0].Result)
}

func TestTaskInstanceRepository_UpsertManyUpdatesCloudJobItemID(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.TaskInstances()
	ctx := context.Background()

	base := domain.TaskInstance{
		SpaceID: "crypto", TaskID: "task-1", RuleID: "rule-1",
		Exchange: "binance", Market: "spot", DataType: "kline",
		TaskParams: `{}`, CloudJobItemID: "task-1:2026-07-26T10:30:00Z",
	}
	require.NoError(t, repo.UpsertMany(ctx, []domain.TaskInstance{base}))
	updated, err := repo.UpdateStatus(
		ctx,
		"crypto",
		"task-1",
		base.CloudJobItemID,
		"node-old",
		domain.InstanceStatusSuccess,
		`{"state":"old"}`,
	)
	require.NoError(t, err)
	require.True(t, updated)

	base.CloudJobItemID = "task-1:2026-07-26T11:00:00Z"
	require.NoError(t, repo.UpsertMany(ctx, []domain.TaskInstance{base}))

	instances, _, err := repo.List(ctx, TaskInstanceFilter{SpaceID: "crypto", TaskID: "task-1"})
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "task-1:2026-07-26T11:00:00Z", instances[0].CloudJobItemID)
	assert.Equal(t, domain.InstanceStatusPending, instances[0].LastExecStatus)
	assert.Empty(t, instances[0].LastExecNode)
	assert.Nil(t, instances[0].LastExecTime)
	assert.JSONEq(t, `{}`, instances[0].Result)

	updated, err = repo.UpdateStatus(
		ctx,
		"crypto",
		"task-1",
		base.CloudJobItemID,
		"node-current",
		domain.InstanceStatusSuccess,
		`{"state":"current"}`,
	)
	require.NoError(t, err)
	require.True(t, updated)
	require.NoError(t, repo.UpsertMany(ctx, []domain.TaskInstance{base}))
	instances, _, err = repo.List(ctx, TaskInstanceFilter{SpaceID: "crypto", TaskID: "task-1"})
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, domain.InstanceStatusSuccess, instances[0].LastExecStatus)
	assert.Equal(t, "node-current", instances[0].LastExecNode)
	assert.NotNil(t, instances[0].LastExecTime)
	assert.JSONEq(t, `{"state":"current"}`, instances[0].Result)
}

func TestTaskInstanceRepository_NormalizeHelpers(t *testing.T) {
	page, size := normalizePage(0, 0)
	assert.Equal(t, 1, page)
	assert.Equal(t, defaultPageSize, size)
	page, size = normalizePage(2, 5000)
	assert.Equal(t, 2, page)
	assert.Equal(t, maxPageSize, size)
	assert.Equal(t, "{}", normalizeJSON(""))
	assert.Equal(t, `{"k":1}`, normalizeJSON(`{"k":1}`))
}
