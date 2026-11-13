package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestValidateRepairViewRequiresConfirmation(t *testing.T) {
	opts := repairViewOptions{spaceID: "space", viewID: "view", timeout: defaultRepairTimeout}
	if err := validateRepairViewOptions(opts); err == nil {
		t.Fatal("没有 --yes 或 --dry-run 时应拒绝执行")
	}
	opts.dryRun = true
	if err := validateRepairViewOptions(opts); err != nil {
		t.Fatal(err)
	}
	opts = repairViewOptions{spaceID: "space", timeout: defaultRepairTimeout, yes: true}
	if err := validateRepairViewOptions(opts); err == nil {
		t.Fatal("缺少 --view-id 时应拒绝执行")
	}
}

func TestResolveRepairPackageRootWalksUpFromStorageConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "start.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(root, "storage", "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveRepairPackageRoot("", filepath.Join(configDir, "storage.yaml")); got != root {
		t.Fatalf("package root = %q, want %q", got, root)
	}
}

func TestResetRepairViewMetadataClearsActiveIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE t_views (
  c_space_id TEXT, c_view_id TEXT, c_active_index_id TEXT,
  c_view_revision INTEGER, c_active_columns_json TEXT, c_active_view_schema_hash TEXT,
  c_indexed_from TEXT, c_indexed_to TEXT, c_attrs_json TEXT, c_mtime TEXT,
  PRIMARY KEY(c_space_id,c_view_id));`)
	if err != nil {
		t.Fatal(err)
	}
	view := &storagepb.View{
		SpaceId:       "space",
		ViewId:        "view",
		ActiveIndexId: "view_s7370616365_v76696577",
		ViewRevision:  3,
		ActiveColumns: []*storagepb.ViewColumn{{ColumnName: "close"}},
		IndexedFrom:   "2026-08-01T00:00:00Z",
		IndexedTo:     "2026-08-02T00:00:00Z",
	}
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t_views VALUES (?,?,?,?,?,?,?,?,?,?)`, "space", "view", view.GetActiveIndexId(), 3, `[{"column_name":"close"}]`, "hash", view.GetIndexedFrom(), view.GetIndexedTo(), string(raw), ""); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := resetRepairViewMetadata(context.Background(), dbPath, "space", "view"); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var active string
	var revision uint64
	if err := db.QueryRow(`SELECT c_active_index_id,c_view_revision FROM t_views WHERE c_space_id='space' AND c_view_id='view'`).Scan(&active, &revision); err != nil {
		t.Fatal(err)
	}
	if active != "" || revision != 0 {
		t.Fatalf("reset state = active %q revision %d", active, revision)
	}
}

func TestPurgeRepairViewIndexesRemovesIndexAndWAL(t *testing.T) {
	root := t.TempDir()
	duckRoot := filepath.Join(root, "duckdb")
	if err := os.MkdirAll(duckRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	id := viewindex.ViewIndexID("space", "view")
	for _, name := range []string{id + ".duckdb", id + ".duckdb.wal"} {
		if err := os.WriteFile(filepath.Join(duckRoot, name), []byte(id), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := purgeRepairViewIndexes(root, "duckdb", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed paths = %v, want index and WAL", removed)
	}
	if _, err := os.Stat(filepath.Join(duckRoot, id+".duckdb")); !os.IsNotExist(err) {
		t.Fatalf("index still exists, stat err=%v", err)
	}
}

// repair-view 按顺序停 storage-view、备份元数据、清掉活动索引指针、删物理索引、再启动。
func TestRepairViewRebuildsTheNamedView(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "metadata.db")
	indexRoot := filepath.Join(root, "view-indexes")
	confPath := filepath.Join(root, "config", "storage.yaml")
	if err := os.MkdirAll(filepath.Dir(confPath), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "storage:\n  metadata:\n    path: " + dbPath + "\n  devices:\n    view_index_root: " + indexRoot + "\n"
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	id := viewindex.ViewIndexID("space", "view")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t_views (
  c_space_id TEXT, c_view_id TEXT, c_engine TEXT, c_active_index_id TEXT,
  c_view_revision INTEGER, c_active_columns_json TEXT, c_active_view_schema_hash TEXT,
  c_indexed_from TEXT, c_indexed_to TEXT, c_attrs_json TEXT, c_mtime TEXT,
  PRIMARY KEY(c_space_id,c_view_id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t_views VALUES ('space','view','duckdb',?,1,'[]','h','','','{"space_id":"space","view_id":"view"}','')`, id); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(indexRoot, "duckdb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(indexRoot, "duckdb", id+".duckdb"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	previous := runRepairLifecycle
	runRepairLifecycle = func(_ context.Context, _, action string, _ io.Writer) error {
		calls = append(calls, action)
		return nil
	}
	t.Cleanup(func() { runRepairLifecycle = previous })

	var out bytes.Buffer
	if err := runRepairView([]string{"--space-id", "space", "--view-id", "view", "--storage-conf", confPath, "--package-root", root, "--yes"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "stop,start" {
		t.Fatalf("lifecycle calls = %v", calls)
	}
	if _, err := os.Stat(filepath.Join(indexRoot, "duckdb", id+".duckdb")); !os.IsNotExist(err) {
		t.Fatalf("索引文件应已删除，stat err=%v", err)
	}
	if !strings.Contains(out.String(), `"backup_path"`) {
		t.Fatalf("输出应带备份路径：%s", out.String())
	}
}
