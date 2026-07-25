package events

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type EventMessage = eventpb.EventMessage

type PublishOptions struct {
	EventID    string
	OccurredAt time.Time
	SpaceID    string
	SubjectID  string
}

type EncodedEvent struct {
	Message *eventpb.EventMessage
	Subject string
	Payload []byte
}

// MarshalMessage creates the exact deterministic EventMessage bytes that an
// outbox stores. Relays must publish these bytes without reconstructing the
// event from topic strings or JSON payloads.
func (r *Registry) MarshalMessage(event Event, payload proto.Message, opts PublishOptions) ([]byte, error) {
	encoded, err := r.Encode(event, payload, opts)
	if err != nil {
		return nil, err
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(encoded.Message)
}

func (r *Registry) UnmarshalMessage(raw []byte) (*eventpb.EventMessage, error) {
	message := new(eventpb.EventMessage)
	if err := proto.Unmarshal(raw, message); err != nil {
		return nil, fmt.Errorf("decode event message: %w", err)
	}
	if _, err := r.ValidateMessage(message); err != nil {
		return nil, err
	}
	return message, nil
}

// ValidateMessage verifies the complete application envelope and its typed
// payload without consulting NATS transport metadata.
func (r *Registry) ValidateMessage(message *eventpb.EventMessage) (Event, error) {
	if r == nil {
		return Event{}, fmt.Errorf("event registry is nil")
	}
	if message == nil {
		return Event{}, fmt.Errorf("event message is nil")
	}
	if strings.TrimSpace(message.GetEventId()) == "" {
		return Event{}, fmt.Errorf("event_id is required")
	}
	if strings.TrimSpace(message.GetEventName()) == "" || message.GetEventVersion() == 0 {
		return Event{}, fmt.Errorf("event name and positive version are required")
	}
	if strings.TrimSpace(message.GetSpaceId()) == "" {
		return Event{}, fmt.Errorf("space_id is required")
	}
	if strings.TrimSpace(message.GetSubjectId()) == "" {
		return Event{}, fmt.Errorf("subject_id is required")
	}
	if message.GetOccurredAt() == nil {
		return Event{}, fmt.Errorf("occurred_at is required")
	}
	if err := message.GetOccurredAt().CheckValid(); err != nil {
		return Event{}, fmt.Errorf("occurred_at: %w", err)
	}
	if len(message.GetPayload()) == 0 {
		return Event{}, fmt.Errorf("payload is required")
	}
	event, ok := r.Lookup(message.GetEventName(), message.GetEventVersion())
	if !ok {
		return Event{}, fmt.Errorf("event %s is not registered", eventKeyParts(message.GetEventName(), message.GetEventVersion()))
	}
	payload := event.NewPayload()
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		return Event{}, fmt.Errorf("decode %s payload: %w", event.Name(), err)
	}
	return event, nil
}

// SubjectForMessage derives the NATS subject only from the governed envelope.
func (r *Registry) SubjectForMessage(message *eventpb.EventMessage) (string, error) {
	event, err := r.ValidateMessage(message)
	if err != nil {
		return "", err
	}
	return r.RenderSubject(event, message.GetSpaceId(), message.GetSubjectId())
}

func (r *Registry) Encode(event Event, payload proto.Message, opts PublishOptions) (EncodedEvent, error) {
	registered, ok := r.Lookup(event.Name(), event.Version())
	if !ok {
		return EncodedEvent{}, fmt.Errorf("event %s is not registered", eventKey(event))
	}
	if payload == nil || payload.ProtoReflect().Descriptor().FullName() != registered.PayloadFullName() {
		return EncodedEvent{}, fmt.Errorf("event %s payload type = %T, want %s", eventKey(event), payload, registered.PayloadFullName())
	}
	if strings.TrimSpace(opts.EventID) == "" {
		return EncodedEvent{}, fmt.Errorf("event_id is required")
	}
	if opts.OccurredAt.IsZero() {
		return EncodedEvent{}, fmt.Errorf("occurred_at is required")
	}
	if strings.TrimSpace(opts.SpaceID) == "" {
		return EncodedEvent{}, fmt.Errorf("space_id is required")
	}
	if strings.TrimSpace(opts.SubjectID) == "" {
		return EncodedEvent{}, fmt.Errorf("subject_id is required")
	}
	rawPayload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(payload)
	if err != nil {
		return EncodedEvent{}, fmt.Errorf("marshal %s payload: %w", eventKey(event), err)
	}
	natsSubject, err := r.RenderSubject(event, opts.SpaceID, opts.SubjectID)
	if err != nil {
		return EncodedEvent{}, err
	}
	message := &eventpb.EventMessage{
		EventId:      opts.EventID,
		EventName:    registered.Name(),
		EventVersion: registered.Version(),
		SpaceId:      opts.SpaceID,
		SubjectId:    opts.SubjectID,
		OccurredAt:   timestamppb.New(opts.OccurredAt.UTC()),
		Payload:      rawPayload,
	}
	if err := message.GetOccurredAt().CheckValid(); err != nil {
		return EncodedEvent{}, fmt.Errorf("occurred_at: %w", err)
	}
	return EncodedEvent{Message: message, Subject: natsSubject, Payload: rawPayload}, nil
}
