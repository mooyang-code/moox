package schema

import (
	"strings"
	"testing"
)

func TestFactorSchemaContainsOnlyDefinitionAndBindingState(t *testing.T) {
	sql := AllSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS t_factor_defs",
		"CREATE TABLE IF NOT EXISTS t_factor_bindings",
		"c_periods_json TEXT NOT NULL",
		"c_depends_json TEXT NOT NULL",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("AllSQL() missing %q", want)
		}
	}
	for _, removed := range []string{
		"c_kind",
		"c_params_json",
		"c_avg_runtime_ms",
		"c_writeback_bars",
		"t_factor_event_inbox",
		"t_factor_event_processed",
		"t_factor_replay_tasks",
		"t_factor_runs",
	} {
		if strings.Contains(sql, removed) {
			t.Fatalf("factor schema still contains retired state %q", removed)
		}
	}
}

func TestAllSQLUsesRepositoryTimeColumnConvention(t *testing.T) {
	sql := AllSQL()

	for _, forbidden := range []string{"c_create_time", "c_update_time"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("AllSQL() must use c_ctime/c_mtime, found %q", forbidden)
		}
	}
}
