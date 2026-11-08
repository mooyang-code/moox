package schema

import (
	"database/sql"
	"fmt"
	"os"
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

// openMemory 打开一个空的内存库并执行 sqlText。
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

// tableShape 返回库里每张表的列（按列序：名称、类型、非空、默认值、主键）与索引（名称、唯一、列），用于比对两个库的结构。
// 不比 sqlite_master.sql：ALTER TABLE 会改写它。
func tableShape(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	_ = tables.Close()
	out := map[string][]string{}
	for _, table := range names {
		cols, err := db.Query(`SELECT name, type, "notnull", COALESCE(dflt_value, '<NULL>'), pk FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			t.Fatal(err)
		}
		for cols.Next() {
			var name, typ, dflt string
			var notNull, pk int
			if err := cols.Scan(&name, &typ, &notNull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], fmt.Sprintf("col %s %s notnull=%d default=%s pk=%d", name, typ, notNull, dflt, pk))
		}
		_ = cols.Close()
		indexes, err := db.Query(`SELECT name, "unique", origin, partial FROM pragma_index_list(?) ORDER BY name`, table)
		if err != nil {
			t.Fatal(err)
		}
		var idx []string
		for indexes.Next() {
			var name, origin string
			var unique, partial int
			if err := indexes.Scan(&name, &unique, &origin, &partial); err != nil {
				t.Fatal(err)
			}
			idx = append(idx, fmt.Sprintf("index %s unique=%d origin=%s partial=%d", name, unique, origin, partial))
		}
		_ = indexes.Close()
		for _, line := range idx {
			name := strings.Fields(line)[1]
			var columns []string
			rows, err := db.Query(`SELECT COALESCE(name, '<expr>') FROM pragma_index_info(?) ORDER BY seqno`, name)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var col string
				if err := rows.Scan(&col); err != nil {
					t.Fatal(err)
				}
				columns = append(columns, col)
			}
			_ = rows.Close()
			out[table] = append(out[table], line+" columns="+strings.Join(columns, ","))
		}
	}
	return out
}

// 路 B 原地升级：批 1 的 schema 加上 testdata/upgrade-batch2.sql，必须和新 schema 建出的库结构一致。
func TestBatch2UpgradeSQLMatchesTheNewSchema(t *testing.T) {
	batch1, err := os.ReadFile("testdata/archive-batch1.sql")
	if err != nil {
		t.Fatal(err)
	}
	upgrade, err := os.ReadFile("testdata/upgrade-batch2.sql")
	if err != nil {
		t.Fatal(err)
	}
	fresh := tableShape(t, openMemory(t, AllSQL()))
	upgraded := tableShape(t, openMemory(t, string(batch1), string(upgrade)))
	if len(fresh) != 4 {
		t.Fatalf("新 schema 有 %d 张表", len(fresh))
	}
	for table, want := range fresh {
		got := upgraded[table]
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("表 %s 升级后与新建不一致：\n升级后：\n%s\n新建：\n%s", table, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
	if len(upgraded) != len(fresh) {
		t.Fatalf("升级后有 %d 张表，新建有 %d 张", len(upgraded), len(fresh))
	}
	// 新 schema 对已建好的库重复执行也不出错（启动时 store.Open 会这样做）。
	openMemory(t, string(batch1), string(upgrade), AllSQL())
}
