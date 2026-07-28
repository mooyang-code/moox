package pebble

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestCleanupExpiredBucketsRemovesOnlyOldTimeSeries(t *testing.T) {
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old := "2026-07-18T00:00:00Z"
	newer := "2026-07-19T00:00:00Z"
	rows := []*pb.RowFieldUpsert{
		{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1d", DataTime: old}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{}}}},
		{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1d", DataTime: newer}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{}}}},
	}
	if err := s.UpsertFields(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.CleanupExpiredBuckets(context.Background(), "s", "d", time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC))
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup deleted=%d err=%v", deleted, err)
	}
}

func TestCleanupExpiredBucketsDoesNotCreateOutboxEvent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	row := &pb.RowFieldUpsert{
		Key: &pb.RowKey{
			SpaceId: "s", DatasetId: "d",
			Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
				SubjectId: "x", Freq: "1d", DataTime: "2026-07-18T00:00:00Z",
			}},
		},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}},
	}
	if _, err := store.UpsertFieldsEvent(ctx, []*pb.RowFieldUpsert{row}, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildDatasetRowsUpsertedMessage("node-1", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
	before, err := store.ListOutbox(ctx, 0, 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("outbox before cleanup = %v, %v", before, err)
	}

	deleted, err := store.CleanupExpiredBuckets(ctx, "s", "d", time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC))
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup deleted=%d err=%v", deleted, err)
	}
	after, err := store.ListOutbox(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("outbox count changed after cleanup: before=%d after=%d", len(before), len(after))
	}
}

func TestCleanupExpiredBucketsIsolatedBySpace(t *testing.T) {
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	row := func(space string) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key:    &pb.RowKey{SpaceId: space, DatasetId: "shared", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1d", DataTime: "2026-07-18T00:00:00Z"}}},
			Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: space}}}},
		}
	}
	if err := s.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row("a"), row("b")}); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.CleanupExpiredBuckets(context.Background(), "a", "shared", time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC))
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup deleted=%d err=%v", deleted, err)
	}
	rows, err := s.ReadFields(context.Background(), []*pb.RowKey{row("b").GetKey()}, []string{"f"}, nil)
	if err != nil || len(rows) != 1 || len(rows[0].GetFields()) != 1 {
		t.Fatalf("space b row removed: rows=%v err=%v", rows, err)
	}
}

func TestUpsertFieldsUpsertsIndependentlyAndReadsOnlyRequestedFields(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1", BucketDuration: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1m", DataTime: "2026-07-19T10:00:00Z"}}}
	row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
	if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	row = &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "volume", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}}}}
	if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
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
	if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"close"}, nil)
	if err != nil || len(rows) != 1 || rows[0].GetFields()[0].GetValue().GetDoubleValue() != 2 {
		t.Fatalf("updated rows=%v err=%v", rows, err)
	}
}

func TestUpsertFieldsExplicitNullReplacesStoredValue(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{
		SpaceId: "s", DatasetId: "d",
		Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: "x", Freq: "1m", DataTime: "2026-07-19T10:00:00Z",
		}},
	}
	write := func(value *pb.TypedValue) {
		t.Helper()
		if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{{
			Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: value}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	write(&pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}})
	write(&pb.TypedValue{Value: &pb.TypedValue_NullValue{NullValue: pb.NullValue_NULL_VALUE_NULL}})

	rows, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"close"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].GetFields()) != 1 {
		t.Fatalf("rows=%v", rows)
	}
	if got := rows[0].GetFields()[0].GetValue().GetNullValue(); got != pb.NullValue_NULL_VALUE_NULL {
		t.Fatalf("null marker = %s; rows=%v", got, rows)
	}
}

func TestHostMetricDimensionsKeepSameTimestampEntitiesDistinct(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	key := func(device string) *pb.RowKey {
		return &pb.RowKey{
			SpaceId:   "moox_system",
			DatasetId: "monitor_host_disk",
			Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
				SubjectId: "agent-1",
				Freq:      "1m",
				DataTime:  "2026-07-26T01:02:00Z",
				Dimensions: map[string]string{
					"device": device,
				},
			}},
		}
	}
	rows := []*pb.RowFieldUpsert{
		{Key: key("disk0"), Fields: []*pb.FieldValue{{FieldId: "device", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "disk0"}}}}},
		{Key: key("disk1"), Fields: []*pb.FieldValue{{FieldId: "device", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "disk1"}}}}},
	}
	if err := store.UpsertFields(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadFields(context.Background(), []*pb.RowKey{key("disk0"), key("disk1")}, []string{"device"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].GetFields()[0].GetValue().GetStringValue() != "disk0" || got[1].GetFields()[0].GetValue().GetStringValue() != "disk1" {
		t.Fatalf("dimensioned rows collided: %v", got)
	}
}

func TestReadFieldsReportsPhysicalRowPresenceWithoutRequestedField(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	present := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "present", Version: "1"}}}
	missing := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "missing", Version: "1"}}}
	if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{{Key: present, Fields: []*pb.FieldValue{{FieldId: "stored", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}); err != nil {
		t.Fatal(err)
	}
	rows, existing, err := store.ReadFieldsWithPresence(context.Background(), []*pb.RowKey{present, missing}, []string{"not_stored"}, nil)
	if err != nil || len(rows) != 2 || len(existing) != 1 || existing[0].GetRecord().GetRecordId() != "present" {
		t.Fatalf("rows=%v existing=%v err=%v", rows, existing, err)
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
		if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
			t.Fatal(err)
		}
	}
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}}}
	rows, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"value"}, nil)
	if err != nil || len(rows) != 1 || rows[0].GetKey().GetRecord().GetVersion() != "2" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}

func TestWriteRejectsEventLargerThanPublisherLimitBeforeCommit(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1", MaxEventBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "value"}}}}}
	if _, err := store.UpsertFieldsEvent(context.Background(), []*pb.RowFieldUpsert{row}, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildDatasetRowsUpsertedMessage("node-1", spaceID, datasetID, rows)
	}); err == nil {
		t.Fatal("expected oversized event to be rejected")
	}
	rows, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"value"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].GetFields()) != 0 {
		t.Fatalf("fact committed despite oversized event: %v", rows)
	}
}
