package outbox

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
	encoded, err := registry.Encode(events.DatasetPeriodCompleted, validPeriodPayload("dataset"), events.PublishOptions{EventID: "event-1", OccurredAt: time.Now().UTC(), SpaceID: "space", SubjectID: "dataset"})
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
			message.Payload, _ = proto.Marshal(validPeriodPayload("other"))
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

func validPeriodPayload(datasetID string) *storagepb.DatasetPeriodCompleted {
	return &storagepb.DatasetPeriodCompleted{
		DatasetId: datasetID, Frequency: "1m", PeriodTime: 1786032000, Status: "complete", Source: "producer",
		CauseEventId: "cause-1", CompletedAt: timestamppb.New(time.Unix(1786032060, 0)),
	}
}
