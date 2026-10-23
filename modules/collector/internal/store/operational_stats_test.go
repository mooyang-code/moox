package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func TestOperationalStatsEmptyAndWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.db")
	s, err := Open(&Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	stats, err := s.OperationalStats(context.Background(), "crypto", 10)
	require.NoError(t, err)
	require.Len(t, stats.Rows, 2)
	for _, row := range stats.Rows {
		require.Zero(t, row.Count)
		require.False(t, row.Capped)
	}
	require.Positive(t, stats.DBBytes)
	wal, err := os.Stat(path + "-wal")
	require.NoError(t, err)
	require.Equal(t, wal.Size(), stats.WALBytes)

	require.NoError(t, s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error)
	stats, err = s.OperationalStats(context.Background(), "crypto", 10)
	require.NoError(t, err)
	require.Zero(t, stats.WALBytes)
	bytes, err := databaseFileBytes(filepath.Join(t.TempDir(), "missing-wal"), true)
	require.NoError(t, err)
	require.Zero(t, bytes)
	_, err = databaseFileBytes(filepath.Join(t.TempDir(), "missing-db"), false)
	require.Error(t, err)
}

func TestOperationalStatsSpaceCapped(t *testing.T) {
	s, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	for _, subject := range []string{"a", "b", "c"} {
		require.NoError(t, s.db.Exec(`INSERT INTO t_collector_backfill_notices (c_space_id, c_dataset_id, c_frequency, c_subject_id, c_start, c_end, c_origin) VALUES ('crypto', 'd', '1m', ?, '2026-10-21 10:00:00', '2026-10-21 11:00:00', 'auto')`, subject).Error)
	}
	require.NoError(t, s.db.Exec(`INSERT INTO t_collector_backfill_notices (c_space_id, c_dataset_id, c_frequency, c_subject_id, c_start, c_end, c_origin) VALUES ('other', 'd', '1m', 'x', '2026-10-21 10:00:00', '2026-10-21 11:00:00', 'auto')`).Error)
	stats, err := s.OperationalStats(context.Background(), "crypto", 2)
	require.NoError(t, err)
	require.Equal(t, OperationalRowCount{Count: 2, Capped: true}, stats.Rows["backfill_notices"])
	uncapped, err := s.OperationalStats(context.Background(), "crypto", 3)
	require.NoError(t, err)
	require.Equal(t, OperationalRowCount{Count: 3, Capped: false}, uncapped.Rows["backfill_notices"])

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.OperationalStats(ctx, "crypto", 2)
	require.Error(t, err)
	_, err = s.OperationalStats(context.Background(), "", 2)
	require.Error(t, err)
}
