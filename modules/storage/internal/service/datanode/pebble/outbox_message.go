package pebble

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/eventmapper"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// BuildDatasetRowsUpsertedMessage creates the governed EventMessage persisted by
// the DataNode outbox. occurred_at is the event-construction timestamp; the
// outbox write is the commit boundary. The event_id is replaced with the
// Pebble outbox position by BindOutboxID after the write batch has reserved it.
func BuildDatasetRowsUpsertedMessage(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
	if spaceID == "" || datasetID == "" {
		return nil, fmt.Errorf("space_id and dataset_id are required")
	}
	rowPayload, err := eventmapper.ToEventRows(&pb.RowsUpserted{SpaceId: spaceID, DatasetId: datasetID, Rows: rows})
	if err != nil {
		return nil, err
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(rowPayload)
	if err != nil {
		return nil, err
	}
	// This is an internal staging envelope, not yet publishable. BindOutboxID
	// assigns its durable position and validates the complete public contract.
	return proto.MarshalOptions{Deterministic: true}.Marshal(&eventpb.EventMessage{EventId: "outbox-pending", EventName: events.DatasetRowsUpserted.Name(), EventVersion: events.DatasetRowsUpserted.Version(), SpaceId: spaceID, SubjectId: datasetID, OccurredAt: timestamppb.New(time.Now().UTC()), Payload: payload})
}

// BindOutboxID assigns the stable transport id after the atomic Pebble batch
// has reserved its internal outbox id.
func BindOutboxID(data []byte, nodeID, storeID string, outboxID uint64) ([]byte, error) {
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(nodeID) != nodeID || strings.TrimSpace(storeID) == "" || strings.TrimSpace(storeID) != storeID || outboxID == 0 {
		return nil, fmt.Errorf("storage outbox position requires node and positive sequence")
	}
	eventMessage := &eventpb.EventMessage{}
	if err := proto.Unmarshal(data, eventMessage); err != nil {
		return nil, fmt.Errorf("unmarshal storage outbox message %d: %w", outboxID, err)
	}
	token, err := jetstream.EncodeSubjectToken(nodeID)
	if err != nil {
		return nil, err
	}
	if eventMessage.GetEventId() == "outbox-pending" {
		eventMessage.EventId = "storage-" + token + "-" + storeID + "-" + strconv.FormatUint(outboxID, 10)
	}
	rows := &storagepb.DatasetRowsUpserted{}
	if err := proto.Unmarshal(eventMessage.GetPayload(), rows); err != nil {
		return nil, fmt.Errorf("decode outbox rows: %w", err)
	}
	if rows.StoreId != "" {
		if rows.StoreId != storeID || rows.NodeId != nodeID || rows.Sequence != outboxID {
			return nil, fmt.Errorf("cannot rebind an assigned outbox source position")
		}
	}
	rows.NodeId, rows.Sequence = nodeID, outboxID
	rows.StoreId = storeID
	eventMessage.Payload, err = proto.MarshalOptions{Deterministic: true}.Marshal(rows)
	if err != nil {
		return nil, err
	}
	if err := validateDatasetRowsUpsertedEvent(eventMessage, ""); err != nil {
		return nil, fmt.Errorf("validate storage rows.upserted envelope %d: %w", outboxID, err)
	}
	if err := validateNewEventID(eventMessage); err != nil {
		return nil, err
	}
	if err := events.DatasetRowsUpserted.Validate(eventMessage, rows); err != nil {
		return nil, err
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(eventMessage)
}

func validateNewEventID(message *eventpb.EventMessage) error {
	if message == nil || message.GetEventId() == "" {
		return fmt.Errorf("event_id is required")
	}
	if message.GetEventId() == "outbox-pending" {
		return fmt.Errorf("placeholder event_id cannot be published")
	}
	return nil
}

func validateDatasetRowsUpsertedEvent(message *eventpb.EventMessage, subject string) error {
	if message == nil || message.GetEventName() != events.DatasetRowsUpserted.Name() || message.GetEventVersion() != events.DatasetRowsUpserted.Version() {
		return fmt.Errorf("unexpected storage event name/version")
	}
	if message.GetSpaceId() == "" || message.GetSubjectId() == "" || message.GetOccurredAt() == nil || message.GetOccurredAt().CheckValid() != nil {
		return fmt.Errorf("storage event metadata is incomplete")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	expected, err := registry.RenderSubject(events.DatasetRowsUpserted, message.GetSpaceId(), message.GetSubjectId())
	if err != nil {
		return err
	}
	if subject != "" && subject != expected {
		return fmt.Errorf("storage event subject %q does not match %q", subject, expected)
	}
	payload := &storagepb.DatasetRowsUpserted{}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		return fmt.Errorf("decode rows.upserted payload: %w", err)
	}
	if payload.GetSpaceId() != message.GetSpaceId() || payload.GetDatasetId() != message.GetSubjectId() || len(payload.GetRows()) == 0 {
		return fmt.Errorf("rows.upserted payload identity is incomplete")
	}
	return nil
}
