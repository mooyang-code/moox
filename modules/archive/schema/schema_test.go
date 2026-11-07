package schema

import (
	"strings"
	"testing"
)

func TestArchiveSchemaContainsTheFourManagerTables(t *testing.T) {
	sql := AllSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS t_archive_tasks",
		"CREATE TABLE IF NOT EXISTS t_archive_runs",
		"CREATE TABLE IF NOT EXISTS t_archive_partitions",
		"CREATE TABLE IF NOT EXISTS t_archive_workers",
		"CHECK (c_interval_seconds IN (3600, 86400))",
		"CHECK (c_pull_every_bars BETWEEN 1 AND 1440)",
		"CHECK (c_kind IN ('scheduled', 'manual', 'tick', 'range'))",
		"UNIQUE (c_space_id, c_dataset_id)",
		"idx_t_archive_tasks_status",
		"trg_t_archive_tasks_mtime",
		"trg_t_archive_runs_mtime",
		"trg_t_archive_partitions_mtime",
		"trg_t_archive_workers_mtime",
		"ON DELETE CASCADE",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("AllSQL() missing %q", want)
		}
	}
	if got := strings.Count(sql, "CREATE TABLE IF NOT EXISTS"); got != 4 {
		t.Fatalf("archive schema creates %d tables, want exactly 4", got)
	}
	for _, forbidden := range []string{"c_create_time", "c_update_time"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("AllSQL() must use c_ctime/c_mtime, found %q", forbidden)
		}
	}
}
