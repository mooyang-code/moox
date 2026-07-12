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
		Interval: "1h", TaskParams: `{}`,
	}
	require.NoError(t, repo.UpsertMany(ctx, []domain.TaskInstance{instance}))

	instances, total, err := repo.List(ctx, TaskInstanceFilter{
		SpaceID: "crypto", Exchange: "binance", Symbol: "BTC", Page: 1, PageSize: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, instances, 1)
	assert.Equal(t, "task-1", instances[0].TaskID)

	require.NoError(t, repo.UpdateCloudJobItemIDs(ctx, "crypto", map[string]string{
		"task-1": "job-1",
		"":       "skip",
		"task-2": "",
	}))
	require.NoError(t, repo.UpdateStatus(ctx, "crypto", "task-1", "node-a", 2, `{"ok":true}`))

	status := 2
	instances, total, err = repo.List(ctx, TaskInstanceFilter{
		SpaceID: "crypto", LastExecNode: "node-a", LastExecStatus: &status,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)

	err = repo.UpdateStatus(ctx, "crypto", "missing", "node-a", 2, `{}`)
	assert.ErrorIs(t, err, ErrTaskInstanceNotFound)
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
