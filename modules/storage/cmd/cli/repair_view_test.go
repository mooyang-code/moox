package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestValidateRepairViewRequiresExplicitReplayForFullReset(t *testing.T) {
	opts := &repairViewOptions{
		spaceID:       "space",
		viewID:        "view",
		stream:        defaultRepairJSName,
		consumer:      "view_0123456789ab",
		deliverPolicy: "new",
		timeout:       defaultRepairTimeout,
		yes:           true,
		resetView:     true,
	}
	if err := validateRepairViewOptions(opts); err == nil {
		t.Fatal("full View reset with new-only delivery must be rejected")
	}
	opts.deliverPolicy = "all"
	if err := validateRepairViewOptions(opts); err != nil {
		t.Fatal(err)
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

func TestResetRepairViewMetadataClearsRuntimeAndCheckpoints(t *testing.T) {
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
  PRIMARY KEY(c_space_id,c_view_id));
CREATE TABLE t_view_period_dataset_states (c_space_id TEXT, c_view_id TEXT);
CREATE TABLE t_view_sync_points (c_space_id TEXT, c_view_id TEXT);`)
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
	if _, err := db.Exec(`INSERT INTO t_view_period_dataset_states VALUES ('space','view')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t_view_sync_points VALUES ('space','view')`); err != nil {
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
	for _, table := range []string{"t_view_period_dataset_states", "t_view_sync_points"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s still has %d rows", table, count)
		}
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
