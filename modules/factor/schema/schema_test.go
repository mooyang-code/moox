package schema

import (
	"strings"
	"testing"
)

func TestAllSQLContainsFactorSchemaObjects(t *testing.T) {
	sql := AllSQL()

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS t_factor_defs",
		"CREATE TABLE IF NOT EXISTS t_factor_bindings",
		"idx_factor_bindings_unique",
		"idx_factor_bindings_source",
		"update_factor_defs_mtime",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("AllSQL() missing %q", want)
		}
	}
}

func TestAllSQLDropsLegacyFactorRunTable(t *testing.T) {
	sql := AllSQL()
	if strings.Contains(sql, "CREATE TABLE IF NOT EXISTS t_factor_runs") {
		t.Fatal("factor schema must not recreate t_factor_runs")
	}
	if !strings.Contains(sql, "DROP TABLE IF EXISTS t_factor_runs") {
		t.Fatal("factor schema must drop legacy t_factor_runs")
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
