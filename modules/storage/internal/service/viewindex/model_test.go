package viewindex

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestMemoryEngineBackfillDoesNotOverwriteLiveValue(t *testing.T) {
	e := NewMemoryEngine("duckdb", t.TempDir())
	schema := ViewIndexSchema{SpaceID: "s", ViewID: "v", ViewVersion: 1, SchemaHash: "h"}
	if err := e.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	write := func(value string, mode WriteMode) error {
		return e.Apply(context.Background(), "idx", ViewIndexApplyBatch{RowWrites: []RowWrite{{Key: RowKey{Key: key}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}}}}}}, ViewRevision: 1, ViewSchemaHash: "h", WriteMode: mode})
	}
	if err := write("live", LiveWrite); err != nil {
		t.Fatal(err)
	}
	if err := write("old", Backfill); err != nil {
		t.Fatal(err)
	}
	rows, err := e.Query(context.Background(), "idx", []*pb.RowKey{key}, []string{"f"})
	if err != nil || len(rows) != 1 || rows[0].GetFields()[0].GetValue().GetStringValue() != "live" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}

func TestViewIndexSlotRoundTrip(t *testing.T) {
	id := ViewIndexID("space", "view", SlotB)
	ref, err := ParseViewIndexID(id)
	if err != nil || ref.SpaceID != "space" || ref.ViewID != "view" || ref.Slot != SlotB {
		t.Fatalf("id=%q ref=%+v err=%v", id, ref, err)
	}
	if got := InactiveViewIndexID("space", "view", id); got != ViewIndexID("space", "view", SlotA) {
		t.Fatalf("inactive=%q", got)
	}
}
