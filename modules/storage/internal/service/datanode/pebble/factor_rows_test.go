package pebble

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestFactorRowsWholeRowUpsertOverwritesNullFields(t *testing.T) {
	store := openFactorRowsStore(t)
	key := factorRowsKey("row-null")
	if _, err := store.WriteFactorRows(context.Background(), "space", "factor_result", "commit-before", []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{doubleField("close", 10), doubleField("ma", 2)}}}); err != nil {
		t.Fatal(err)
	}
	row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{
		doubleField("close", 11),
		{FieldId: "ma", Value: &pb.TypedValue{Value: &pb.TypedValue_NullValue{NullValue: pb.NullValue_NULL_VALUE_NULL}}},
	}}
	if _, err := store.WriteFactorRows(context.Background(), "space", "factor_result", "commit-null", []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"close", "ma"}, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("rows=%v err=%v", got, err)
	}
	fields := make(map[string]*pb.TypedValue)
	for _, field := range got[0].GetFields() {
		fields[field.GetFieldId()] = field.GetValue()
	}
	if fields["close"].GetDoubleValue() != 11 {
		t.Fatalf("close=%v", fields["close"])
	}
	if fields["ma"] == nil || fields["ma"].GetNullValue() != pb.NullValue_NULL_VALUE_NULL {
		t.Fatalf("ma was not overwritten with explicit null: %v", fields["ma"])
	}
}

func TestFactorRowsSameCommitIDIsIdempotent(t *testing.T) {
	store := openFactorRowsStore(t)
	rows := []*pb.RowFieldUpsert{
		{Key: factorRowsKey("row-a"), Fields: []*pb.FieldValue{doubleField("close", 10), doubleField("volume", 2)}},
		{Key: factorRowsKey("row-b"), Fields: []*pb.FieldValue{doubleField("close", 11)}},
	}
	if _, err := store.WriteFactorRows(context.Background(), "space", "factor_result", "commit-factor", rows); err != nil {
		t.Fatal(err)
	}
	reordered := []*pb.RowFieldUpsert{
		{Key: factorRowsKey("row-b"), Fields: []*pb.FieldValue{doubleField("close", 11)}},
		{Key: factorRowsKey("row-a"), Fields: []*pb.FieldValue{doubleField("volume", 2), doubleField("close", 10)}},
	}
	if _, err := store.WriteFactorRows(context.Background(), "space", "factor_result", "commit-factor", reordered); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox entries=%v err=%v", entries, err)
	}
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(entries[0].Data, message); err != nil {
		t.Fatal(err)
	}
	rowsEvent := &storageeventpb.DatasetRowsUpserted{}
	if err := proto.Unmarshal(message.GetPayload(), rowsEvent); err != nil {
		t.Fatal(err)
	}
	if rowsEvent.GetWriteKind() != WriteKindFactorResult {
		t.Fatalf("write_kind=%q", rowsEvent.GetWriteKind())
	}
}

func TestFactorRowsThenMarkerOrdering(t *testing.T) {
	store := openFactorRowsStore(t)
	if _, err := store.WriteFactorRows(context.Background(), "space", "factor_result", "commit-order", []*pb.RowFieldUpsert{{Key: factorRowsKey("row-order"), Fields: []*pb.FieldValue{doubleField("close", 10)}}}); err != nil {
		t.Fatal(err)
	}
	marker := &pb.FactorPeriodComputedMarker{
		DatasetId: "factor_result", SourceDatasetId: "source", Frequency: "1m", PeriodTime: 1,
		Status: "complete", TriggerEventId: "trigger-1", ComputedAt: timestamppb.Now(),
	}
	message, _, err := BuildFactorPeriodComputedMessage("space", marker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendDatasetMarker(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("outbox entries=%v err=%v", entries, err)
	}
	if entries[0].ID >= entries[1].ID {
		t.Fatalf("row event id=%d marker id=%d", entries[0].ID, entries[1].ID)
	}
	first, second := &eventpb.EventMessage{}, &eventpb.EventMessage{}
	if err := proto.Unmarshal(entries[0].Data, first); err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(entries[1].Data, second); err != nil {
		t.Fatal(err)
	}
	if first.GetEventName() != events.DatasetRowsUpserted.Name() || second.GetEventName() != events.FactorPeriodComputed.Name() {
		t.Fatalf("event order=%q, %q", first.GetEventName(), second.GetEventName())
	}
}

func openFactorRowsStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func doubleField(id string, value float64) *pb.FieldValue {
	return &pb.FieldValue{FieldId: id, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}
}

func factorRowsKey(recordID string) *pb.RowKey {
	return &pb.RowKey{SpaceId: "space", DatasetId: "factor_result", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: recordID, Version: "v1"}}}
}
