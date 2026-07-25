package events

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// DecodeRaw is the single envelope decoder for governed EventMessage values.
// It validates broker metadata, registry membership, rendered subject, and the
// registered protobuf payload type before returning the typed payload.
func DecodeRaw(registry *Registry, raw []byte, subject, messageID, contentType string) (*eventpb.EventMessage, proto.Message, error) {
	if registry == nil {
		return nil, nil, fmt.Errorf("event registry is nil")
	}
	if contentType != ContentType {
		return nil, nil, fmt.Errorf("unexpected event content type %q", contentType)
	}
	message := new(eventpb.EventMessage)
	if err := proto.Unmarshal(raw, message); err != nil {
		return nil, nil, fmt.Errorf("decode event message: %w", err)
	}
	if strings.TrimSpace(message.GetEventId()) == "" || strings.TrimSpace(message.GetEventName()) == "" || message.GetEventVersion() == 0 {
		return message, nil, fmt.Errorf("event message metadata is incomplete")
	}
	if strings.TrimSpace(messageID) == "" || message.GetEventId() != messageID {
		return message, nil, fmt.Errorf("event_id %q does not match NATS message id %q", message.GetEventId(), messageID)
	}
	if message.GetOccurredAt() == nil {
		return message, nil, fmt.Errorf("event message occurred_at is required")
	}
	if len(message.GetPayload()) == 0 {
		return message, nil, fmt.Errorf("event message payload is required")
	}
	if err := message.GetOccurredAt().CheckValid(); err != nil {
		return message, nil, fmt.Errorf("event message occurred_at: %w", err)
	}
	event, ok := registry.Lookup(message.GetEventName(), message.GetEventVersion())
	if !ok {
		return message, nil, fmt.Errorf("event %s is not registered", eventKeyParts(message.GetEventName(), message.GetEventVersion()))
	}
	expected, err := registry.RenderSubject(event, message.GetSpaceId(), message.GetSubjectId())
	if err != nil {
		return message, nil, err
	}
	if subject != expected {
		return message, nil, fmt.Errorf("event subject mismatch: got %q, want %q", subject, expected)
	}
	payload := event.NewPayload()
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		return message, nil, fmt.Errorf("decode %s payload: %w", event.Name(), err)
	}
	if err := event.Validate(message, payload); err != nil {
		return message, nil, fmt.Errorf("validate %s payload: %w", event.Name(), err)
	}
	return message, payload, nil
}

// DecodeDatasetRowsUpserted validates the governed storage envelope and its
// structured delta payload.
func DecodeDatasetRowsUpserted(registry *Registry, raw []byte, subject, messageID string) (*eventpb.EventMessage, *storagepb.DatasetRowsUpserted, error) {
	return DecodeDatasetRowsUpsertedWithContentType(registry, raw, subject, messageID, ContentType)
}

// DecodeDatasetRowsUpsertedWithContentType validates the broker content type
// and the structured storage envelope. Live consumers should pass the NATS
// Content-Type header received with the delivery.
func DecodeDatasetRowsUpsertedWithContentType(registry *Registry, raw []byte, subject, messageID, contentType string) (*eventpb.EventMessage, *storagepb.DatasetRowsUpserted, error) {
	message, payload, err := DecodeRaw(registry, raw, subject, messageID, contentType)
	if err != nil {
		return message, nil, err
	}
	if message.GetEventName() != DatasetRowsUpserted.Name() || message.GetEventVersion() != DatasetRowsUpserted.Version() {
		return message, nil, fmt.Errorf("unexpected storage event name/version")
	}
	storagePayload, ok := payload.(*storagepb.DatasetRowsUpserted)
	if !ok {
		return message, nil, fmt.Errorf("storage event payload has type %T", payload)
	}
	return message, storagePayload, nil
}
