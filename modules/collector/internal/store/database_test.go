package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/schema"
)

func TestInitializeDoesNotCreateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if _, err := mgr.db.DB(); err != nil {
		t.Fatalf("DB() error = %v", err)
	}

	var count int64
	if err := mgr.db.Raw(`
SELECT count(*)
FROM sqlite_master
WHERE type = 'table'
  AND name LIKE 't_collector_%'
`).Scan(&count).Error; err != nil {
		t.Fatalf("query table count: %v", err)
	}
	if count != 0 {
		t.Fatalf("Initialize() created %d collector tables, want 0", count)
	}
}

func TestApplySchemaAddsTaskInstanceFunctionColumnBeforeIndexes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.db.Exec(`CREATE TABLE t_collector_task_instances (
		c_id INTEGER PRIMARY KEY AUTOINCREMENT,
		c_space_id TEXT NOT NULL DEFAULT '',
		c_task_id TEXT NOT NULL,
		c_rule_id TEXT NOT NULL,
		c_provider TEXT NOT NULL DEFAULT '',
		c_market_type TEXT NOT NULL DEFAULT '',
		c_data_type TEXT NOT NULL DEFAULT '',
		c_dataset_id TEXT NOT NULL DEFAULT '',
		c_subject_id TEXT NOT NULL DEFAULT '',
		c_frequency TEXT NOT NULL DEFAULT '',
		c_last_exec_node TEXT NOT NULL DEFAULT '',
		c_last_exec_status INTEGER NOT NULL DEFAULT 1,
		c_task_params TEXT NOT NULL DEFAULT '{}',
		c_last_exec_time DATETIME,
		c_result TEXT NOT NULL DEFAULT '{}',
		c_is_deleted INTEGER NOT NULL DEFAULT 0,
		c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
		c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}
	var columnCount int64
	if err := mgr.db.Raw("SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", "t_collector_task_instances", "c_function_name").Scan(&columnCount).Error; err != nil {
		t.Fatalf("query migrated column: %v", err)
	}
	if columnCount != 1 {
		t.Fatalf("c_function_name count = %d, want 1", columnCount)
	}
	// Keep the test tied to a real repository query so the dependent index is
	// exercised rather than only checking PRAGMA metadata.
	if _, _, err := mgr.TaskInstances().List(context.Background(), TaskInstanceFilter{Page: 1, PageSize: 1}); err != nil {
		t.Fatalf("query migrated task instances: %v", err)
	}
}

func TestApplySchemaAddsTaskRulePreparationColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.db.Exec(`CREATE TABLE t_collector_task_rules (
		c_id INTEGER PRIMARY KEY AUTOINCREMENT,
		c_space_id TEXT NOT NULL DEFAULT '',
		c_rule_id TEXT NOT NULL,
		c_data_type TEXT NOT NULL DEFAULT '',
		c_provider TEXT NOT NULL DEFAULT '',
		c_market_type TEXT NOT NULL DEFAULT '',
		c_collect_params TEXT NOT NULL DEFAULT '{}',
		c_enabled INTEGER NOT NULL DEFAULT 1,
		c_creator TEXT NOT NULL DEFAULT '',
		c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
		c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create legacy rule table: %v", err)
	}
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}
	for _, column := range []string{"c_prepare_state", "c_last_error"} {
		var count int64
		if err := mgr.db.Raw("SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", "t_collector_task_rules", column).Scan(&count).Error; err != nil {
			t.Fatalf("query migrated column %s: %v", column, err)
		}
		if count != 1 {
			t.Fatalf("%s count = %d, want 1", column, count)
		}
	}
}

func TestApplySchemaAddsCoverageStartTimeWithoutLosingLegacyRules(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.db.Exec(`CREATE TABLE t_collector_task_rules (
		c_id INTEGER PRIMARY KEY AUTOINCREMENT,
		c_space_id TEXT NOT NULL DEFAULT '',
		c_rule_id TEXT NOT NULL,
		c_data_type TEXT NOT NULL DEFAULT '',
		c_provider TEXT NOT NULL DEFAULT '',
		c_market_type TEXT NOT NULL DEFAULT '',
		c_collect_params TEXT NOT NULL DEFAULT '{}',
		c_enabled INTEGER NOT NULL DEFAULT 1,
		c_creator TEXT NOT NULL DEFAULT '',
		c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
		c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create legacy rule table: %v", err)
	}
	if err := mgr.db.Exec(`INSERT INTO t_collector_task_rules (
		c_space_id, c_rule_id, c_data_type, c_provider, c_market_type, c_collect_params, c_enabled, c_creator, c_ctime, c_mtime
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"crypto_market",
		"legacy-rule",
		"kline",
		"binance",
		"spot",
		`{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"binance_spot_symbols","target_dataset_id":"binance_spot_kline_1m","frequency":"1m"}`,
		1,
		"test",
		"2026-08-01T12:34:00Z",
		"2026-08-01T12:34:00Z",
	).Error; err != nil {
		t.Fatalf("insert legacy rule: %v", err)
	}
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}

	var columnCount int64
	if err := mgr.db.Raw("SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", "t_collector_task_rules", "c_coverage_start_time").Scan(&columnCount).Error; err != nil {
		t.Fatalf("query migrated coverage_start_time column: %v", err)
	}
	if columnCount != 1 {
		t.Fatalf("c_coverage_start_time count = %d, want 1", columnCount)
	}

	var got struct {
		RuleID            string
		CoverageStartTime string
	}
	if err := mgr.db.Raw(`SELECT c_rule_id AS rule_id, COALESCE(c_coverage_start_time, '') AS coverage_start_time FROM t_collector_task_rules WHERE c_rule_id = ?`, "legacy-rule").Scan(&got).Error; err != nil {
		t.Fatalf("query migrated legacy rule: %v", err)
	}
	if got.RuleID != "legacy-rule" {
		t.Fatalf("rule_id = %q, want legacy-rule", got.RuleID)
	}
	if got.CoverageStartTime == "" {
		t.Fatal("coverage_start_time should be backfilled for legacy rule")
	}
}
