package view

import (
	"errors"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/dlqpb"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

func TestBoundedReason(t *testing.T) {
	reason := errors.New(strings.Repeat("x", 2048))
	if got := boundedReason(reason); len(got) != 1024 {
		t.Fatalf("bounded reason length = %d, want 1024", len(got))
	}
	if got := boundedReason(nil); got == "" {
		t.Fatal("nil reason produced an empty DLQ reason")
	}
}

func TestStorageViewDLQPublisherRequiresClient(t *testing.T) {
	if err := storageViewDLQPublisher(nil)(nil, nil, nil); err == nil {
		t.Fatal("storageViewDLQPublisher() error = nil for nil client")
	}
}

func TestBuildStorageViewDLQMessageMatchesContract(t *testing.T) {
	message, err := buildStorageViewDLQMessage(&jetstream.Delivery{Subject: "moox.storage.dataset.rows.upserted.v1.space.dataset", RawMessageID: "event-1", RawData: []byte("event"), DeliveryCount: 10}, errors.New("index unavailable"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	subject, err := registry.SubjectForMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	_, payload, err := events.DecodeRaw(registry, mustMarshalEvent(message), subject, message.GetEventId(), events.ContentType)
	if err != nil {
		t.Fatal(err)
	}
	rejected, ok := payload.(*dlqpb.RejectedMessage)
	if !ok {
		t.Fatalf("payload type = %T", payload)
	}
	if rejected.GetOriginalMessageId() != "event-1" || rejected.GetOriginalSubject() == "" || rejected.GetDeliveryCount() != 10 {
		t.Fatalf("rejected payload = %v", rejected)
	}
}

func mustMarshalEvent(message *eventpb.EventMessage) []byte {
	raw, _ := proto.Marshal(message)
	return raw
}
