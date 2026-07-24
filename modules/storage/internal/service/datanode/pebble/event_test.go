package pebble

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

func TestBuildDatasetRowsUpsertedMessageUsesExplicitOuterContract(t *testing.T) {
	data, err := BuildDatasetRowsUpsertedMessage("node-1", "crypto", "spot_kline", []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "crypto", DatasetId: "spot_kline", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r1", Version: "v1"}}}}})
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
	if message.GetEventId() == "" || message.GetEventName() != events.DatasetRowsUpserted.Name || message.GetEventVersion() != 1 || message.GetSpaceId() != "crypto" || message.GetSubjectId() != "spot_kline" {
		t.Fatalf("outer message = %v", message)
	}
	payload := &storagepb.DatasetRowsUpserted{}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		t.Fatal(err)
	}
	if payload.GetSpaceId() != "crypto" || payload.GetDatasetId() != "spot_kline" || len(payload.GetRows()) != 1 {
		t.Fatalf("rows.upserted payload = %v", payload)
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
		return BuildDatasetRowsUpsertedMessage("foo", spaceID, datasetID, rows)
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

func TestValidateNewEventIDRejectsPlaceholder(t *testing.T) {
	if err := validateNewEventID(&eventpb.EventMessage{EventId: "outbox-pending"}); err == nil {
		t.Fatal("validateNewEventID() error = nil, want placeholder rejection")
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
		return BuildDatasetRowsUpsertedMessage("foo", spaceID, datasetID+"-wrong", rows)
	}); err == nil {
		t.Fatal("WriteFieldsEvent() error = nil, want callback identity rejection")
	}
}

func TestWriteFieldsEventWithSourceIsIdempotentAfterRedelivery(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "foo", DatasetId: "bar", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "5m", DataTime: "2026-07-23T10:00:00Z"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}}}
	build := func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildDatasetRowsUpsertedMessageForSource("foo", "market-kline-1", spaceID, datasetID, rows)
	}
	first, err := store.WriteFieldsEventWithSource(context.Background(), []*pb.RowFieldUpsert{row}, "market-kline-1", build)
	if err != nil || len(first) != 1 {
		t.Fatalf("first source write entries=%v err=%v", first, err)
	}
	second, err := store.WriteFieldsEventWithSource(context.Background(), []*pb.RowFieldUpsert{row}, "market-kline-1", build)
	if err != nil || len(second) != 0 {
		t.Fatalf("redelivery source write entries=%v err=%v", second, err)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox entries=%v err=%v", entries, err)
	}
	message := mustEvent(t, entries[0].Data)
	if message.GetEventId() == "" || message.GetEventId() == "outbox-pending" {
		t.Fatalf("source event id was not persisted: %q", message.GetEventId())
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
