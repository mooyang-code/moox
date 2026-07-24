package eventconsumer

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestValidateDatasetEventRejectsInvalidEnvelopeAndPayload(t *testing.T) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := registry.Encode(events.DatasetRowsUpserted, &storagepb.DatasetRowsUpserted{
		SpaceId: "space", DatasetId: "dataset",
		Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: "record-1", Version: "v1"}}}}},
	}, events.PublishOptions{EventID: "event-1", OccurredAt: time.Now().UTC(), SpaceID: "space", SubjectID: "dataset"})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := proto.Marshal(encoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateDatasetEvent(valid); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*eventpb.EventMessage)
	}{
		{name: "empty payload", mutate: func(message *eventpb.EventMessage) { message.Payload = nil }},
		{name: "invalid occurred at", mutate: func(message *eventpb.EventMessage) { message.OccurredAt = timestamppb.New(time.Unix(1<<62, 0)) }},
		{name: "payload identity mismatch", mutate: func(message *eventpb.EventMessage) {
			message.Payload, _ = proto.Marshal(&storagepb.DatasetRowsUpserted{SpaceId: "other", DatasetId: "dataset", Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{SpaceId: "other", DatasetId: "dataset", Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: "record-1", Version: "v1"}}}}}})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := proto.Clone(encoded.Message).(*eventpb.EventMessage)
			tt.mutate(message)
			raw, err := proto.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := validateDatasetEvent(raw); err == nil {
				t.Fatal("invalid event was accepted")
			}
		})
	}
}
