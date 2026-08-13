package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestValidateRepairViewRequiresExplicitReplayForFullReset(t *testing.T) {
	opts := &repairViewOptions{
		spaceID:       "space",
		viewID:        "view",
		stream:        defaultRepairStream,
		consumer:      defaultRepairConsumer,
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

func TestForceRepairViewRevisionPreservesActiveIndexAndUpdatesAttrs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE t_views (c_space_id TEXT, c_view_id TEXT, c_desired_view_revision INTEGER, c_attrs_json TEXT, c_mtime TEXT, PRIMARY KEY(c_space_id,c_view_id))`)
	if err != nil {
		t.Fatal(err)
	}
	view := &storagepb.View{
		SpaceId:             "space",
		ViewId:              "view",
		DesiredViewRevision: 3,
		ActiveIndexId:       "view_s7370616365_v76696577_a",
		ActiveViewRevision:  3,
		Columns:             []*storagepb.ViewColumn{{ColumnName: "bias_5"}},
		IndexBuild:          &storagepb.ViewIndexBuild{BuildId: "stale"},
	}
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t_views VALUES (?,?,?,?,?)`, "space", "view", 3, string(raw), ""); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if err := forceRepairViewRevision(context.Background(), dbPath, "space", "view", 4); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var revision uint64
	var attrs string
	if err := db.QueryRow(`SELECT c_desired_view_revision,c_attrs_json FROM t_views WHERE c_space_id='space' AND c_view_id='view'`).Scan(&revision, &attrs); err != nil {
		t.Fatal(err)
	}
	if revision != 4 {
		t.Fatalf("revision = %d, want 4", revision)
	}
	if !json.Valid([]byte(attrs)) {
		t.Fatal("updated attributes are not JSON")
	}
	updated := &storagepb.View{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(attrs), updated); err != nil {
		t.Fatal(err)
	}
	if updated.GetActiveIndexId() != view.GetActiveIndexId() {
		t.Fatalf("active index changed: %q", updated.GetActiveIndexId())
	}
	if updated.GetDesiredViewRevision() != 4 || updated.GetColumns() != nil || updated.GetIndexBuild() != nil {
		t.Fatalf("unexpected rebuild attrs: %+v", updated)
	}
}

func TestPurgeRepairViewIndexesNeverRequiresActivePath(t *testing.T) {
	root := t.TempDir()
	active := "view_s7370616365_v76696577_a"
	inactive := "view_s7370616365_v76696577_b"
	duckRoot := filepath.Join(root, "duckdb")
	if err := os.MkdirAll(duckRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{active, inactive} {
		if err := os.WriteFile(filepath.Join(duckRoot, id+".duckdb"), []byte(id), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := purgeRepairViewIndexes(root, "duckdb", inactive)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed paths = %v, want one inactive file", removed)
	}
	if _, err := os.Stat(filepath.Join(duckRoot, active+".duckdb")); err != nil {
		t.Fatalf("active index was removed: %v", err)
	}
}
