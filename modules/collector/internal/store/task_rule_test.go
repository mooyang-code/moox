package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

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

func TestTaskRuleRepository_CRUD(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.TaskRules()
	ctx := context.Background()
	rule := domain.TaskRule{
		SpaceID: "crypto", RuleID: "rule-1", DataType: "symbol", Provider: "binance", MarketType: "spot",
		CollectParams: `{"source":{"kind":"none"}}`, Enabled: true,
	}
	require.NoError(t, repo.Create(ctx, rule))

	got, err := repo.GetByRuleID(ctx, "crypto", "rule-1")
	require.NoError(t, err)
	assert.Equal(t, "rule-1", got.RuleID)

	rules, total, err := repo.List(ctx, TaskRuleFilter{SpaceID: "crypto", Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, rules, 1)

	updated, err := repo.UpdateByRuleID(ctx, "crypto", "rule-1", domain.TaskRule{
		SpaceID: "crypto", RuleID: "rule-1", DataType: "symbol", Provider: "binance", MarketType: "spot",
		CollectParams: `{"source":{"kind":"none"}}`, Creator: "updated", Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", updated.Creator)

	require.NoError(t, repo.SetEnabled(ctx, "crypto", "rule-1", false))
	enabled, err := repo.ListEnabled(ctx, "crypto")
	require.NoError(t, err)
	assert.Len(t, enabled, 0)
}

func TestCollectorRuleSchemaOmitsNodeAssignmentColumns(t *testing.T) {
	s := newCollectorStore(t)
	rows, err := s.db.Raw("PRAGMA table_info(t_collector_task_rules)").Rows()
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
	rows, err := s.db.Raw("PRAGMA table_info(t_collector_task_rules)").Rows()
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

func TestTaskRuleRepositoryUpdatesPrepareStateWithoutChangingDefinition(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.TaskRules()
	ctx := context.Background()
	rule := domain.TaskRule{
		SpaceID: "crypto", RuleID: "resample-1", DataType: "kline_resample", Provider: "moox", MarketType: "spot",
		CollectParams: `{"target_dataset_id":"derived"}`, Enabled: true, PrepareState: domain.PrepareStatePending,
	}
	require.NoError(t, repo.Create(ctx, rule))
	require.NoError(t, repo.SetPrepareState(ctx, "crypto", "resample-1", domain.PrepareStateWaitingView, "view pending"))

	got, err := repo.GetByRuleID(ctx, "crypto", "resample-1")
	require.NoError(t, err)
	assert.Equal(t, domain.PrepareStateWaitingView, got.PrepareState)
	assert.Equal(t, "view pending", got.LastError)
	assert.Equal(t, rule.CollectParams, got.CollectParams)

	rows, err := repo.ListResampleByPrepareStates(ctx, []domain.TaskRulePrepareState{domain.PrepareStateWaitingView}, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "resample-1", rows[0].RuleID)
}

func TestTaskRuleRepository_ListEnabledAllRejectsPartialSnapshot(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.TaskRules()
	ctx := context.Background()
	for index := 0; index < 3; index++ {
		require.NoError(t, repo.Create(ctx, domain.TaskRule{
			SpaceID: "crypto", RuleID: fmt.Sprintf("rule-%d", index), Enabled: true,
		}))
	}
	rows, err := repo.ListEnabledAll(ctx, 3)
	require.NoError(t, err)
	require.Len(t, rows, 3)

	rows, err = repo.ListEnabledAll(ctx, 2)
	require.ErrorContains(t, err, "exceeds limit")
	require.Nil(t, rows)
	_, err = repo.ListEnabledAll(ctx, MaxEnabledTaskRules+1)
	require.Error(t, err)
}
