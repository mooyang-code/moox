package pebble

import (
	"crypto/sha256"
	"encoding/hex"
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

// BuildDatasetRowsUpsertedMessage creates the new governed EventMessage persisted by
// the DataNode outbox. occurred_at is the event-construction timestamp; the
// outbox write is the commit boundary. The event_id is replaced with the
// Pebble outbox id by BindOutboxID after the write batch has reserved that id.
func BuildDatasetRowsUpsertedMessage(nodeID string, spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
	return buildDatasetRowsUpsertedMessage(nodeID, "", "outbox-pending", spaceID, datasetID, rows)
}

// BuildDatasetRowsUpsertedMessageWithSource persists the upstream write source
// in the public storage change event.
func BuildDatasetRowsUpsertedMessageWithSource(nodeID, writeSource, spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
	return buildDatasetRowsUpsertedMessage(nodeID, writeSource, "outbox-pending", spaceID, datasetID, rows)
}

// BuildDatasetRowsUpsertedMessageForSource derives a stable output ID from the
// source event and write identity. It is used by consumers whose input can be
// redelivered after the local write has already committed.
func BuildDatasetRowsUpsertedMessageForSource(nodeID, sourceEventID, spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
	return BuildDatasetRowsUpsertedMessageForSourceWithWriteSource(nodeID, sourceEventID, "", spaceID, datasetID, rows)
}

// BuildDatasetRowsUpsertedMessageForSourceWithWriteSource preserves the source
// event identity and carries the upstream writer into the outbox payload.
func BuildDatasetRowsUpsertedMessageForSourceWithWriteSource(nodeID, sourceEventID, writeSource, spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
	if sourceEventID == "" {
		return nil, fmt.Errorf("source_event_id is required")
	}
	hash := sha256.Sum256([]byte(sourceEventID + "\x00" + spaceID + "\x00" + datasetID))
	return buildDatasetRowsUpsertedMessage(nodeID, writeSource, "storage-source-"+hex.EncodeToString(hash[:16]), spaceID, datasetID, rows)
}

func buildDatasetRowsUpsertedMessage(nodeID, writeSource, eventID, spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
	if spaceID == "" || datasetID == "" {
		return nil, fmt.Errorf("space_id and dataset_id are required")
	}
	rowPayload, err := eventmapper.ToEventRows(&pb.RowsUpserted{SpaceId: spaceID, DatasetId: datasetID, Rows: rows, WriteSource: writeSource})
	if err != nil {
		return nil, err
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(rowPayload)
	if err != nil {
		return nil, err
	}
	// This is an internal staging envelope, not yet publishable. BindOutboxID
	// assigns its durable position and validates the complete public contract.
	_ = nodeID
	return proto.MarshalOptions{Deterministic: true}.Marshal(&eventpb.EventMessage{EventId: eventID, EventName: events.DatasetRowsUpserted.Name(), EventVersion: events.DatasetRowsUpserted.Version(), SpaceId: spaceID, SubjectId: datasetID, OccurredAt: timestamppb.New(time.Now().UTC()), Payload: payload})
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
	if rows.SourceStoreId != "" {
		if rows.SourceStoreId != storeID || rows.SourceNodeId != nodeID || rows.SourceSequence != outboxID {
			return nil, fmt.Errorf("cannot rebind an assigned outbox source position")
		}
	} else if strings.HasPrefix(eventMessage.EventId, "storage-source-") {
		// Source-derived dedupe IDs must not cross a recreated DataNode store.
		eventMessage.EventId += "-" + storeID
	}
	rows.SourceNodeId, rows.SourceSequence = nodeID, outboxID
	rows.SourceStoreId = storeID
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
