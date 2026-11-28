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
		"CHECK (c_status IN ('enabled', 'disabled'))",
		"CHECK (c_kind IN ('manual', 'range', 'import'))",
		"CHECK (c_status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled'))",
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
	for _, forbidden := range []string{"c_create_time", "c_update_time", "c_subjects_total", "c_creator", "c_partition_unit", "c_retention", "c_meta_json", "DATETIME", "CURRENT_TIMESTAMP", "'paused'", "'canceled'", "'follow'"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("AllSQL() must drop dead columns and legacy status words, found %q", forbidden)
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
	insertTask := `INSERT INTO t_archive_tasks (c_space_id, c_dataset_id, c_purpose, c_dir_name, c_freq, c_backfill_from)
VALUES (?, ?, ?, ?, '1m', 1793491200000)`
	if err := exec(insertTask, "crypto", "ds_a", "collect", "binance-spot-kline-1m"); err != nil {
		t.Fatal(err)
	}
	var ctime, mtime int64
	if err := db.QueryRow(`SELECT c_ctime, c_mtime FROM t_archive_tasks WHERE c_dataset_id = 'ds_a'`).Scan(&ctime, &mtime); err != nil || ctime < 1_700_000_000_000 || mtime < 1_700_000_000_000 {
		t.Fatalf("c_ctime/c_mtime 应为 Unix 毫秒整数: ctime=%d mtime=%d err=%v", ctime, mtime, err)
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

	if err := exec(`INSERT INTO t_archive_tasks (c_space_id, c_dataset_id, c_purpose, c_dir_name, c_freq)
VALUES ('crypto', 'ds_g', 'data', 'g', '1m')`); err == nil {
		t.Fatal("归档起点不能为空")
	}
	if err := exec(`UPDATE t_archive_tasks SET c_rescan_from = 1793491200000, c_rescan_until = 0 WHERE c_dataset_id = 'ds_a'`); err == nil {
		t.Fatal("重读进度起止必须同时为 0 或同时大于 0")
	}
	if err := exec(`UPDATE t_archive_tasks SET c_status = 'paused' WHERE c_dataset_id = 'ds_a'`); err == nil {
		t.Fatal("任务状态不再接受 paused")
	}
	if err := exec(`UPDATE t_archive_tasks SET c_status = 'disabled' WHERE c_dataset_id = 'ds_a'`); err != nil {
		t.Fatalf("任务状态应接受 disabled: %v", err)
	}

	insertRun := `INSERT INTO t_archive_runs (c_run_id, c_space_id, c_dataset_id, c_kind, c_range_start, c_range_end, c_scheduled_at)
VALUES (?, 'crypto', 'ds_a', ?, ?, ?, 1794096000000)`
	if err := exec(insertRun, "r1", "range", 0, 0); err == nil {
		t.Fatal("补拉区间必须带起止时间")
	}
	if err := exec(insertRun, "r2", "range", 1793491200000, 1793577600000); err != nil {
		t.Fatalf("合法的补拉区间: %v", err)
	}
	for i, kind := range []string{"manual", "import"} {
		if err := exec(insertRun, fmt.Sprintf("r3_%d", i), kind, 0, 0); err != nil {
			t.Fatalf("合法的 %s 运行: %v", kind, err)
		}
	}
	for i, kind := range []string{"follow", "unknown", "tick", "scheduled"} {
		if err := exec(insertRun, fmt.Sprintf("r4_%d", i), kind, 0, 0); err == nil {
			t.Fatalf("运行类型 %s 必须被拒", kind)
		}
	}
	if err := exec(`UPDATE t_archive_runs SET c_status = 'canceled' WHERE c_run_id = 'r2'`); err == nil {
		t.Fatal("运行状态不再接受 canceled")
	}
	if err := exec(`UPDATE t_archive_runs SET c_status = 'cancelled' WHERE c_run_id = 'r2'`); err != nil {
		t.Fatalf("运行状态应接受 cancelled: %v", err)
	}

	if err := exec(`INSERT INTO t_archive_workers (c_worker_id, c_disk_used_bytes, c_min_free_bytes) VALUES ('w', -1, 0)`); err == nil {
		t.Fatal("c_disk_used_bytes 不能为负")
	}
	if err := exec(`INSERT INTO t_archive_workers (c_worker_id, c_disk_used_bytes, c_min_free_bytes) VALUES ('w', 1, 5368709120)`); err != nil {
		t.Fatal(err)
	}
}
