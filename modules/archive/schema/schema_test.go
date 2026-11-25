package schema

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "github.com/glebarez/go-sqlite"
)

func TestArchiveSchemaContainsTheFourManagerTables(t *testing.T) {
	sql := AllSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS t_archive_tasks",
		"CREATE TABLE IF NOT EXISTS t_archive_runs",
		"CREATE TABLE IF NOT EXISTS t_archive_partitions",
		"CREATE TABLE IF NOT EXISTS t_archive_workers",
		"CHECK (c_kind IN ('follow', 'manual', 'range', 'import'))",
		"CHECK (c_purpose IN ('collect', 'factor', 'data'))",
		"UNIQUE (c_space_id, c_dataset_id)",
		"UNIQUE (c_space_id, c_purpose, c_dir_name)",
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

// openMemory 打开一个空的内存库并依次执行 sqlTexts。
func openMemory(t *testing.T, sqlTexts ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, text := range sqlTexts {
		if _, err := db.Exec(text); err != nil {
			t.Fatalf("执行 SQL 失败: %v", err)
		}
	}
	return db
}

// 启动时 store.Open 会对已建好的库再执行一遍 schema，重复执行不能出错。
func TestArchiveSchemaLoadsIntoAnEmptyDatabaseRepeatedly(t *testing.T) {
	openMemory(t, AllSQL(), AllSQL())
}

func TestArchiveSchemaConstraints(t *testing.T) {
	db := openMemory(t, AllSQL())
	exec := func(query string, args ...any) error {
		_, err := db.Exec(query, args...)
		return err
	}
	insertTask := `INSERT INTO t_archive_tasks (c_space_id, c_dataset_id, c_purpose, c_dir_name, c_freq, c_partition_unit, c_backfill_from)
VALUES (?, ?, ?, ?, '1m', 'day', '2026-11-01 00:00:00.000')`
	if err := exec(insertTask, "crypto", "ds_a", "collect", "binance-spot-kline-1m"); err != nil {
		t.Fatal(err)
	}
	// 同一空间、同一用途下目录名唯一；用途不同或空间不同可以同名。
	if err := exec(insertTask, "crypto", "ds_b", "collect", "binance-spot-kline-1m"); err == nil {
		t.Fatal("同一空间同一用途下目录名必须唯一")
	}
	if err := exec(insertTask, "crypto", "ds_c", "factor", "binance-spot-kline-1m"); err != nil {
		t.Fatalf("用途不同可以同名: %v", err)
	}
	if err := exec(insertTask, "stockcn", "ds_d", "collect", "binance-spot-kline-1m"); err != nil {
		t.Fatalf("空间不同可以同名: %v", err)
	}
	if err := exec(insertTask, "crypto", "ds_e", "other", "x"); err == nil {
		t.Fatal("用途只能是 collect、factor、data")
	}
	if err := exec(insertTask, "crypto", "ds_f", "data", ""); err == nil {
		t.Fatal("目录名不能为空")
	}

	if err := exec(`INSERT INTO t_archive_tasks (c_space_id, c_dataset_id, c_purpose, c_dir_name, c_freq, c_partition_unit)
VALUES ('crypto', 'ds_g', 'data', 'g', '1m', 'day')`); err == nil {
		t.Fatal("归档起点不能为空")
	}

	insertRun := `INSERT INTO t_archive_runs (c_run_id, c_space_id, c_dataset_id, c_kind, c_range_start, c_range_end, c_scheduled_at)
VALUES (?, 'crypto', 'ds_a', ?, ?, ?, '2026-11-08 00:00:00.000')`
	if err := exec(insertRun, "r1", "range", nil, nil); err == nil {
		t.Fatal("补拉区间必须带起止时间")
	}
	if err := exec(insertRun, "r2", "range", "2026-11-01 00:00:00.000", "2026-11-02 00:00:00.000"); err != nil {
		t.Fatalf("合法的补拉区间: %v", err)
	}
	for i, kind := range []string{"follow", "manual", "import"} {
		if err := exec(insertRun, fmt.Sprintf("r3_%d", i), kind, nil, nil); err != nil {
			t.Fatalf("合法的 %s 运行: %v", kind, err)
		}
	}
	for i, kind := range []string{"unknown", "tick", "scheduled"} {
		if err := exec(insertRun, fmt.Sprintf("r4_%d", i), kind, nil, nil); err == nil {
			t.Fatalf("运行类型 %s 必须被拒", kind)
		}
	}

	if err := exec(`INSERT INTO t_archive_workers (c_worker_id, c_disk_used_bytes, c_min_free_bytes) VALUES ('w', -1, 0)`); err == nil {
		t.Fatal("c_disk_used_bytes 不能为负")
	}
	if err := exec(`INSERT INTO t_archive_workers (c_worker_id, c_disk_used_bytes, c_min_free_bytes) VALUES ('w', 1, 5368709120)`); err != nil {
		t.Fatal(err)
	}
}
