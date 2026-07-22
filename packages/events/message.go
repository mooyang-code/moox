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

func (r *Registry) Encode(event EventType, payload proto.Message, opts PublishOptions) (EncodedEvent, error) {
	spec, ok := r.Spec(event)
	if !ok {
		return EncodedEvent{}, fmt.Errorf("event %s is not registered", eventKey(event))
	}
	if payload == nil || payload.ProtoReflect().Descriptor().FullName() != spec.Payload {
		return EncodedEvent{}, fmt.Errorf("event %s payload type = %T, want %s", eventKey(event), payload, spec.Payload)
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
	subject, err := NewSubjectTemplate(spec.Subject)
	if err != nil {
		return EncodedEvent{}, err
	}
	natsSubject, err := subject.Render(opts.SpaceID, opts.SubjectID)
	if err != nil {
		return EncodedEvent{}, err
	}
	message := &eventpb.EventMessage{
		EventId:      opts.EventID,
		EventName:    spec.Name,
		EventVersion: spec.Version,
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
