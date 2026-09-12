package inputcache

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDatabaseStoresCompleteRowsAndRefreshesLocalTime(t *testing.T) {
	ctx := context.Background()
	db, err := CreateDatabase(ctx, filepath.Join(t.TempDir(), "view.duckdb"),
		[]Column{{"subject_id", "VARCHAR"}, {"close", "DOUBLE"}, {"extra", "VARCHAR"}}, []string{"subject_id"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	at := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, db.Upsert(ctx, [][]any{{"BTC", 1.0, nil}, {"ETH", 2.0, "extra"}}, at))
	require.NoError(t, db.Upsert(ctx, [][]any{{"BTC", 3.0, "new"}}, at.Add(time.Minute)))
	var count int
	require.NoError(t, db.db.QueryRowContext(ctx, "SELECT count(*) FROM cached_rows").Scan(&count))
	require.Equal(t, 2, count)
	var value float64
	var extra string
	var updated time.Time
	require.NoError(t, db.db.QueryRowContext(ctx, "SELECT close, extra, __moox_cache_updated_at FROM cached_rows WHERE subject_id = ?", "BTC").Scan(&value, &extra, &updated))
	require.Equal(t, 3.0, value)
	require.Equal(t, "new", extra)
	require.True(t, updated.Equal(at.Add(time.Minute)))
	require.Error(t, db.Upsert(ctx, [][]any{{"BTC", 4.0}}, at))
}

func TestDatabaseRejectsInvalidSchema(t *testing.T) {
	for _, columns := range [][]Column{
		{{"a", "VARCHAR"}, {"A", "DOUBLE"}},
		{{updatedColumn, "TIMESTAMP_NS"}},
		{{"a", "VARCHAR); DROP TABLE x; --"}},
	} {
		_, err := CreateDatabase(context.Background(), filepath.Join(t.TempDir(), "bad.duckdb"), columns, []string{"a"})
		require.Error(t, err)
	}
}
