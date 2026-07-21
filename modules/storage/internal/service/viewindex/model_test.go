package viewindex

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestViewIndexWriteBatchValidate(t *testing.T) {
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	batch := ViewIndexWriteBatch{
		RowWrites:    []RowWrite{{Key: RowKey{Key: key}, Fields: []*pb.FieldValue{{FieldId: "title", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}},
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: LiveWrite,
	}
	if err := batch.Validate(); err != nil {
		t.Fatal(err)
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

func TestHashViewIndexSchemaChangesWithColumnType(t *testing.T) {
	base := ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "d", Engine: "duckdb", Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}
	changed := base
	changed.Columns = []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}}
	if HashViewIndexSchema(base) == HashViewIndexSchema(changed) {
		t.Fatal("schema hash ignored column type")
	}
}
