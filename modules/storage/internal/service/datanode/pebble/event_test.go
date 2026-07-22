package pebble

import (
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
	if err := jetstream.ValidateMessage(message, 1024); err != nil {
		t.Fatalf("ValidateMessage() error = %v", err)
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
	if message.GetPublishedAt() == nil || message.GetPublishedAt().CheckValid() != nil || message.GetPublishedAt().AsTime().Location() != time.UTC {
		t.Fatalf("published_at = %v", message.GetPublishedAt())
	}
	if message.GetContentType() != "application/x-protobuf; message=trpc.moox.storage.DatasetFieldsChanged" {
		t.Fatalf("content type = %q", message.GetContentType())
	}
	if message.GetMessageType() != "moox.storage.fields_changed.v1" {
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

func TestBindOutboxIDRejectsInvalidEnvelope(t *testing.T) {
	if _, err := BindOutboxID([]byte("not-protobuf"), "foo", 42); err == nil {
		t.Fatal("BindOutboxID() error = nil, want invalid envelope rejection")
	}
}
