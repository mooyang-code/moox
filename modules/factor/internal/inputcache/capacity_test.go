package inputcache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCapacityPausesWhenRebuiltDatabaseStillExceedsBudget(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.MaxBytes = 1
	cfg.RebuildKeepRows = 1
	g, err := NewGeneration(ctx, cfg.Dir, []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, g.Close()) })
	require.NoError(t, g.Use(func(db *Database, _ uint64) error {
		return db.Upsert(ctx, [][]any{{"BTC"}, {"ETH"}}, time.Now())
	}))
	result, err := maintainCapacity(ctx, cfg, []*Generation{g}, func(string) (uint64, error) { return 1 << 60, nil })
	require.NoError(t, err)
	require.Equal(t, 1, result.Rebuilt)
	require.True(t, result.PauseWrites)
	require.NoError(t, g.Use(func(db *Database, _ uint64) error {
		var count int
		err := db.db.QueryRowContext(ctx, "SELECT count(*) FROM cached_rows").Scan(&count)
		require.Equal(t, 1, count)
		return err
	}))
	result, err = maintainCapacity(ctx, cfg, []*Generation{g}, func(string) (uint64, error) { return 1 << 60, nil })
	require.NoError(t, err)
	require.Zero(t, result.Rebuilt)
	require.True(t, result.PauseWrites)
	require.NoError(t, g.Use(func(db *Database, _ uint64) error {
		return db.Upsert(ctx, [][]any{{"SOL"}}, time.Now())
	}))
	result, err = maintainCapacity(ctx, cfg, []*Generation{g}, func(string) (uint64, error) { return 0, nil })
	require.ErrorContains(t, err, "headroom")
	require.Zero(t, result.Rebuilt)
	require.True(t, result.PauseWrites)
}
