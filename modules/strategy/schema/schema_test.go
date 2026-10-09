package schema

import (
	"reflect"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openMemory(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(AllSQL()).Error; err != nil {
		t.Fatalf("载入策略 schema 失败：%v", err)
	}
	return db
}

func TestAllSQLCreatesExactlyStrategyTables(t *testing.T) {
	db := openMemory(t)
	var tables []string
	if err := db.Raw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`).Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	want := []string{
		"t_strategy_def_versions",
		"t_strategy_defs",
		"t_strategy_instances",
		"t_strategy_replay_bars",
		"t_strategy_replays",
		"t_strategy_result_items",
		"t_strategy_results",
		"t_strategy_sessions",
	}
	if !reflect.DeepEqual(tables, want) {
		t.Fatalf("表 = %v，期望 %v", tables, want)
	}
}

func TestStrategySchemaColumns(t *testing.T) {
	db := openMemory(t)
	want := map[string][]string{
		"t_strategy_defs":         {"c_strategy_id", "c_name", "c_dsl_yaml", "c_dsl_hash", "c_deleted_at", "c_ctime", "c_mtime"},
		"t_strategy_def_versions": {"c_dsl_hash", "c_dsl_yaml", "c_ctime"},
		"t_strategy_instances":    {"c_instance_id", "c_strategy_id", "c_space_id", "c_view_id", "c_logical_account_id", "c_enabled", "c_session_id", "c_resolved_json", "c_health", "c_deleted_at", "c_ctime", "c_mtime"},
		"t_strategy_sessions":     {"c_session_id", "c_instance_id", "c_dsl_hash", "c_resolved_json", "c_ctime", "c_closed_at"},
		"t_strategy_results":      {"c_result_id", "c_instance_id", "c_session_id", "c_bar_end_time", "c_valid_until", "c_status", "c_skip_reason", "c_dsl_hash", "c_input_json", "c_targets_json", "c_rule_states_json", "c_summary_json", "c_event_data", "c_publish_status", "c_ctime"},
		"t_strategy_result_items": {"c_result_id", "c_rule_id", "c_instrument_id", "c_stage", "c_score", "c_rank", "c_weight", "c_reason", "c_ctime"},
		"t_strategy_replays":      {"c_replay_id", "c_strategy_id", "c_instance_id", "c_session_id", "c_dsl_yaml", "c_dsl_hash", "c_view_index_id", "c_space_id", "c_view_id", "c_start_time", "c_end_time", "c_fee_bps", "c_factors_json", "c_status", "c_progress_time", "c_metrics_json", "c_error", "c_ctime", "c_mtime"},
		"t_strategy_replay_bars":  {"c_replay_id", "c_bar_end_time", "c_status", "c_targets_json", "c_positions_json", "c_summary_json", "c_return", "c_equity", "c_turnover", "c_fee", "c_holdings", "c_frozen", "c_skip_reason", "c_unfilled", "c_liquidated"},
	}
	for table, columns := range want {
		var got []string
		if err := db.Raw(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table).Scan(&got).Error; err != nil {
			t.Fatalf("读取 %s 列失败：%v", table, err)
		}
		if !reflect.DeepEqual(got, columns) {
			t.Errorf("%s 列 = %v，期望 %v", table, got, columns)
		}
	}
}

func TestStrategySchemaIndexesAndConstraints(t *testing.T) {
	sql := AllSQL()
	for _, part := range []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_t_strategy_instances_account",
		"ON t_strategy_instances (c_space_id, c_logical_account_id)",
		"WHERE c_enabled = 1 AND c_logical_account_id IS NOT NULL",
		"CREATE INDEX IF NOT EXISTS idx_t_strategy_results_instance_bar ON t_strategy_results (c_instance_id, c_bar_end_time);",
		"CREATE INDEX IF NOT EXISTS idx_t_strategy_results_pending",
		"ON t_strategy_results (c_ctime, c_result_id)",
		"WHERE c_publish_status = 'pending'",
		"CHECK (c_status IN ('ok', 'skipped'))",
		"CHECK (c_publish_status IN ('none', 'pending', 'sent', 'cancelled'))",
		"UNIQUE (c_instance_id, c_session_id, c_bar_end_time)",
		"ON DELETE CASCADE",
	} {
		if !strings.Contains(sql, part) {
			t.Errorf("schema 缺少 %q", part)
		}
	}
	for _, obsolete := range []string{"state_json", "data_revision", "input_bindings_json", "snapshot_json", "c_topic", "c_payload"} {
		if strings.Contains(strings.ToLower(sql), obsolete) {
			t.Errorf("schema 仍包含过时列 %q", obsolete)
		}
	}
}

func TestResultItemsCascadeOnResultDelete(t *testing.T) {
	db := openMemory(t)
	if err := db.Exec(`PRAGMA foreign_keys = ON`).Error; err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO t_strategy_defs (c_strategy_id, c_name, c_dsl_yaml, c_dsl_hash) VALUES ('s', 'n', 'name: n', 'h')`,
		`INSERT INTO t_strategy_def_versions (c_dsl_hash, c_dsl_yaml) VALUES ('h', 'name: n')`,
		`INSERT INTO t_strategy_instances (c_instance_id, c_strategy_id, c_space_id, c_view_id) VALUES ('i', 's', 'sp', 'v')`,
		`INSERT INTO t_strategy_sessions (c_session_id, c_instance_id, c_dsl_hash, c_resolved_json) VALUES ('se', 'i', 'h', '{}')`,
		`INSERT INTO t_strategy_results (c_result_id, c_instance_id, c_session_id, c_bar_end_time, c_valid_until, c_status, c_dsl_hash, c_input_json, c_targets_json, c_rule_states_json, c_summary_json) VALUES ('r', 'i', 'se', 1, 2, 'ok', 'h', '{}', '[]', '{}', '{}')`,
		`INSERT INTO t_strategy_result_items (c_result_id, c_rule_id, c_instrument_id, c_stage, c_ctime) VALUES ('r', 'rule', 'BTC-USDT', 'weighted', '2026-10-01 00:00:00')`,
		`DELETE FROM t_strategy_results WHERE c_result_id = 'r'`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("%s：%v", statement, err)
		}
	}
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM t_strategy_result_items`).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("解释明细应随结果级联删除，剩余 %d", count)
	}
}
