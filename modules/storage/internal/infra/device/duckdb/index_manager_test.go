//go:build cgo

package duckdb

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestIndexManagerKeepsViewSlotsInIndependentFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatalf("OpenIndexManager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	schema := viewindex.ViewIndexSchema{
		SpaceID: "crypto", ViewID: "spot_kline", ViewVersion: 1, Engine: "duckdb",
		SchemaHash: "schema-1",
		Columns:    []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	a := viewindex.ViewIndexID("crypto", "spot_kline", viewindex.SlotA)
	b := viewindex.ViewIndexID("crypto", "spot_kline", viewindex.SlotB)
	for _, id := range []string{a, b} {
		if err := manager.Prepare(ctx, id, schema); err != nil {
			t.Fatalf("Prepare(%s): %v", id, err)
		}
	}
	if err := manager.Write(ctx, a, viewindex.ViewIndexBatch{TimeSeriesRows: []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-10T00:00:00Z", duckDBTestValue("close", 100)),
	}}); err != nil {
		t.Fatalf("Write(a): %v", err)
	}

	statA, err := manager.Stat(ctx, a)
	if err != nil {
		t.Fatalf("Stat(a): %v", err)
	}
	statB, err := manager.Stat(ctx, b)
	if err != nil {
		t.Fatalf("Stat(b): %v", err)
	}
	if statA.EntryCount != 1 || statB.EntryCount != 0 {
		t.Fatalf("slot counts = %d/%d, want 1/0", statA.EntryCount, statB.EntryCount)
	}
	if statA.SchemaHash != "schema-1" || statA.PhysicalBytes == 0 {
		t.Fatalf("slot a stats = %+v", statA)
	}
	for _, id := range []string{a, b} {
		ref, _ := viewindex.ParseViewIndexID(id)
		if _, err := os.Stat(viewindex.DuckDBPath(root, ref)); err != nil {
			t.Fatalf("stat %s file: %v", id, err)
		}
	}

	_, rows, _, err := manager.QueryTimeSeriesRows(ctx, a, &pb.QueryTimeSeriesRowsReq{Page: &pb.Page{Page: 1, Size: 25}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("Query(a) rows=%d err=%v", len(rows), err)
	}
	_, rows, _, err = manager.QueryTimeSeriesRows(ctx, b, &pb.QueryTimeSeriesRowsReq{Page: &pb.Page{Page: 1, Size: 25}})
	if err != nil || len(rows) != 0 {
		t.Fatalf("Query(b) rows=%d err=%v", len(rows), err)
	}
}

func TestIndexManagerRemoveWaitsForReferencesAndDeletesFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatalf("OpenIndexManager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	id := viewindex.ViewIndexID("crypto", "spot_kline", viewindex.SlotA)
	if err := manager.Prepare(ctx, id, viewindex.ViewIndexSchema{Engine: "duckdb", SchemaHash: "schema-1"}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	handle, release, err := manager.acquire(id, false)
	if err != nil || handle == nil {
		t.Fatalf("acquire: handle=%v err=%v", handle, err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.Remove(ctx, id) }()
	select {
	case err := <-done:
		t.Fatalf("Remove returned while reference held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, _, err := manager.acquire(id, false); !errors.Is(err, ErrIndexClosing) {
		t.Fatalf("acquire while removing error = %v, want ErrIndexClosing", err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Remove did not finish after release")
	}
	ref, _ := viewindex.ParseViewIndexID(id)
	if _, err := os.Stat(viewindex.DuckDBPath(root, ref)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed file stat error = %v, want not exist", err)
	}
	stat, err := manager.Stat(ctx, id)
	if err != nil || stat.Exists {
		t.Fatalf("Stat after remove = %+v err=%v", stat, err)
	}
}

func TestIndexManagerConcurrentRemoveClosesOnce(t *testing.T) {
	ctx := context.Background()
	manager, err := OpenIndexManager(IndexManagerOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenIndexManager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	id := viewindex.ViewIndexID("crypto", "spot_kline", viewindex.SlotA)
	if err := manager.Prepare(ctx, id, viewindex.ViewIndexSchema{Engine: "duckdb", SchemaHash: "schema-1"}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, release, err := manager.acquire(id, false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	results := make(chan error, 2)
	go func() { results <- manager.Remove(ctx, id) }()
	go func() { results <- manager.Remove(ctx, id) }()
	time.Sleep(100 * time.Millisecond)
	release()
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent Remove %d: %v", i, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent Remove did not finish")
		}
	}
}

func TestIndexManagerCloseWaitsForReferences(t *testing.T) {
	ctx := context.Background()
	manager, err := OpenIndexManager(IndexManagerOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenIndexManager: %v", err)
	}
	id := viewindex.ViewIndexID("crypto", "spot_kline", viewindex.SlotA)
	if err := manager.Prepare(ctx, id, viewindex.ViewIndexSchema{Engine: "duckdb", SchemaHash: "schema-1"}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, release, err := manager.acquire(id, false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.Close() }()
	select {
	case err := <-done:
		t.Fatalf("Close returned while reference held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish after release")
	}
}

func TestIndexManagerReopensExistingSlot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	id := viewindex.ViewIndexID("crypto", "spot_kline", viewindex.SlotA)
	manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatalf("OpenIndexManager: %v", err)
	}
	if err := manager.Prepare(ctx, id, viewindex.ViewIndexSchema{Engine: "duckdb", SchemaHash: "schema-1"}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := manager.Write(ctx, id, viewindex.ViewIndexBatch{TimeSeriesRows: []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-10T00:00:00Z"),
	}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatalf("reopen manager: %v", err)
	}
	defer reopened.Close()
	stat, err := reopened.Stat(ctx, id)
	if err != nil || stat.EntryCount != 1 || stat.SchemaHash != "schema-1" {
		t.Fatalf("reopened Stat = %+v err=%v", stat, err)
	}
}
