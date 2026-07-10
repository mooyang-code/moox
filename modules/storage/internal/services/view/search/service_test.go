package search

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestBleveViewIndexEngineLifecycle(t *testing.T) {
	ctx := context.Background()
	service := NewService(Options{Root: t.TempDir()})
	indexID := viewindex.ViewIndexID("crypto", "news_view", viewindex.SlotA)
	columns := []*pb.ViewColumn{{
		ColumnName: "title",
		OriginId:   "news.title",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
	}}

	if got := service.Engine(); got != "bleve" {
		t.Fatalf("engine = %q, want bleve", got)
	}
	if err := service.Prepare(ctx, indexID, viewindex.ViewIndexSchema{
		SpaceID: "crypto", ViewID: "news_view", ViewVersion: 3, Engine: "bleve", SchemaHash: "schema-1", Columns: columns,
	}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := os.Stat(service.indexPath(indexID)); err != nil {
		t.Fatalf("prepared index path: %v", err)
	}
	if err := service.Write(ctx, indexID, viewindex.ViewIndexBatch{
		Columns:    columns,
		RecordRows: searchTestRecordRows(),
	}); err != nil {
		t.Fatalf("Write record rows: %v", err)
	}
	if err := service.Write(ctx, indexID, viewindex.ViewIndexBatch{
		TimeSeriesRows: []*pb.TimeSeriesRow{{}},
	}); err == nil {
		t.Fatal("Write time series rows succeeded, want error")
	}

	stats, err := service.Stat(ctx, indexID)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stats.EntryCount != 2 {
		t.Fatalf("entry count = %d, want 2", stats.EntryCount)
	}
	if stats.MinVersion != "" || stats.MaxVersion != "" {
		t.Fatalf("record stats = %+v, want revision metadata without string versions", stats)
	}
	if stats.ViewVersion != 3 || stats.SchemaHash != "schema-1" || stats.PhysicalBytes == 0 {
		t.Fatalf("stats = %+v, want schema and physical bytes", stats)
	}

	if err := service.Remove(ctx, indexID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(service.indexPath(indexID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed index path stat err = %v, want not exist", err)
	}
}

func TestBleveViewIndexListsPhysicalIndexes(t *testing.T) {
	ctx := context.Background()
	service := NewService(Options{Root: t.TempDir()})
	a := viewindex.ViewIndexID("crypto", "news_view", viewindex.SlotA)
	b := viewindex.ViewIndexID("crypto", "news_view", viewindex.SlotB)
	for _, indexID := range []string{a, b} {
		if err := service.Prepare(ctx, indexID, viewindex.ViewIndexSchema{Engine: "bleve", SchemaHash: "schema-1"}); err != nil {
			t.Fatalf("Prepare(%s): %v", indexID, err)
		}
	}

	got, err := service.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := map[string]bool{a: true, b: true}
	for _, indexID := range got {
		delete(want, indexID)
	}
	if len(want) != 0 {
		t.Fatalf("List missing indexes: %v (got %v)", want, got)
	}
}

func TestBleveViewIndexRemoveWaitsForReferences(t *testing.T) {
	ctx := context.Background()
	service := NewService(Options{Root: t.TempDir()})
	indexID := viewindex.ViewIndexID("crypto", "news_view", viewindex.SlotA)
	if err := service.Prepare(ctx, indexID, viewindex.ViewIndexSchema{Engine: "bleve", SchemaHash: "schema-1"}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	index, release, err := service.acquire(indexID, false)
	if err != nil || index == nil {
		t.Fatalf("acquire: index=%v err=%v", index, err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Remove(ctx, indexID) }()
	select {
	case err := <-done:
		t.Fatalf("Remove returned while reference held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, _, err := service.acquire(indexID, false); !errors.Is(err, ErrIndexClosing) {
		t.Fatalf("acquire while closing error = %v", err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Remove did not finish")
	}
}

func TestBleveViewIndexConcurrentRemoveClosesOnce(t *testing.T) {
	ctx := context.Background()
	service := NewService(Options{Root: t.TempDir()})
	t.Cleanup(func() { _ = service.Close() })
	indexID := viewindex.ViewIndexID("crypto", "news_view", viewindex.SlotA)
	if err := service.Prepare(ctx, indexID, viewindex.ViewIndexSchema{Engine: "bleve", SchemaHash: "schema-1"}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, release, err := service.acquire(indexID, false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	results := make(chan error, 2)
	go func() { results <- service.Remove(ctx, indexID) }()
	go func() { results <- service.Remove(ctx, indexID) }()
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

func TestBleveViewIndexCloseWaitsForReferences(t *testing.T) {
	ctx := context.Background()
	service := NewService(Options{Root: t.TempDir()})
	indexID := viewindex.ViewIndexID("crypto", "news_view", viewindex.SlotA)
	if err := service.Prepare(ctx, indexID, viewindex.ViewIndexSchema{Engine: "bleve", SchemaHash: "schema-1"}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, release, err := service.acquire(indexID, false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Close() }()
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

func searchTestRecordRows() []*pb.RecordRow {
	return []*pb.RecordRow{
		searchTestRecordRow("news-1", "2026-07-09T01:00:00Z"),
		searchTestRecordRow("news-2", "2026-07-09T01:01:00Z"),
	}
}

func searchTestRecordRow(recordID string, version string) *pb.RecordRow {
	return &pb.RecordRow{
		Key: &pb.RecordKey{
			SpaceId:   "crypto",
			DatasetId: "news",
			RecordId:  recordID,
		},
		Revision: uint64(len(version)),
		Columns: []*pb.ColumnValue{{
			ColumnName: "title",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "market update"}},
		}},
	}
}
