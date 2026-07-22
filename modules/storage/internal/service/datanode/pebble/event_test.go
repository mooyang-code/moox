package pebble

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
)

func TestBuildDatasetFieldsChangedMessageUsesFieldsChangedEnvelopeContract(t *testing.T) {
	data, err := BuildDatasetFieldsChangedMessage("foo", "foo", "bar", []*pb.RowFieldUpsert{{}})
	if err != nil {
		t.Fatal(err)
	}
	data, err = BindOutboxID(data, "foo", 42)
	if err != nil {
		t.Fatal(err)
	}

	message := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(data, message); err != nil {
		t.Fatal(err)
	}
	if err := jetstream.ValidateOutboxMessage(message, 1024); err != nil {
		t.Fatalf("ValidateOutboxMessage() error = %v", err)
	}
	if message.GetTopic() != "moox.storage.fields_changed.v1.mzxw6.mjqxe" {
		t.Fatalf("topic = %q, want fields_changed subject with encoded space and dataset", message.GetTopic())
	}
	if message.GetMessageId() != "storage-mzxw6-42" {
		t.Fatalf("message id = %q", message.GetMessageId())
	}
	if message.GetProducer().GetServiceName() != "storage-node" || message.GetProducer().GetInstanceId() != "foo" || message.GetProducer().GetNodeId() != "foo" {
		t.Fatalf("producer = %+v", message.GetProducer())
	}
	if message.GetOccurredAt() == nil || message.GetOccurredAt().CheckValid() != nil || message.GetOccurredAt().AsTime().Location() != time.UTC {
		t.Fatalf("occurred_at = %v", message.GetOccurredAt())
	}
	if message.GetPublishedAt() != nil {
		t.Fatalf("published_at = %v, want unset before relay", message.GetPublishedAt())
	}
	if message.GetContentType() != jetstream.StorageFieldsChangedContentType {
		t.Fatalf("content type = %q", message.GetContentType())
	}
	if message.GetMessageType() != jetstream.StorageFieldsChangedMessageType {
		t.Fatalf("message type = %q", message.GetMessageType())
	}
	payload := &pb.DatasetFieldsChanged{}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		t.Fatal(err)
	}
	if payload.GetSpaceId() != "foo" || payload.GetDatasetId() != "bar" || len(payload.GetRows()) != 1 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestPrepareOutboxPublicationCompletesAndPersistsEnvelope(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "foo", DatasetId: "bar", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	if _, err := store.WriteFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildDatasetFieldsChangedMessage("foo", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
	before, err := store.ListOutbox(context.Background(), 0, 1)
	if err != nil || len(before) != 1 {
		t.Fatalf("ListOutbox() = %v, %v", before, err)
	}
	var beforeMessage messagepb.MooxMessage
	if err := proto.Unmarshal(before[0].Data, &beforeMessage); err != nil {
		t.Fatal(err)
	}
	if beforeMessage.GetPublishedAt() != nil {
		t.Fatal("outbox entry has published_at before publication")
	}
	occurredAt := beforeMessage.GetOccurredAt()
	payload := append([]byte(nil), beforeMessage.GetPayload()...)
	publicationTime := time.Date(2026, 7, 22, 1, 2, 3, 4, time.UTC)
	data, err := store.PrepareOutboxPublication(context.Background(), before[0].ID, publicationTime)
	if err != nil {
		t.Fatal(err)
	}
	message := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(data, message); err != nil {
		t.Fatal(err)
	}
	if err := jetstream.ValidateMessage(message, 1024); err != nil {
		t.Fatalf("ValidateMessage() error = %v", err)
	}
	if message.GetTopic() != "moox.storage.fields_changed.v1.mzxw6.mjqxe" || message.GetContentType() != jetstream.StorageFieldsChangedContentType || message.GetMessageType() != jetstream.StorageFieldsChangedMessageType || message.GetMessageId() != "storage-mzxw6-1" {
		t.Fatalf("final envelope contract = topic %q content_type %q message_type %q message_id %q", message.GetTopic(), message.GetContentType(), message.GetMessageType(), message.GetMessageId())
	}
	if message.GetProducer().GetInstanceId() != "foo" {
		t.Fatalf("producer instance_id = %q", message.GetProducer().GetInstanceId())
	}
	if !message.GetPublishedAt().AsTime().Equal(publicationTime) || message.GetPublishedAt().AsTime().Location() != time.UTC {
		t.Fatalf("published_at = %v, want %v UTC", message.GetPublishedAt(), publicationTime)
	}
	if !proto.Equal(message.GetOccurredAt(), occurredAt) {
		t.Fatalf("occurred_at changed: %v vs %v", message.GetOccurredAt(), occurredAt)
	}
	if !bytes.Equal(message.GetPayload(), payload) {
		t.Fatal("payload changed during publication preparation")
	}
	second, err := store.PrepareOutboxPublication(context.Background(), before[0].ID, publicationTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, second) {
		t.Fatal("retry changed persisted publication bytes")
	}
}

func TestBindOutboxIDRejectsInvalidEnvelope(t *testing.T) {
	if _, err := BindOutboxID([]byte("not-protobuf"), "foo", 42); err == nil {
		t.Fatal("BindOutboxID() error = nil, want invalid envelope rejection")
	}
}

func TestBindOutboxIDRejectsInvalidFieldsChangedContract(t *testing.T) {
	data, err := BuildDatasetFieldsChangedMessage("foo", "foo", "bar", []*pb.RowFieldUpsert{{}})
	if err != nil {
		t.Fatal(err)
	}
	base := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(data, base); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*messagepb.MooxMessage)
	}{
		{name: "kind", mutate: func(message *messagepb.MooxMessage) {
			message.Kind = messagepb.MessageKind_MESSAGE_KIND_COMMAND
		}},
		{name: "content type", mutate: func(message *messagepb.MooxMessage) {
			message.ContentType = "application/x-protobuf; message=other.Message"
		}},
		{name: "message type", mutate: func(message *messagepb.MooxMessage) {
			message.MessageType = "moox.storage.other.v1"
		}},
		{name: "topic and payload mismatch", mutate: func(message *messagepb.MooxMessage) {
			message.Topic = "moox.storage.fields_changed.v1.mzxw6.b3RoZXI"
		}},
		{name: "message and topic mismatch", mutate: func(message *messagepb.MooxMessage) {
			message.SpaceId = "other"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := proto.Clone(base).(*messagepb.MooxMessage)
			test.mutate(message)
			data, err := proto.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := BindOutboxID(data, "foo", 42); err == nil {
				t.Fatal("BindOutboxID() error = nil, want fields_changed contract rejection")
			}
		})
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
		return BuildDatasetFieldsChangedMessage("foo", spaceID, datasetID+"-wrong", rows)
	}); err == nil {
		t.Fatal("WriteFieldsEvent() error = nil, want callback identity rejection")
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("outbox entries = %d, want 0 after rejected callback", len(entries))
	}
}
