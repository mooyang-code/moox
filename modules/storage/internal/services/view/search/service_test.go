package search

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestBleveViewIndexEngineLifecycle(t *testing.T) {
	ctx := context.Background()
	service := NewService(Options{Root: t.TempDir()})
	indexID := "view_crypto_news_view_a"
	columns := []*pb.ViewColumn{{
		ColumnName: "title",
		OriginId:   "news.title",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
	}}

	if got := service.Engine(); got != "bleve" {
		t.Fatalf("engine = %q, want bleve", got)
	}
	if err := service.Prepare(ctx, indexID, viewindex.ViewIndexSchema{Columns: columns}); err != nil {
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
	if stats.MinVersion != "2026-07-09T01:00:00Z" {
		t.Fatalf("min version = %q, want 2026-07-09T01:00:00Z", stats.MinVersion)
	}
	if stats.MaxVersion != "2026-07-09T01:01:00Z" {
		t.Fatalf("max version = %q, want 2026-07-09T01:01:00Z", stats.MaxVersion)
	}

	if err := service.Remove(ctx, indexID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(service.indexPath(indexID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed index path stat err = %v, want not exist", err)
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
			Version:   version,
		},
		Columns: []*pb.ColumnValue{{
			ColumnName: "title",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "market update"}},
		}},
	}
}
