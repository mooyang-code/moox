package bleve

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestBleveViewIndexStatReturnsRecordVersionBounds(t *testing.T) {
	ctx := context.Background()
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "record_view")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer index.Close()

	rows := []*pb.RecordRow{
		bleveTestRecordRow("news-1", "2026-07-09T01:00:00Z"),
		bleveTestRecordRow("news-2", "2026-07-09T01:01:00Z"),
	}
	if err := index.IndexRows(ctx, rows, map[string]bool{"title": true}); err != nil {
		t.Fatalf("IndexRows: %v", err)
	}

	stats, err := index.Stat(ctx)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !stats.Exists {
		t.Fatal("stats exists = false, want true")
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
}

func bleveTestRecordRow(recordID string, version string) *pb.RecordRow {
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
