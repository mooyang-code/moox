package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/packages/events"
	_ "modernc.org/sqlite"
)

func TestValidateResetViewConsumersRequiresExplicitConfirmation(t *testing.T) {
	opts := resetViewConsumersOptions{
		stream:   events.StorageViewConsumerStream,
		lookback: time.Hour,
		timeout:  time.Minute,
	}
	if err := validateResetViewConsumersOptions(opts); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected explicit confirmation error, got %v", err)
	}
	opts.dryRun = true
	if err := validateResetViewConsumersOptions(opts); err != nil {
		t.Fatalf("dry-run should not require confirmation: %v", err)
	}
}

func TestStageResetViewIndexesRestoresBothSlots(t *testing.T) {
	root := t.TempDir()
	view := resetViewRecord{SpaceID: "crypto", ViewID: "kline", Engine: "duckdb"}
	for _, slot := range []viewindex.Slot{viewindex.SlotA, viewindex.SlotB} {
		id := viewindex.ViewIndexID(view.SpaceID, view.ViewID, slot)
		path := filepath.Join(root, "duckdb", id+".duckdb")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	staging, moves, err := stageResetViewIndexes(root, []resetViewRecord{view})
	if err != nil {
		t.Fatal(err)
	}
	for _, move := range moves {
		if _, err := os.Stat(move.original); !os.IsNotExist(err) {
			t.Fatalf("original index still exists after staging: %s", move.original)
		}
	}
	if err := restoreResetViewIndexes(moves); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(staging)
	for _, slot := range []viewindex.Slot{viewindex.SlotA, viewindex.SlotB} {
		id := viewindex.ViewIndexID(view.SpaceID, view.ViewID, slot)
		if _, err := os.Stat(filepath.Join(root, "duckdb", id+".duckdb")); err != nil {
			t.Fatalf("restored index missing: %v", err)
		}
	}
}

func TestResetViewsLookbackReadyRequiresActiveCoverage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE t_views (c_status TEXT, c_engine TEXT, c_active_index_id TEXT, c_indexed_from TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t_views VALUES ('active','duckdb','view_a',?)`, time.Now().UTC().Add(-2*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ready, err := resetViewsLookbackReady(context.Background(), dbPath, time.Hour)
	if err != nil || !ready {
		t.Fatalf("covered View ready=%v err=%v", ready, err)
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE t_views SET c_active_index_id = ''`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	ready, err = resetViewsLookbackReady(context.Background(), dbPath, time.Hour)
	if err != nil || ready {
		t.Fatalf("missing active View ready=%v err=%v", ready, err)
	}
}

func TestValidateResetViewConsumersRejectsWrongStreamAndLookback(t *testing.T) {
	opts := resetViewConsumersOptions{
		stream:   "OTHER",
		lookback: time.Hour,
		timeout:  time.Minute,
		yes:      true,
	}
	if err := validateResetViewConsumersOptions(opts); err == nil || !strings.Contains(err.Error(), events.StorageViewConsumerStream) {
		t.Fatalf("expected stream validation error, got %v", err)
	}
	opts.stream = events.StorageViewConsumerStream
	opts.lookback = 0
	if err := validateResetViewConsumersOptions(opts); err == nil || !strings.Contains(err.Error(), "lookback") {
		t.Fatalf("expected lookback validation error, got %v", err)
	}
}

func TestValidateResetPrimaryPathRequiresPackageDataChild(t *testing.T) {
	root := t.TempDir()
	if _, err := validateResetPrimaryPath(filepath.Join(root, "data", "storage-node", "pebble"), root); err != nil {
		t.Fatalf("expected package data path to be allowed: %v", err)
	}
	for _, path := range []string{root, filepath.Dir(root), filepath.Join(root, "config"), filepath.Join(root, "data")} {
		if _, err := validateResetPrimaryPath(path, root); err == nil {
			t.Fatalf("expected unsafe primary path %q to be rejected", path)
		}
	}
}

func TestResetViewsLookbackReadyIgnoresDisabledViews(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t_views (c_status TEXT, c_engine TEXT, c_active_index_id TEXT, c_indexed_from TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t_views VALUES ('disabled','duckdb','','')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ready, err := resetViewsLookbackReady(context.Background(), dbPath, time.Hour)
	if err != nil || !ready {
		t.Fatalf("disabled-only View set should not block reset ready=%v err=%v", ready, err)
	}
}
