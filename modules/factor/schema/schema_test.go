package schema

import (
	"strings"
	"testing"
)

func TestFactorSchemaContainsExpectedTables(t *testing.T) {
	sql := AllSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS t_factor_sets",
		"CREATE TABLE IF NOT EXISTS t_factor_defs",
		"CREATE TABLE IF NOT EXISTS t_factor_set_members",
		"CREATE TABLE IF NOT EXISTS t_factor_recalc_jobs",
		"CREATE TABLE IF NOT EXISTS t_factor_engine_heartbeat",
		"CREATE TABLE IF NOT EXISTS t_factor_categories",
		"CREATE TABLE IF NOT EXISTS t_factor_param_requests",
		"CREATE TABLE IF NOT EXISTS t_factor_param_snapshots",
		"CREATE TABLE IF NOT EXISTS t_factor_param_freezes",
		"CREATE TABLE IF NOT EXISTS t_factor_param_frozen_periods",
		// 成员按别名标识：同一定义可以用不同别名加入一个集合多次。
		"PRIMARY KEY (c_set_id, c_member_alias)",
		"PRIMARY KEY (c_set_id, c_member_alias, c_start_period)",
		"UNIQUE (c_request_id, c_set_id, c_member_alias)",
		"FOREIGN KEY (c_category_id) REFERENCES t_factor_categories (c_category_id) ON DELETE RESTRICT",
		"idx_t_factor_sets_owner",
		"FOREIGN KEY (c_factor_id) REFERENCES t_factor_defs (c_factor_id) ON DELETE RESTRICT",
		"idx_t_factor_set_members_set",
		"idx_t_factor_set_members_factor",
		"trg_t_factor_set_members_mtime",
		"FOREIGN KEY (c_set_id) REFERENCES t_factor_sets (c_set_id),",
		"UNIQUE (c_request_id)",
		"trg_t_factor_sets_mtime",
		"trg_t_factor_defs_mtime",
		"trg_t_factor_recalc_jobs_mtime",
		"c_lease_expires_at INTEGER NOT NULL DEFAULT 0",
		"idx_t_factor_recalc_jobs_pull",
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
	// 同一源数据集可以有多个集合（各策略实例的私有集合）。
	if strings.Contains(sql, "UNIQUE (c_space_id, c_source_dataset_id, c_freq)") {
		t.Fatal("t_factor_sets must allow several sets over one source dataset")
	}
	if got := strings.Count(sql, "CREATE TABLE IF NOT EXISTS"); got != 10 {
		t.Fatalf("factor schema creates %d tables, want exactly 10", got)
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
