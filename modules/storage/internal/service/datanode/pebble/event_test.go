package pebble

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	eventstoragepb "github.com/mooyang-code/moox/packages/events/storagepb"
	"google.golang.org/protobuf/proto"
)

func TestBuildRowsUpsertedMessageUsesExplicitOuterContract(t *testing.T) {
	data, err := BuildRowsUpsertedMessage("node-1", "crypto", "spot_kline", []*pb.RowFieldUpsert{{}})
	if err != nil {
		t.Fatal(err)
	}
	data, err = BindOutboxID(data, "node-1", 7)
	if err != nil {
		t.Fatal(err)
	}
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(data, message); err != nil {
		t.Fatal(err)
	}
	if message.GetEventId() == "" || message.GetEventName() != events.StorageRowsUpserted.Name || message.GetEventVersion() != 1 || message.GetSpaceId() != "crypto" || message.GetSubjectId() != "spot_kline" {
		t.Fatalf("outer message = %v", message)
	}
	payload := &eventstoragepb.RowsUpserted{}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		t.Fatal(err)
	}
	rows := &pb.RowsUpserted{}
	if err := proto.Unmarshal(payload.GetRows(), rows); err != nil {
		t.Fatal(err)
	}
	if payload.GetDatasetId() != "spot_kline" || rows.GetSpaceId() != "crypto" || rows.GetDatasetId() != "spot_kline" || len(rows.GetRows()) != 1 {
		t.Fatalf("rows.upserted payload = %v / %v", payload, rows)
	}
}

func TestPrepareOutboxPublicationIsByteStable(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "foo", DatasetId: "bar", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	if _, err := store.WriteFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildRowsUpsertedMessage("foo", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 1)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListOutbox() = %v, %v", entries, err)
	}
	first, err := store.PrepareOutboxPublication(context.Background(), entries[0].ID, nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PrepareOutboxPublication(context.Background(), entries[0].ID, nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(mustEvent(t, first), mustEvent(t, second)) {
		t.Fatal("retry changed persisted EventMessage")
	}
}

func TestBindOutboxIDRejectsInvalidEnvelope(t *testing.T) {
	if _, err := BindOutboxID([]byte("not-protobuf"), "foo", 42); err == nil {
		t.Fatal("BindOutboxID() error = nil, want invalid envelope rejection")
	}
}

func TestWriteFieldsEventRejectsCallbackIdentityMismatch(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "foo", DatasetId: "bar", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}
	if _, err := store.WriteFieldsEvent(context.Background(), []*pb.RowFieldUpsert{row}, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildRowsUpsertedMessage("foo", spaceID, datasetID+"-wrong", rows)
	}); err == nil {
		t.Fatal("WriteFieldsEvent() error = nil, want callback identity rejection")
	}
}

func mustEvent(t *testing.T, raw []byte) *eventpb.EventMessage {
	t.Helper()
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(raw, message); err != nil {
		t.Fatal(err)
	}
	return message
}

func nowForTest() time.Time { return time.Now().UTC() }
