package schema

import (
	"strings"
	"testing"
)

func TestFactorSchemaContainsExpectedTables(t *testing.T) {
	sql := AllSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS t_factor_tasks",
		"CREATE TABLE IF NOT EXISTS t_factor_defs",
		"CREATE TABLE IF NOT EXISTS t_factor_task_members",
		"CREATE TABLE IF NOT EXISTS t_factor_recalc_jobs",
		"CREATE TABLE IF NOT EXISTS t_factor_engine_heartbeat",
		"CREATE TABLE IF NOT EXISTS t_factor_categories",
		// 成员按成员键标识：同一因子可以用不同参数加入一个任务多次；一个因子最多一个可变成员。
		"PRIMARY KEY (c_task_id, c_member_key)",
		"ux_t_factor_task_members_var",
		"c_last_submitted_params TEXT NOT NULL DEFAULT '{}'",
		"UNIQUE (c_space_id, c_name)",
		"ux_t_factor_tasks_bound_instance",
		"c_default_filled_json TEXT NOT NULL DEFAULT '{}'",
		"FOREIGN KEY (c_category_id) REFERENCES t_factor_categories (c_category_id) ON DELETE RESTRICT",
		"FOREIGN KEY (c_factor_id) REFERENCES t_factor_defs (c_factor_id) ON DELETE RESTRICT",
		"idx_t_factor_task_members_task",
		"idx_t_factor_task_members_factor",
		"trg_t_factor_task_members_mtime",
		"FOREIGN KEY (c_task_id) REFERENCES t_factor_tasks (c_task_id),",
		"UNIQUE (c_request_id)",
		"trg_t_factor_tasks_mtime",
		"trg_t_factor_defs_mtime",
		"trg_t_factor_recalc_jobs_mtime",
		"c_lease_expires_at INTEGER NOT NULL DEFAULT 0",
		"idx_t_factor_recalc_jobs_pull",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("AllSQL() missing %q", want)
		}
	}
	defs := sql[strings.Index(sql, "CREATE TABLE IF NOT EXISTS t_factor_defs"):strings.Index(sql, "CREATE TABLE IF NOT EXISTS t_factor_task_members")]
	for _, forbidden := range []string{"c_task_id", "c_status"} {
		if strings.Contains(defs, forbidden) {
			t.Fatalf("t_factor_defs must not contain %q", forbidden)
		}
	}
	// 同一源数据集可以有多个任务（各策略实例各一个）。
	if strings.Contains(sql, "UNIQUE (c_space_id, c_source_dataset_id, c_freq)") {
		t.Fatal("t_factor_tasks must allow several sets over one source dataset")
	}
	if got := strings.Count(sql, "CREATE TABLE IF NOT EXISTS"); got != 6 {
		t.Fatalf("factor schema creates %d tables, want exactly 6", got)
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
