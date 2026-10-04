package schema

import (
	"reflect"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestAllSQLCreatesExactlyStrategyTables(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(AllSQL()).Error; err != nil {
		t.Fatalf("load strategy schema: %v", err)
	}

	var tables []string
	if err := db.Raw(`
		SELECT name
		FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`).Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	want := []string{
		"t_strategies",
		"t_strategy_instances",
		"t_strategy_results",
	}
	if !reflect.DeepEqual(tables, want) {
		t.Fatalf("strategy tables = %v, want %v", tables, want)
	}
}

func TestStrategySchemaUsesInstanceAndResultColumns(t *testing.T) {
	sql := AllSQL()
	for _, column := range []string{
		"strategy_name",
		"dsl_yaml",
		"instance_id",
		"logical_account_id",
		"input_bindings_json",
		"enabled",
		"session_id",
		"result_id",
		"bar_end_time",
		"valid_until",
		"snapshot_json",
		"targets_json",
		"rule_states_json",
		"event_data",
		"publish_status",
	} {
		if !strings.Contains(sql, column) {
			t.Errorf("strategy schema does not contain column %q", column)
		}
	}
	for _, indexPart := range []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_strategy_instances_enabled_account",
		"ON t_strategy_instances (space_id, logical_account_id)",
		"WHERE enabled = 1 AND logical_account_id IS NOT NULL",
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_strategy_results_session_bar",
		"ON t_strategy_results (instance_id, session_id, bar_end_time)",
		"CREATE INDEX IF NOT EXISTS ix_strategy_results_pending",
		"ON t_strategy_results (created_at, result_id)",
		"WHERE publish_status = 'pending'",
	} {
		if !strings.Contains(sql, indexPart) {
			t.Errorf("strategy schema does not contain %q", indexPart)
		}
	}
}

func TestStrategySchemaHasNoStateOrDataRevisionColumns(t *testing.T) {
	sql := strings.ToLower(AllSQL())
	for _, obsolete := range []string{
		"state_revision",
		"state_json",
		"state_schema_version",
		"previous_state_revision",
		"data_revision",
	} {
		if strings.Contains(sql, obsolete) {
			t.Errorf("strategy schema still contains obsolete column %q", obsolete)
		}
	}
}

func TestStrategySchemaHasExactly25Columns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(AllSQL()).Error; err != nil {
		t.Fatalf("load strategy schema: %v", err)
	}
	want := map[string][]string{
		"t_strategies": {
			"strategy_id", "strategy_name", "dsl_yaml", "created_at", "updated_at",
		},
		"t_strategy_instances": {
			"instance_id", "strategy_id", "space_id", "input_bindings_json",
			"logical_account_id", "enabled", "session_id", "created_at", "updated_at",
		},
		"t_strategy_results": {
			"result_id", "instance_id", "session_id", "bar_end_time", "valid_until",
			"snapshot_json", "targets_json", "rule_states_json", "event_data",
			"publish_status", "created_at",
		},
	}
	count := 0
	for table, columns := range want {
		var got []string
		if err := db.Raw(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table).Scan(&got).Error; err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if !reflect.DeepEqual(got, columns) {
			t.Errorf("%s columns = %v, want %v", table, got, columns)
		}
		count += len(got)
	}
	if count != 25 {
		t.Fatalf("strategy schema column count = %d, want 25", count)
	}
}
