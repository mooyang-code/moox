package schema

import (
	"strings"
	"testing"
)

func TestFactorSchemaContainsOnlySetsDefsAndRecalcJobs(t *testing.T) {
	sql := AllSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS t_factor_sets",
		"CREATE TABLE IF NOT EXISTS t_factor_defs",
		"CREATE TABLE IF NOT EXISTS t_factor_recalc_jobs",
		"UNIQUE (c_space_id, c_source_dataset_id, c_freq)",
		"FOREIGN KEY (c_set_id) REFERENCES t_factor_sets (c_set_id)",
		"UNIQUE (c_request_id)",
		"trg_t_factor_sets_mtime",
		"trg_t_factor_defs_mtime",
		"trg_t_factor_recalc_jobs_mtime",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("AllSQL() missing %q", want)
		}
	}
	for _, removed := range []string{
		"t_factor_subject_runs", "t_factor_subject_heads", "t_factor_period_barriers",
		"t_factor_period_pairs", "t_factor_output_manifests", "t_factor_bindings",
		"t_factor_merged_datasets", "t_factor_catalog", "t_factor_engine_status",
	} {
		if strings.Contains(sql, removed) {
			t.Fatalf("factor schema still contains retired table %q", removed)
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
