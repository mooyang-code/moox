package pebble

import (
	"fmt"
	"strconv"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	eventstoragepb "github.com/mooyang-code/moox/packages/events/storagepb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

// BuildRowsUpsertedMessage creates the new governed EventMessage persisted by
// the DataNode outbox. The event_id is replaced with the Pebble outbox id by
// BindOutboxID after the write batch has reserved that id.
func BuildRowsUpsertedMessage(nodeID string, spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
	if spaceID == "" || datasetID == "" {
		return nil, fmt.Errorf("space_id and dataset_id are required")
	}
	rowPayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&pb.RowsUpserted{SpaceId: spaceID, DatasetId: datasetID, Rows: rows})
	if err != nil {
		return nil, err
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	encoded, err := registry.Encode(events.StorageRowsUpserted, &eventstoragepb.RowsUpserted{DatasetId: datasetID, Rows: rowPayload}, events.PublishOptions{
		EventID:    "outbox-pending",
		OccurredAt: time.Now().UTC(),
		SpaceID:    spaceID,
		SubjectID:  datasetID,
	})
	if err != nil {
		return nil, err
	}
	// Keep the node in the call signature so callers continue to make the
	// producer identity explicit; stable identity is assigned from outboxID.
	_ = nodeID
	return proto.MarshalOptions{Deterministic: true}.Marshal(encoded.Message)
}

// BindOutboxID assigns the stable transport id after the atomic Pebble batch
// has reserved its internal outbox id.
func BindOutboxID(data []byte, nodeID string, outboxID uint64) ([]byte, error) {
	newEnvelope := &eventpb.EventMessage{}
	if err := proto.Unmarshal(data, newEnvelope); err != nil {
		return nil, fmt.Errorf("unmarshal storage outbox message %d: %w", outboxID, err)
	}
	token, err := jetstream.EncodeSubjectToken(nodeID)
	if err != nil {
		return nil, err
	}
	newEnvelope.EventId = "storage-" + token + "-" + strconv.FormatUint(outboxID, 10)
	if err := validateRowsUpsertedEvent(newEnvelope, ""); err != nil {
		return nil, fmt.Errorf("validate storage rows.upserted envelope %d: %w", outboxID, err)
	}
	if err := validateNewEventID(newEnvelope); err != nil {
		return nil, err
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(newEnvelope)
}

func validateNewEventID(message *eventpb.EventMessage) error {
	if message == nil || message.GetEventId() == "" {
		return fmt.Errorf("event_id is required")
	}
	return nil
}

func validateRowsUpsertedEvent(message *eventpb.EventMessage, subject string) error {
	if message == nil || message.GetEventName() != events.StorageRowsUpserted.Name || message.GetEventVersion() != events.StorageRowsUpserted.Version {
		return fmt.Errorf("unexpected storage event name/version")
	}
	if message.GetSpaceId() == "" || message.GetSubjectId() == "" || message.GetOccurredAt() == nil || message.GetOccurredAt().CheckValid() != nil {
		return fmt.Errorf("storage event metadata is incomplete")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	spec, ok := registry.Spec(events.StorageRowsUpserted)
	if !ok {
		return fmt.Errorf("storage event is not registered")
	}
	template, err := events.NewSubjectTemplate(spec.Subject)
	if err != nil {
		return err
	}
	expected, err := template.Render(message.GetSpaceId(), message.GetSubjectId())
	if err != nil {
		return err
	}
	if subject != "" && subject != expected {
		return fmt.Errorf("storage event subject %q does not match %q", subject, expected)
	}
	payload := &eventstoragepb.RowsUpserted{}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		return fmt.Errorf("decode rows.upserted payload: %w", err)
	}
	if payload.GetDatasetId() != message.GetSubjectId() || len(payload.GetRows()) == 0 {
		return fmt.Errorf("rows.upserted payload identity is incomplete")
	}
	rowEvent := &pb.RowsUpserted{}
	if err := proto.Unmarshal(payload.GetRows(), rowEvent); err != nil {
		return fmt.Errorf("decode rows payload: %w", err)
	}
	if rowEvent.GetSpaceId() != message.GetSpaceId() || rowEvent.GetDatasetId() != message.GetSubjectId() {
		return fmt.Errorf("rows payload identity mismatch")
	}
	return nil
}
