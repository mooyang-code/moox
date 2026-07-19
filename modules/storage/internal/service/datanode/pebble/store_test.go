package pebble

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestWriteFieldsUpsertsIndependentlyAndReadsOnlyRequestedFields(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1", BucketDuration: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1m", DataTime: "2026-07-19T10:00:00Z"}}}
	row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
	if err := store.WriteFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	row = &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "volume", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}}}}
	if err := store.WriteFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"close", "volume", "missing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].GetFields()) != 2 {
		t.Fatalf("rows=%v", rows)
	}
	row = &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}}}}
	if err := store.WriteFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"close"}, nil)
	if err != nil || len(rows) != 1 || rows[0].GetFields()[0].GetValue().GetDoubleValue() != 2 {
		t.Fatalf("updated rows=%v err=%v", rows, err)
	}
}

func TestRecordEmptyVersionResolvesCharacterMaximum(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, version := range []string{"1", "2", "10"} {
		key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: version}}}
		row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: version}}}}}
		if err := store.WriteFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
			t.Fatal(err)
		}
	}
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}}}
	rows, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"value"}, nil)
	if err != nil || len(rows) != 1 || rows[0].GetKey().GetRecord().GetVersion() != "2" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}
