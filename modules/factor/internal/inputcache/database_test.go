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

func TestDatabaseSupportsViewJSONAndRejectsAmbiguousKeyCase(t *testing.T) {
	ctx := context.Background()
	db, err := CreateDatabase(ctx, filepath.Join(t.TempDir(), "json.duckdb"), []Column{{"id", "VARCHAR"}, {"payload", "JSON"}}, []string{"id"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Upsert(ctx, [][]any{{"BTC", `{"venue":"A"}`}}, time.Now()))
	_, err = CreateDatabase(ctx, filepath.Join(t.TempDir(), "case.duckdb"), []Column{{"At", "TIMESTAMP_NS"}}, []string{"at"})
	require.ErrorContains(t, err, "exact column name")
}

func TestDatabaseUnsignedValuesSurviveEveryCachePath(t *testing.T) {
	ctx := context.Background()
	db, err := CreateDatabase(ctx, filepath.Join(t.TempDir(), "unsigned.duckdb"),
		[]Column{{"id", "UBIGINT"}, {"at", "TIMESTAMP_NS"}, {"value", "UBIGINT"}}, []string{"id", "at"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	at := time.Now().UTC().Truncate(time.Microsecond)
	max := ^uint64(0)
	require.NoError(t, db.Upsert(ctx, [][]any{{max, at, max}, {uint64(0), at, uint64(1)}}, at))
	query := WindowQuery{TimeColumn: "at", Through: at, Lookback: 1, Filters: map[string][]any{"id": {max}}}
	rows, err := db.ReadWindow(ctx, query)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, max, rows[0][0])
	require.Equal(t, max, rows[0][2])
	next, err := db.Rebuild(ctx, filepath.Join(t.TempDir(), "next.duckdb"), 2)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, next.Close()) })
	rows, err = next.ReadWindow(ctx, query)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, max, rows[0][2])
	require.NoError(t, next.DeleteKeys(ctx, [][]any{{max, at}}))
	rows, err = next.ReadWindow(ctx, query)
	require.NoError(t, err)
	require.Empty(t, rows)
	query.Filters["id"] = []any{uint64(0)}
	rows, err = next.ReadWindow(ctx, query)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestDatabaseJSONReadAndRebuildPreserveNumberText(t *testing.T) {
	ctx := context.Background()
	db, err := CreateDatabase(ctx, filepath.Join(t.TempDir(), "json-window.duckdb"), []Column{{"id", "VARCHAR"}, {"at", "TIMESTAMP_NS"}, {"payload", "JSON"}}, []string{"id", "at"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	at := time.Now().UTC().Truncate(time.Microsecond)
	const payload = `{"large":18446744073709551615,"decimal":0.12345678901234567890123456789}`
	require.NoError(t, db.Upsert(ctx, [][]any{{"BTC", at, payload}, {"ETH", at, nil}, {"SOL", at, "null"}}, at))
	query := WindowQuery{TimeColumn: "at", Through: at, Lookback: 1}
	check := func(database *Database) {
		rows, err := database.ReadWindow(ctx, query)
		require.NoError(t, err)
		require.Len(t, rows, 3)
		require.Equal(t, payload, rows[0][2])
		require.Nil(t, rows[1][2])
		require.Equal(t, "null", rows[2][2])
	}
	check(db)
	next, err := db.Rebuild(ctx, filepath.Join(t.TempDir(), "next.duckdb"), 3)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, next.Close()) })
	check(next)
}
