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
		"t_strategy_outbox",
		"t_strategy_results",
		"t_strategy_runners",
	}
	if !reflect.DeepEqual(tables, want) {
		t.Fatalf("strategy tables = %v, want %v", tables, want)
	}
}

func TestStrategySchemaUsesRunnerAndResultColumns(t *testing.T) {
	sql := AllSQL()
	for _, column := range []string{
		"runner_id",
		"logical_account_id",
		"current_targets_json",
		"command_sequence",
		"last_result_id",
		"result_id",
		"trigger_bar_time",
		"namespace",
		"input_hash",
		"output_json",
	} {
		if !strings.Contains(sql, column) {
			t.Errorf("strategy schema does not contain column %q", column)
		}
	}
	for _, indexPart := range []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_strategy_runners_enabled_logical_account",
		"WHERE logical_account_id IS NOT NULL AND status = 'ENABLED'",
		"UNIQUE (runner_id, strategy_id, namespace, trigger_bar_time)",
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
