package events

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// PayloadValidationError distinguishes a decoded but invalid business payload
// from malformed envelope or transport metadata.
type PayloadValidationError struct {
	EventName string
	Cause     error
}

func (e *PayloadValidationError) Error() string {
	return fmt.Sprintf("validate %s payload: %v", e.EventName, e.Cause)
}

func (e *PayloadValidationError) Unwrap() error { return e.Cause }

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
		return message, nil, &PayloadValidationError{EventName: event.Name(), Cause: err}
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
	message, payload, err := decodeStorageEvent(registry, raw, subject, messageID, contentType, DatasetRowsUpserted)
	if err != nil {
		return message, nil, err
	}
	storagePayload, ok := payload.(*storagepb.DatasetRowsUpserted)
	if !ok {
		return message, nil, fmt.Errorf("storage event payload has type %T", payload)
	}
	return message, storagePayload, nil
}

// DecodeDatasetPeriodCollected validates and decodes a Dataset completion marker.
func DecodeDatasetPeriodCollected(registry *Registry, raw []byte, subject, messageID string) (*eventpb.EventMessage, *storagepb.DatasetPeriodCollected, error) {
	return DecodeDatasetPeriodCollectedWithContentType(registry, raw, subject, messageID, ContentType)
}

// DecodeDatasetPeriodCollectedWithContentType also validates broker content type.
func DecodeDatasetPeriodCollectedWithContentType(registry *Registry, raw []byte, subject, messageID, contentType string) (*eventpb.EventMessage, *storagepb.DatasetPeriodCollected, error) {
	message, payload, err := decodeStorageEvent(registry, raw, subject, messageID, contentType, DatasetPeriodCollected)
	if err != nil {
		return message, nil, err
	}
	storagePayload, ok := payload.(*storagepb.DatasetPeriodCollected)
	if !ok {
		return message, nil, fmt.Errorf("dataset period collected payload has type %T", payload)
	}
	return message, storagePayload, nil
}

// DecodeViewSourcePeriodReady validates and decodes a source View readiness event.
func DecodeViewSourcePeriodReady(registry *Registry, raw []byte, subject, messageID string) (*eventpb.EventMessage, *storagepb.ViewSourcePeriodReady, error) {
	return DecodeViewSourcePeriodReadyWithContentType(registry, raw, subject, messageID, ContentType)
}

// DecodeViewSourcePeriodReadyWithContentType also validates broker content type.
func DecodeViewSourcePeriodReadyWithContentType(registry *Registry, raw []byte, subject, messageID, contentType string) (*eventpb.EventMessage, *storagepb.ViewSourcePeriodReady, error) {
	message, payload, err := decodeStorageEvent(registry, raw, subject, messageID, contentType, ViewSourcePeriodReady)
	if err != nil {
		return message, nil, err
	}
	storagePayload, ok := payload.(*storagepb.ViewSourcePeriodReady)
	if !ok {
		return message, nil, fmt.Errorf("view source period ready payload has type %T", payload)
	}
	return message, storagePayload, nil
}

// DecodeFactorPeriodComputed validates and decodes a factor completion marker.
func DecodeFactorPeriodComputed(registry *Registry, raw []byte, subject, messageID string) (*eventpb.EventMessage, *storagepb.FactorPeriodComputed, error) {
	return DecodeFactorPeriodComputedWithContentType(registry, raw, subject, messageID, ContentType)
}

// DecodeFactorPeriodComputedWithContentType also validates broker content type.
func DecodeFactorPeriodComputedWithContentType(registry *Registry, raw []byte, subject, messageID, contentType string) (*eventpb.EventMessage, *storagepb.FactorPeriodComputed, error) {
	message, payload, err := decodeStorageEvent(registry, raw, subject, messageID, contentType, FactorPeriodComputed)
	if err != nil {
		return message, nil, err
	}
	storagePayload, ok := payload.(*storagepb.FactorPeriodComputed)
	if !ok {
		return message, nil, fmt.Errorf("factor period computed payload has type %T", payload)
	}
	return message, storagePayload, nil
}

// DecodeViewFactorPeriodReady validates and decodes a result View readiness event.
func DecodeViewFactorPeriodReady(registry *Registry, raw []byte, subject, messageID string) (*eventpb.EventMessage, *storagepb.ViewFactorPeriodReady, error) {
	return DecodeViewFactorPeriodReadyWithContentType(registry, raw, subject, messageID, ContentType)
}

// DecodeViewFactorPeriodReadyWithContentType also validates broker content type.
func DecodeViewFactorPeriodReadyWithContentType(registry *Registry, raw []byte, subject, messageID, contentType string) (*eventpb.EventMessage, *storagepb.ViewFactorPeriodReady, error) {
	message, payload, err := decodeStorageEvent(registry, raw, subject, messageID, contentType, ViewFactorPeriodReady)
	if err != nil {
		return message, nil, err
	}
	storagePayload, ok := payload.(*storagepb.ViewFactorPeriodReady)
	if !ok {
		return message, nil, fmt.Errorf("view factor period ready payload has type %T", payload)
	}
	return message, storagePayload, nil
}

// DecodeDatasetSyncPoint validates and decodes an ordered Dataset sync point.
func DecodeDatasetSyncPoint(registry *Registry, raw []byte, subject, messageID string) (*eventpb.EventMessage, *storagepb.DatasetSyncPoint, error) {
	return DecodeDatasetSyncPointWithContentType(registry, raw, subject, messageID, ContentType)
}

// DecodeDatasetSyncPointWithContentType also validates broker content type.
func DecodeDatasetSyncPointWithContentType(registry *Registry, raw []byte, subject, messageID, contentType string) (*eventpb.EventMessage, *storagepb.DatasetSyncPoint, error) {
	message, payload, err := decodeStorageEvent(registry, raw, subject, messageID, contentType, DatasetSyncPoint)
	if err != nil {
		return message, nil, err
	}
	storagePayload, ok := payload.(*storagepb.DatasetSyncPoint)
	if !ok {
		return message, nil, fmt.Errorf("dataset sync point payload has type %T", payload)
	}
	return message, storagePayload, nil
}

func decodeStorageEvent(registry *Registry, raw []byte, subject, messageID, contentType string, event Event) (*eventpb.EventMessage, proto.Message, error) {
	message, payload, err := DecodeRaw(registry, raw, subject, messageID, contentType)
	if err != nil {
		return message, nil, err
	}
	if message.GetEventName() != event.Name() || message.GetEventVersion() != event.Version() {
		return message, nil, fmt.Errorf("unexpected storage event name/version")
	}
	return message, payload, nil
}
