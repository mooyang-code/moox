package inputcache

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRebuildKeepsRecentlyModifiedRowsAndLeavesSourceIntact(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source, err := CreateDatabase(ctx, filepath.Join(dir, "old.duckdb"), []Column{{"id", "VARCHAR"}, {"close", "DOUBLE"}}, []string{"id"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, source.Close()) })
	at := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, source.Upsert(ctx, [][]any{{"BTC", 1.0}, {"ETH", 2.0}, {"SOL", 3.0}}, at))
	require.NoError(t, source.Upsert(ctx, [][]any{{"SOL", 4.0}}, at.Add(time.Minute)))
	next, err := source.Rebuild(ctx, filepath.Join(dir, "new.duckdb"), 1)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, next.Close()) })
	var id string
	var value float64
	var updated time.Time
	require.NoError(t, next.db.QueryRowContext(ctx, "SELECT id, close, __moox_cache_updated_at FROM cached_rows").Scan(&id, &value, &updated))
	require.Equal(t, "SOL", id)
	require.Equal(t, 4.0, value)
	require.True(t, at.Add(time.Minute).Equal(updated))
	var count int
	require.NoError(t, source.db.QueryRowContext(ctx, "SELECT count(*) FROM cached_rows").Scan(&count))
	require.Equal(t, 3, count)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = source.Rebuild(cancelled, filepath.Join(dir, "cancelled.duckdb"), 1)
	require.Error(t, err)
	_, err = source.Rebuild(ctx, filepath.Join(dir, "invalid.duckdb"), 0)
	require.Error(t, err)
}
