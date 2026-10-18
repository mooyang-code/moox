//go:build cgo

package duckdb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func tempDirTestSchema() viewindex.ViewIndexSchema {
	return viewindex.ViewIndexSchema{
		SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash",
		Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
}

func openTempDirManager(t *testing.T) (*IndexManager, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "duckdb")
	manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager, root
}

func resolved(t *testing.T, path string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("目录 %s 不存在：%v", path, err)
	}
	return got
}

// 线上事故：所有索引共用同一个 DuckDB 临时目录，临时文件重名后互相删除，提交失败，索引再也写不进去。
// 每个索引必须有自己的临时目录。
func TestEachIndexUsesItsOwnTempDirectory(t *testing.T) {
	manager, root := openTempDirManager(t)
	ids := []string{"idx-a", "idx-b"}
	seen := map[string]string{}
	for _, id := range ids {
		if err := manager.Prepare(context.Background(), id, tempDirTestSchema()); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := manager.dbs[id].QueryRow(`SELECT current_setting('temp_directory')`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, id+".duckdb.tmp")
		if resolved(t, got) != resolved(t, want) {
			t.Fatalf("索引 %s 的临时目录 = %s，want %s", id, got, want)
		}
		if other, dup := seen[got]; dup {
			t.Fatalf("索引 %s 与 %s 共用临时目录 %s", id, other, got)
		}
		seen[got] = id
	}
}

func TestPrepareLeavesNoPreparedTempDirectory(t *testing.T) {
	manager, root := openTempDirManager(t)
	if err := manager.Prepare(context.Background(), "idx", tempDirTestSchema()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".prepare-") {
			t.Fatalf("准备阶段的残留 %s 没有清理", entry.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(root, "idx.duckdb.tmp")); err != nil {
		t.Fatalf("正式索引应有自己的临时目录：%v", err)
	}
}

func TestRemoveDeletesTheIndexTempDirectory(t *testing.T) {
	manager, root := openTempDirManager(t)
	if err := manager.Prepare(context.Background(), "idx", tempDirTestSchema()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "idx.duckdb.tmp", "duckdb_temp_storage_S32K-0.tmp"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(context.Background(), "idx"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"idx.duckdb", "idx.duckdb.wal", "idx.duckdb.tmp"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s 应在删除索引后一并清理：%v", name, err)
		}
	}
}

func TestOpenIndexManagerClearsInterruptedPrepareArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "duckdb")
	if err := os.MkdirAll(filepath.Join(root, "idx.duckdb.prepare-9.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "idx.duckdb.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"idx.duckdb.prepare-9",
		"idx.duckdb.prepare-9.wal",
		filepath.Join("idx.duckdb.prepare-9.tmp", "duckdb_temp_storage_S32K-0.tmp"),
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	for _, name := range []string{"idx.duckdb.prepare-9", "idx.duckdb.prepare-9.wal", "idx.duckdb.prepare-9.tmp"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("中断留下的 %s 没有清理：%v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "idx.duckdb.tmp")); err != nil {
		t.Fatalf("正式索引的临时目录不应被当作准备残留清掉：%v", err)
	}
}
