package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCollectorStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

func TestCollectionTaskRepository_CRUD(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.Tasks()
	ctx := context.Background()
	rule := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "rule-1", DataType: "instrument", Provider: "binance", MarketType: "spot",
		CollectParams: `{"source":{"kind":"none"}}`, Enabled: true,
	}
	require.NoError(t, repo.Create(ctx, rule))

	got, err := repo.GetByTaskID(ctx, "crypto", "rule-1")
	require.NoError(t, err)
	assert.Equal(t, "rule-1", got.TaskID)

	rules, total, err := repo.List(ctx, TaskFilter{SpaceID: "crypto", Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, rules, 1)

	updated, err := repo.UpdateByTaskID(ctx, "crypto", "rule-1", domain.CollectionTask{
		SpaceID: "crypto", TaskID: "rule-1", DataType: "instrument", Provider: "binance", MarketType: "spot",
		CollectParams: `{"source":{"kind":"none"}}`, Creator: "updated", Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", updated.Creator)

	require.NoError(t, repo.SetEnabled(ctx, "crypto", "rule-1", false))
	enabled, err := repo.ListEnabled(ctx, "crypto")
	require.NoError(t, err)
	assert.Len(t, enabled, 0)
}

func TestCollectionTaskCoverageStartHonorsEnabledAndStockCalendar(t *testing.T) {
	t.Setenv("MOOX_STOCK_CN_CALENDAR_PATH", filepath.Join("..", "..", "config", "markets", "stockcn", "calendar.yaml"))
	now := time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC) // Sunday in Asia/Shanghai.
	lookback := domain.CollectionTask{
		SpaceID:       "stockcn",
		DataType:      "kline",
		Provider:      "stockcn_multi",
		MarketType:    "equity",
		CollectParams: `{"history_policy":{"mode":"lookback","lookback":2}}`,
	}
	start, err := resolveCollectionTaskCoverageStart(&lookback, now)
	require.NoError(t, err)
	require.NotNil(t, start)
	assert.Equal(t, time.Date(2026, 8, 27, 1, 30, 0, 0, time.UTC), *start)

	disabled := lookback
	disabled.Enabled = false
	require.NoError(t, applyCollectionTaskCoverageStart(&disabled, now, disabled.Enabled))
	assert.Nil(t, disabled.CoverageStartTime)

	repo := newCollectorStore(t).Tasks()
	disabled.TaskID = "disabled-stock-rule"
	require.NoError(t, repo.Create(context.Background(), disabled))
	stored, err := repo.GetByTaskID(context.Background(), "stockcn", disabled.TaskID)
	require.NoError(t, err)
	assert.Nil(t, stored.CoverageStartTime)
}

func TestCollectionTaskReenableRecomputesCoverageStart(t *testing.T) {
	repo := newCollectorStore(t).Tasks()
	ctx := context.Background()
	rule := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "re-enable", DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: `{"history_policy":{"mode":"live_only"}}`, Enabled: true,
	}
	require.NoError(t, repo.Create(ctx, rule))
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	rule.CoverageStartTime = &old
	_, err := repo.UpdateByTaskID(ctx, "crypto", rule.TaskID, rule)
	require.NoError(t, err)
	require.NoError(t, repo.SetEnabled(ctx, "crypto", rule.TaskID, false))
	require.NoError(t, repo.SetEnabled(ctx, "crypto", rule.TaskID, true))
	stored, err := repo.GetByTaskID(ctx, "crypto", rule.TaskID)
	require.NoError(t, err)
	require.NotNil(t, stored.CoverageStartTime)
	assert.True(t, stored.CoverageStartTime.After(old))
}

func TestCollectorRuleSchemaOmitsNodeAssignmentColumns(t *testing.T) {
	s := newCollectorStore(t)
	rows, err := s.db.Raw("PRAGMA table_info(t_collector_tasks)").Rows()
	require.NoError(t, err)
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		columns[name] = true
	}
	for _, forbidden := range []string{
		"c_" + "assignment" + "_type",
		"c_" + "assigned" + "_nodes",
		"c_" + "node" + "_pattern",
		"c_" + "node" + "_tags",
	} {
		assert.False(t, columns[forbidden], "schema still contains %s", forbidden)
	}
}

func TestCollectorRuleSchemaAddsPreparationStateWithoutConfigHash(t *testing.T) {
	s := newCollectorStore(t)
	rows, err := s.db.Raw("PRAGMA table_info(t_collector_tasks)").Rows()
	require.NoError(t, err)
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		columns[name] = true
	}
	assert.True(t, columns["c_prepare_state"])
	assert.True(t, columns["c_last_error"])
	assert.False(t, columns["c_config_hash"])
}

func TestCollectionTaskRepositoryUpdatesPrepareStateWithoutChangingDefinition(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.Tasks()
	ctx := context.Background()
	rule := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "resample-1", DataType: "kline_resample", Provider: "moox", MarketType: "spot",
		CollectParams: `{"target_dataset_id":"derived"}`, Enabled: true, PrepareState: domain.PrepareStatePending,
	}
	require.NoError(t, repo.Create(ctx, rule))
	require.NoError(t, repo.SetPrepareState(ctx, "crypto", "resample-1", domain.PrepareStateWaitingView, "view pending"))

	got, err := repo.GetByTaskID(ctx, "crypto", "resample-1")
	require.NoError(t, err)
	assert.Equal(t, domain.PrepareStateWaitingView, got.PrepareState)
	assert.Equal(t, "view pending", got.LastError)
	assert.Equal(t, rule.CollectParams, got.CollectParams)

	rows, err := repo.ListResampleByPrepareStates(ctx, []domain.CollectionTaskPrepareState{domain.PrepareStateWaitingView}, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "resample-1", rows[0].TaskID)
}

func TestCollectionTaskRepository_ListEnabledAllRejectsPartialSnapshot(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.Tasks()
	ctx := context.Background()
	for index := 0; index < 3; index++ {
		require.NoError(t, repo.Create(ctx, domain.CollectionTask{
			SpaceID: "crypto", TaskID: fmt.Sprintf("rule-%d", index), Enabled: true,
		}))
	}
	rows, err := repo.ListEnabledAll(ctx, 3)
	require.NoError(t, err)
	require.Len(t, rows, 3)

	rows, err = repo.ListEnabledAll(ctx, 2)
	require.ErrorContains(t, err, "exceeds limit")
	require.Nil(t, rows)
	_, err = repo.ListEnabledAll(ctx, MaxEnabledTasks+1)
	require.Error(t, err)
}
