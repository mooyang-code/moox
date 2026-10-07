package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
)

func TestDeleteBeforeRemovesOldRowsInBatches(t *testing.T) {
	mgr, err := Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	cutoff := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	old := 2*retentionDeleteRows + 7
	for i := 0; i < old+5; i++ {
		at := cutoff.Add(-time.Duration(i+1) * time.Minute)
		if i >= old {
			at = cutoff.Add(time.Duration(i) * time.Minute)
		}
		require.NoError(t, mgr.db.Exec(`INSERT INTO t_monitor_alert_events (c_event_id, c_rule_id, c_check_id, c_event_type, c_status, c_created_at)
VALUES (?, 'r', 'c', 'fired', 'firing', ?)`, "e"+time.Duration(i).String(), at).Error)
	}
	deleted, err := DeleteBefore(context.Background(), mgr.db, "t_monitor_alert_events", "c_created_at", cutoff)
	require.NoError(t, err)
	require.EqualValues(t, old, deleted)
	var left int64
	require.NoError(t, mgr.db.Table("t_monitor_alert_events").Count(&left).Error)
	require.EqualValues(t, 5, left)

	var plan []struct{ Detail string }
	require.NoError(t, mgr.db.Raw("EXPLAIN QUERY PLAN SELECT c_id FROM t_monitor_check_results WHERE c_checked_at < ? ORDER BY c_checked_at LIMIT 10", cutoff).Scan(&plan).Error)
	require.Contains(t, plan[0].Detail, "idx_monitor_results_checked_at")
}
