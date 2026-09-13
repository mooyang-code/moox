package inputcache

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDeleteKeysIsExactAtomicAndTracksMutation(t *testing.T) {
	ctx := context.Background()
	db, err := CreateDatabase(ctx, filepath.Join(t.TempDir(), "view.duckdb"),
		[]Column{{"subject", "VARCHAR"}, {"venue", "VARCHAR"}, {"at", "TIMESTAMP_NS"}, {"close", "DOUBLE"}},
		[]string{"subject", "venue", "at"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	at := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, db.Upsert(ctx, [][]any{{"BTC", "A", at, 1.0}, {"BTC", "B", at, 2.0}, {"ETH", "A", at, 3.0}}, at))
	query := WindowQuery{TimeColumn: "at", Through: at, Lookback: 1}
	before := db.mutations.Load()
	for _, keys := range [][][]any{
		{{"BTC", "A", at}, {"ETH", "A"}},
		{{"BTC", "A", at}, {"ETH", "A", nil}},
		{{"BTC", "A", at}, {sql.NullString{Valid: false}, "A", at}},
		{{"BTC", "A", at}, {(*string)(nil), "A", at}},
		{{"BTC", "A", at}, {[]byte(nil), "A", at}},
		{{"BTC", "A", at}, {"ETH", "A", "not-a-timestamp"}},
	} {
		require.Error(t, db.DeleteKeys(ctx, keys))
		rows, err := db.ReadWindow(ctx, query)
		require.NoError(t, err)
		require.Len(t, rows, 3)
		require.Equal(t, before, db.mutations.Load())
	}
	require.NoError(t, db.DeleteKeys(ctx, nil))
	require.Equal(t, before, db.mutations.Load())
	require.NoError(t, db.DeleteKeys(ctx, [][]any{{"BTC", "A", at}, {"BTC' OR 1=1 --", "A", at}}))
	require.Equal(t, before+1, db.mutations.Load())
	rows, err := db.ReadWindow(ctx, query)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "B", rows[0][1])
	require.Equal(t, "ETH", rows[1][0])
}

func TestDeleteKeysPreservesUnsignedDriverValues(t *testing.T) {
	ctx := context.Background()
	db, err := CreateDatabase(ctx, filepath.Join(t.TempDir(), "view.duckdb"), []Column{{"id", "UBIGINT"}}, []string{"id"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	id := uint64(123)
	require.NoError(t, db.Upsert(ctx, [][]any{{id}}, time.Now()))
	require.NoError(t, db.DeleteKeys(ctx, [][]any{{&id}}))
	var count int
	require.NoError(t, db.db.QueryRowContext(ctx, "SELECT count(*) FROM cached_rows").Scan(&count))
	require.Zero(t, count)
}
