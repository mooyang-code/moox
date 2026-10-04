package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func TestOperationalStatsOldestQueriesUseCoveringIndexes(t *testing.T) {
	s := newCollectorStore(t)
	for _, test := range []struct{ query, index string }{
		{`SELECT c_ctime FROM t_collector_fetch_retry_items WHERE c_space_id = 'crypto' AND c_status = 'pending' ORDER BY c_ctime LIMIT 1`, "idx_collector_retry_operational_oldest"},
		{`SELECT c_period_time FROM t_collector_period_storage_states WHERE c_space_id = 'crypto' AND c_status = 'waiting' ORDER BY c_period_time LIMIT 1`, "idx_collector_period_operational_oldest"},
	} {
		var rows []struct{ Detail string }
		require.NoError(t, s.db.Raw("EXPLAIN QUERY PLAN "+test.query).Scan(&rows).Error)
		var details []string
		for _, row := range rows {
			details = append(details, row.Detail)
		}
		plan := strings.Join(details, " ")
		require.Contains(t, plan, test.index)
		require.NotContains(t, plan, "USE TEMP B-TREE")
	}
}

func TestOperationalStatsEmptyAndWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.db")
	s, err := Open(&Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	stats, err := s.OperationalStats(context.Background(), "crypto", time.Now().Add(-24*time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, stats.Rows, 6)
	for _, row := range stats.Rows {
		require.Zero(t, row.Count)
		require.False(t, row.Capped)
	}
	require.True(t, stats.OldestPendingRetry.IsZero())
	require.True(t, stats.OldestDispatchedRetry.IsZero())
	require.True(t, stats.OldestWaitingPeriod.IsZero())
	require.True(t, stats.OldestSnapshotCleanupCandidate.IsZero())
	require.Positive(t, stats.DBBytes)
	wal, err := os.Stat(path + "-wal")
	require.NoError(t, err)
	require.Equal(t, wal.Size(), stats.WALBytes)

	require.NoError(t, s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error)
	stats, err = s.OperationalStats(context.Background(), "crypto", time.Now(), 10)
	require.NoError(t, err)
	require.Zero(t, stats.WALBytes)
	bytes, err := databaseFileBytes(filepath.Join(t.TempDir(), "missing-wal"), true)
	require.NoError(t, err)
	require.Zero(t, bytes)
	_, err = databaseFileBytes(filepath.Join(t.TempDir(), "missing-db"), false)
	require.Error(t, err)
}

func TestOperationalStatsSpaceCappedAndOldest(t *testing.T) {
	s, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	for i, state := range []string{"pending", "dispatched", "succeeded"} {
		require.NoError(t, s.db.Exec(`INSERT INTO t_collector_fetch_retry_items (c_space_id, c_retry_key, c_source_batch_id, c_subject_id, c_frequency, c_target_data_time, c_status, c_ctime) VALUES (?, ?, '', 'subject-secret', '1m', ?, ?, ?)`, "crypto", state, old, state, old.Add(time.Duration(i)*time.Hour)).Error)
	}
	require.NoError(t, s.db.Exec(`INSERT INTO t_collector_fetch_retry_items (c_space_id, c_retry_key, c_source_batch_id, c_subject_id, c_frequency, c_target_data_time, c_status, c_ctime) VALUES ('other', 'pending', '', 'other-secret', '1m', ?, 'pending', ?)`, old, old.Add(-time.Hour)).Error)
	require.NoError(t, s.db.Exec(`INSERT INTO t_collector_period_storage_states (c_space_id, c_dataset_id, c_frequency, c_period_time, c_series_hash, c_expected_count, c_deadline_at, c_status, c_confirmed_at) VALUES ('crypto', 'task-secret', '1h', ?, 'hash', 1, ?, 'waiting', ?)`, old, old, old).Error)
	stats, err := s.OperationalStats(context.Background(), "crypto", time.Now().Add(-24*time.Hour), 2)
	require.NoError(t, err)
	require.Equal(t, OperationalRowCount{Count: 2, Capped: true}, stats.Rows["retry"])
	require.True(t, old.Equal(stats.OldestPendingRetry))
	require.True(t, old.Add(time.Hour).Equal(stats.OldestDispatchedRetry))
	require.True(t, old.Equal(stats.OldestWaitingPeriod))
	uncapped, err := s.OperationalStats(context.Background(), "crypto", time.Now().Add(-24*time.Hour), 3)
	require.NoError(t, err)
	require.Equal(t, OperationalRowCount{Count: 3, Capped: false}, uncapped.Rows["retry"])
	require.True(t, stats.OldestSnapshotCleanupCandidate.IsZero(), "waiting is not a cleanup candidate")
	require.NoError(t, s.db.Exec(`UPDATE t_collector_period_storage_states SET c_status = 'complete'`).Error)
	stats, err = s.OperationalStats(context.Background(), "crypto", time.Now().Add(-24*time.Hour), 2)
	require.NoError(t, err)
	require.True(t, old.Equal(stats.OldestSnapshotCleanupCandidate))
	require.True(t, stats.OldestWaitingPeriod.IsZero())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.OperationalStats(ctx, "crypto", time.Now(), 2)
	require.Error(t, err)
	_, err = s.OperationalStats(context.Background(), "", time.Now(), 2)
	require.Error(t, err)
}
