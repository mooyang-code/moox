package staging

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stateShape 返回两张永久表的列（按列序）与索引，用于比对；不比 sqlite_master.sql（ALTER TABLE 会改写它）。
func stateShape(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out []string
	for _, table := range []string{"task_state", "partition_state"} {
		rows, err := db.Query(`SELECT name, type, "notnull", COALESCE(dflt_value, '<NULL>'), pk FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name, typ, dflt string
			var notNull, pk int
			if err := rows.Scan(&name, &typ, &notNull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%s.%s %s notnull=%d default=%s pk=%d", table, name, typ, notNull, dflt, pk))
		}
		_ = rows.Close()
		idx, err := db.Query(`SELECT name, "unique", origin FROM pragma_index_list(?) ORDER BY name`, table)
		if err != nil {
			t.Fatal(err)
		}
		for idx.Next() {
			var name, origin string
			var unique int
			if err := idx.Scan(&name, &unique, &origin); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%s index %s unique=%d origin=%s", table, name, unique, origin))
		}
		_ = idx.Close()
	}
	return strings.Join(out, "\n")
}

// 路 B 原地升级：批 1 的暂存库加上 testdata/upgrade-batch2.sql，必须和新建的暂存库结构一致，升级后的库能照常打开使用。
func TestBatch2UpgradeSQLMatchesTheNewStagingTables(t *testing.T) {
	batch1, err := os.ReadFile("testdata/staging-batch1.sql")
	if err != nil {
		t.Fatal(err)
	}
	upgrade, err := os.ReadFile("testdata/upgrade-batch2.sql")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	fresh, err := Open(filepath.Join(dir, "fresh.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := stateShape(t, fresh.db)
	_ = fresh.Close()

	oldPath := filepath.Join(dir, "old.db")
	old, err := openDB(oldPath, Options{CacheBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(batch1), `INSERT INTO partition_state (space_id, dataset_id, partition_key, range_start, range_end, status, last_changed_at, exported_at, file_path, file_sha256)
VALUES ('crypto', 'ds', '2026-10-16', 1, 2, 'final', 1, 1, 'crypto/ds/2026/20261016.csv.gz', 'abc')`, string(upgrade)} {
		if _, err := old.Exec(text); err != nil {
			t.Fatalf("执行 SQL 失败: %v", err)
		}
	}
	if got := stateShape(t, old); got != want {
		t.Fatalf("升级后与新建不一致：\n升级后：\n%s\n新建：\n%s", got, want)
	}
	_ = old.Close()
	upgraded, err := Open(oldPath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	parts, err := upgraded.Partitions(t.Context(), "crypto", "ds")
	if err != nil || len(parts) != 1 || parts[0].Remote.Key != "" || parts[0].Remote.Attempts != 0 || parts[0].Replicated() {
		t.Fatalf("升级后的分区 = %+v, %v", parts, err)
	}
	pending, err := upgraded.PendingUploads(t.Context(), "crypto", "ds", parts[0].ExportedAt)
	if err != nil || len(pending) != 1 {
		t.Fatalf("升级后已有分区应是待上传: %+v, %v", pending, err)
	}
}
