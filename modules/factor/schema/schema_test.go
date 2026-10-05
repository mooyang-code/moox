package schema

import (
	"strings"
	"testing"
)

func TestFactorSchemaContainsOnlySetsDefsMembersAndRecalcJobs(t *testing.T) {
	sql := AllSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS t_factor_sets",
		"CREATE TABLE IF NOT EXISTS t_factor_defs",
		"CREATE TABLE IF NOT EXISTS t_factor_set_members",
		"CREATE TABLE IF NOT EXISTS t_factor_recalc_jobs",
		"PRIMARY KEY (c_set_id, c_factor_id)",
		"FOREIGN KEY (c_factor_id) REFERENCES t_factor_defs (c_factor_id) ON DELETE RESTRICT",
		"idx_t_factor_set_members_set",
		"idx_t_factor_set_members_factor",
		"trg_t_factor_set_members_mtime",
		"UNIQUE (c_space_id, c_source_dataset_id, c_freq)",
		"FOREIGN KEY (c_set_id) REFERENCES t_factor_sets (c_set_id),",
		"UNIQUE (c_request_id)",
		"trg_t_factor_sets_mtime",
		"trg_t_factor_defs_mtime",
		"trg_t_factor_recalc_jobs_mtime",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("AllSQL() missing %q", want)
		}
	}
	defs := sql[strings.Index(sql, "CREATE TABLE IF NOT EXISTS t_factor_defs"):strings.Index(sql, "CREATE TABLE IF NOT EXISTS t_factor_set_members")]
	for _, forbidden := range []string{"c_set_id", "c_status"} {
		if strings.Contains(defs, forbidden) {
			t.Fatalf("t_factor_defs must not contain %q", forbidden)
		}
	}
	if got := strings.Count(sql, "CREATE TABLE IF NOT EXISTS"); got != 4 {
		t.Fatalf("factor schema creates %d tables, want exactly 4", got)
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
