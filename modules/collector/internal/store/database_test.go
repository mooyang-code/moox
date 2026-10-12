package store

import (
	"context"
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

func TestApplySchemaCreatesCurrentTaskAndInstanceTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}
	for _, table := range []string{"t_collector_tasks", "t_collector_task_tags", "t_collector_task_series", "t_collector_task_instances", "t_collector_instance_write_targets", "t_period_readiness", "t_period_readiness_items"} {
		var count int64
		if err := mgr.db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count).Error; err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}
	for table, columns := range map[string][]string{
		"t_collector_tasks":                  {"c_definition_hash", "c_series_hash", "c_result_dataset_id", "c_result_view_id"},
		"t_collector_task_tags":              {"c_task_id", "c_tag_id"},
		"t_collector_task_series":            {"c_task_id", "c_series_index", "c_series_key", "c_subject_id", "c_provider", "c_source_id", "c_market_type", "c_provider_symbol", "c_series_tag"},
		"t_collector_task_instances":         {"c_instance_id", "c_run_id", "c_request_key", "c_source_id", "c_series_tag"},
		"t_collector_instance_write_targets": {"c_write_target_id", "c_instance_id", "c_task_id", "c_dataset_id", "c_series_index", "c_series_hash", "c_expected_count"},
		"t_period_readiness_items":           {"c_write_target_id", "c_series_tag"},
	} {
		for _, column := range columns {
			var count int64
			if err := mgr.db.Raw("SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&count).Error; err != nil {
				t.Fatalf("query column %s.%s: %v", table, column, err)
			}
			if count != 1 {
				t.Fatalf("column %s.%s count = %d, want 1", table, column, count)
			}
		}
	}
	for _, index := range []string{"idx_collector_instances_run", "idx_collector_instances_space_page", "idx_collector_instances_list_page", "idx_collector_instances_space_status_page", "idx_collector_instances_storage_write", "idx_collector_instances_terminal_cleanup_space", "idx_collector_write_targets_retention_space", "idx_period_readiness_items_write_target"} {
		var count int64
		if err := mgr.db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?", index).Scan(&count).Error; err != nil {
			t.Fatalf("query index %s: %v", index, err)
		}
		if count != 1 {
			t.Fatalf("index %s count = %d, want 1", index, count)
		}
	}
	if _, _, err := mgr.TaskInstances().List(context.Background(), TaskInstanceFilter{Page: 1, PageSize: 1}); err != nil {
		t.Fatalf("query current task instances: %v", err)
	}
	for _, forbidden := range []string{"t_collector_run_tasks", "t_collector_run_series"} {
		var count int64
		if err := mgr.db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", forbidden).Scan(&count).Error; err != nil {
			t.Fatalf("query forbidden snapshot table %s: %v", forbidden, err)
		}
		if count != 0 {
			t.Fatalf("forbidden per-run snapshot table %s exists", forbidden)
		}
	}
}
