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
	for _, status := range []string{"succeeded", "failed", "running"} {
		require.NoError(t, s.db.Exec(`INSERT INTO t_collector_task_instances (c_space_id, c_instance_id, c_data_type, c_subject_id, c_frequency) VALUES ('crypto', ?, 'kline', ?, '1m')`, "instance-"+status, "subject-"+status).Error)
	}
	require.NoError(t, s.db.Exec(`INSERT INTO t_collector_task_instances (c_space_id, c_instance_id, c_data_type, c_subject_id, c_frequency) VALUES ('other', 'instance-other', 'kline', 'subject-other', '1m')`).Error)
	stats, err := s.OperationalStats(context.Background(), "crypto", 2)
	require.NoError(t, err)
	require.Equal(t, OperationalRowCount{Count: 2, Capped: true}, stats.Rows["instances"])
	uncapped, err := s.OperationalStats(context.Background(), "crypto", 3)
	require.NoError(t, err)
	require.Equal(t, OperationalRowCount{Count: 3, Capped: false}, uncapped.Rows["instances"])

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.OperationalStats(ctx, "crypto", 2)
	require.Error(t, err)
	_, err = s.OperationalStats(context.Background(), "", 2)
	require.Error(t, err)
}
