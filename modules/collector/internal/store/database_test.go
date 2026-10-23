package store

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func TestInitializeDoesNotCreateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	require.NotNil(t, mgr.Tasks())
	var absent *Store
	require.Nil(t, absent.Tasks())
	var count int64
	if err := mgr.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name LIKE 't_collector_%'`).Scan(&count).Error; err != nil {
		t.Fatalf("query table count: %v", err)
	}
	if count != 0 {
		t.Fatalf("Open() created %d collector tables, want 0", count)
	}
}

func TestApplySchemaCreatesCurrentTablesAndDropsRemovedResampleStructures(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	// 升级前的库里还留着旧的逐标的合成实例、写入目标和周期就绪表：升级后应被清掉。
	for _, stale := range []string{"t_collector_task_instances", "t_collector_instance_write_targets", "t_period_readiness", "t_period_readiness_items"} {
		require.NoError(t, mgr.db.Exec("CREATE TABLE "+stale+" (c_id INTEGER)").Error)
	}
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() must be idempotent: %v", err)
	}
	count := func(query string, args ...any) int64 {
		var n int64
		if err := mgr.db.Raw(query, args...).Scan(&n).Error; err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		return n
	}
	for _, table := range []string{"t_collector_tasks", "t_collector_task_tags", "t_collector_task_series", "t_collector_resample_snapshots", "t_collector_resample_jobs", "t_collector_backfill_notices", "t_collector_event_outbox"} {
		require.EqualValues(t, 1, count("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table), table)
	}
	for _, table := range []string{"t_collector_task_instances", "t_collector_instance_write_targets", "t_period_readiness", "t_period_readiness_items", "t_collector_run_tasks", "t_collector_run_series"} {
		require.Zero(t, count("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table), "removed table %s must not exist", table)
	}
	for table, columns := range map[string][]string{
		"t_collector_tasks":       {"c_definition_hash", "c_series_hash", "c_result_dataset_id", "c_result_view_id"},
		"t_collector_task_tags":   {"c_task_id", "c_tag_id"},
		"t_collector_task_series": {"c_task_id", "c_series_index", "c_series_key", "c_subject_id", "c_provider", "c_source_id", "c_market_type", "c_provider_symbol", "c_series_tag"},
	} {
		for _, column := range columns {
			require.EqualValues(t, 1, count("SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", table, column), "%s.%s", table, column)
		}
	}
}
