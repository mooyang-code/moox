package pebble

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestDeleteDatasetRowsRemovesRowsAndHistoryWithoutTouchingOtherDataset(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := func(dataset, subject string) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "s", DatasetId: dataset, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, Freq: "1m", DataTime: "2026-08-16T00:00:00Z"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
	}
	rows := []*pb.RowFieldUpsert{row("delete-me", "BTC-USDT"), row("keep-me", "ETH-USDT")}
	if err := store.UpsertFields(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{SpaceId: "s", DatasetId: "delete-me", Page: &pb.Page{Page: 1, Size: 10}}); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeleteDatasetRows(context.Background(), "s", "delete-me"); err != nil || deleted == 0 {
		t.Fatalf("deleted ranges=%d err=%v", deleted, err)
	}
	removed, err := store.ReadFields(context.Background(), []*pb.RowKey{rows[0].GetKey()}, []string{"close"}, nil)
	if err != nil || len(removed) != 1 || len(removed[0].GetFields()) != 0 {
		t.Fatalf("deleted dataset still has fields: rows=%v err=%v", removed, err)
	}
	remaining, err := store.ReadFields(context.Background(), []*pb.RowKey{rows[1].GetKey()}, []string{"close"}, nil)
	if err != nil || len(remaining) != 1 || len(remaining[0].GetFields()) != 1 {
		t.Fatalf("other dataset was removed: rows=%v err=%v", remaining, err)
	}
	history, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{SpaceId: "s", DatasetId: "delete-me", Page: &pb.Page{Page: 1, Size: 10}})
	if err != nil || len(history.GetRows()) != 0 {
		t.Fatalf("deleted dataset still has history: rows=%v err=%v", history, err)
	}
}

func TestDeleteDatasetRowsClearsSourceEventDedupe(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "s", DatasetId: "delete-me", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-16T00:00:00Z"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
	build := func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildDatasetRowsUpsertedMessage("node-1", spaceID, datasetID, rows)
	}
	if entries, err := store.UpsertFieldsEventWithSource(context.Background(), []*pb.RowFieldUpsert{row}, "source-event-1", build); err != nil || len(entries) != 1 {
		t.Fatalf("first source write entries=%d err=%v", len(entries), err)
	}
	if _, err := store.DeleteDatasetRows(context.Background(), "s", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if entries, err := store.UpsertFieldsEventWithSource(context.Background(), []*pb.RowFieldUpsert{row}, "source-event-1", build); err != nil || len(entries) != 1 {
		t.Fatalf("recreated source write entries=%d err=%v", len(entries), err)
	}
}
