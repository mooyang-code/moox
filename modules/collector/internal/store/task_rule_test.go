package store

import (
	"context"
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
		SpaceID: "crypto", RuleID: "rule-1", DataType: "symbol", Exchange: "binance",
		CollectParams: `{"source":{"kind":"none"}}`, AssignmentType: "auto",
		AssignedNodes: "[]", NodeTags: "[]", Enabled: true,
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
		SpaceID: "crypto", RuleID: "rule-1", DataType: "symbol", Exchange: "binance",
		CollectParams: `{"source":{"kind":"none"}}`, AssignmentType: "manual", Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "manual", updated.AssignmentType)

	require.NoError(t, repo.SetEnabled(ctx, "crypto", "rule-1", false))
	enabled, err := repo.ListEnabled(ctx, "crypto")
	require.NoError(t, err)
	assert.Len(t, enabled, 0)
}
