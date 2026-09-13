package inputcache

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadWindowPartitionsByEverySourceKeyDimension(t *testing.T) {
	ctx := context.Background()
	db, err := CreateDatabase(ctx, filepath.Join(t.TempDir(), "view.duckdb"),
		[]Column{{"subject", "VARCHAR"}, {"venue", "VARCHAR"}, {"at", "TIMESTAMP_NS"}, {"close", "DOUBLE"}},
		[]string{"subject", "venue", "at"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	at := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, db.Upsert(ctx, [][]any{
		{"BTC", "A", at.Add(-time.Minute), 1.0},
		{"BTC", "A", at, 2.0},
		{"BTC", "A", at.Add(time.Minute), 99.0},
		{"BTC", "B", at, 3.0},
		{"ETH", "A", at, 4.0},
	}, at))
	query := WindowQuery{TimeColumn: "at", Through: at, Lookback: 1, Filters: map[string][]any{"subject": {"BTC"}}}
	rows, err := db.ReadWindow(ctx, query)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, 2.0, rows[0][3])
	require.Equal(t, 3.0, rows[1][3])
	query.Filters["subject"] = []any{"BTC' OR 1=1 --"}
	rows, err = db.ReadWindow(ctx, query)
	require.NoError(t, err)
	require.Empty(t, rows)
	query.Filters["missing"] = []any{"x"}
	_, err = db.ReadWindow(ctx, query)
	require.ErrorContains(t, err, "unknown")
}
