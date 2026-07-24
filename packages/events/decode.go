package events

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/events/tradingpb"
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
	event := EventType{name: message.GetEventName(), version: message.GetEventVersion()}
	spec, ok := registry.Schema(event)
	if !ok {
		return message, nil, fmt.Errorf("event %s is not registered", eventKey(event))
	}
	expected, err := registry.RenderSubject(event, message.GetSpaceId(), message.GetSubjectId())
	if err != nil {
		return message, nil, err
	}
	if subject != expected {
		return message, nil, fmt.Errorf("event subject mismatch: got %q, want %q", subject, expected)
	}
	factory, ok := registry.PayloadFactory(spec.Payload)
	if !ok {
		return message, nil, fmt.Errorf("payload %q is not registered", spec.Payload)
	}
	payload := factory()
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		return message, nil, fmt.Errorf("decode %s payload: %w", spec.Name, err)
	}
	return message, payload, nil
}

// DecodeTradingSignal validates the governed envelope and the signal payload
// identity before a consumer persists the strategy recommendation.
func DecodeTradingSignal(registry *Registry, raw []byte, subject, messageID string) (*eventpb.EventMessage, *tradingpb.TradingSignal, error) {
	return DecodeTradingSignalWithContentType(registry, raw, subject, messageID, ContentType)
}

// DecodeTradingSignalWithContentType validates both the NATS content type and
// the governed TradingSignal envelope. Consumers should pass the broker
// Content-Type header instead of assuming it from the payload type.
func DecodeTradingSignalWithContentType(registry *Registry, raw []byte, subject, messageID, contentType string) (*eventpb.EventMessage, *tradingpb.TradingSignal, error) {
	message, payload, err := DecodeRaw(registry, raw, subject, messageID, contentType)
	if err != nil {
		return message, nil, err
	}
	if message.GetEventName() != TradingSignal.Name() || message.GetEventVersion() != TradingSignal.Version() {
		return message, nil, fmt.Errorf("unexpected trading signal name/version")
	}
	signal, ok := payload.(*tradingpb.TradingSignal)
	if !ok {
		return message, nil, fmt.Errorf("trading signal payload has type %T", payload)
	}
	if err := ValidateTradingSignal(signal); err != nil {
		return message, nil, err
	}
	if signal.GetSymbol() != message.GetSubjectId() {
		return message, nil, fmt.Errorf("trading signal symbol %q does not match subject_id %q", signal.GetSymbol(), message.GetSubjectId())
	}
	return message, signal, nil
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
	if storagePayload.GetSpaceId() == "" || storagePayload.GetSpaceId() != message.GetSpaceId() || storagePayload.GetDatasetId() == "" || storagePayload.GetDatasetId() != message.GetSubjectId() {
		return message, nil, fmt.Errorf("storage event payload identity mismatch")
	}
	if len(storagePayload.GetRows()) == 0 {
		return message, nil, fmt.Errorf("storage event rows payload is empty")
	}
	for i, row := range storagePayload.GetRows() {
		if row == nil || row.GetKey() == nil {
			return message, nil, fmt.Errorf("storage event row %d key is required", i)
		}
		if row.GetKey().GetSpaceId() != storagePayload.GetSpaceId() || row.GetKey().GetDatasetId() != storagePayload.GetDatasetId() {
			return message, nil, fmt.Errorf("storage event row %d identity mismatch", i)
		}
		if err := validateStorageRow(row); err != nil {
			return message, nil, fmt.Errorf("storage event row %d: %w", i, err)
		}
	}
	return message, storagePayload, nil
}

func validateStorageRow(row *storagepb.RowUpsert) error {
	key := row.GetKey()
	switch kind := key.GetKind().(type) {
	case *storagepb.RowKey_TimeSeries:
		series := kind.TimeSeries
		if series == nil || strings.TrimSpace(series.GetSubjectId()) == "" || strings.TrimSpace(series.GetFreq()) == "" || strings.TrimSpace(series.GetDataTime()) == "" {
			return fmt.Errorf("time-series key requires subject_id, freq, and data_time")
		}
		if err := validateStorageTime(series.GetDataTime()); err != nil {
			return fmt.Errorf("time-series data_time: %w", err)
		}
	case *storagepb.RowKey_Record:
		record := kind.Record
		if record == nil || strings.TrimSpace(record.GetRecordId()) == "" || strings.TrimSpace(record.GetVersion()) == "" {
			return fmt.Errorf("record key requires record_id and version")
		}
	default:
		return fmt.Errorf("row key kind is required")
	}
	for i, field := range row.GetFields() {
		if field == nil || strings.TrimSpace(field.GetFieldId()) == "" {
			return fmt.Errorf("field %d requires field_id", i)
		}
		if err := validateStorageValue(field.GetValue()); err != nil {
			return fmt.Errorf("field %d: %w", i, err)
		}
	}
	for name, value := range row.GetAttributes() {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("attribute name is required")
		}
		if err := validateStorageValue(value); err != nil {
			return fmt.Errorf("attribute %q: %w", name, err)
		}
	}
	return nil
}

func validateStorageValue(value *storagepb.TypedValue) error {
	if value == nil || value.GetValue() == nil {
		return fmt.Errorf("typed value is required")
	}
	switch typed := value.GetValue().(type) {
	case *storagepb.TypedValue_DoubleValue:
		if math.IsNaN(typed.DoubleValue) || math.IsInf(typed.DoubleValue, 0) {
			return fmt.Errorf("double value must be finite")
		}
	case *storagepb.TypedValue_TimeValue:
		if err := validateStorageTime(typed.TimeValue); err != nil {
			return fmt.Errorf("time value: %w", err)
		}
	case *storagepb.TypedValue_JsonValue:
		if !json.Valid([]byte(typed.JsonValue)) {
			return fmt.Errorf("json value is invalid")
		}
	case *storagepb.TypedValue_ListValue:
		if typed.ListValue == nil {
			return fmt.Errorf("list value is nil")
		}
		for i, item := range typed.ListValue.GetValues() {
			if err := validateStorageValue(item); err != nil {
				return fmt.Errorf("list item %d: %w", i, err)
			}
		}
	case *storagepb.TypedValue_NullValue:
		if typed.NullValue != storagepb.NullValue_NULL_VALUE_NULL {
			return fmt.Errorf("null value must be explicitly NULL")
		}
	}
	return nil
}

func validateStorageTime(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("time is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
		return err
	}
	return nil
}
