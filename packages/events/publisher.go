package events

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

const ContentType = "application/vnd.moox.event+protobuf"

type RawPublisher interface {
	PublishRaw(context.Context, string, string, []byte, string) (*jetstream.PublishAck, error)
}

type Publisher struct {
	client   RawPublisher
	registry *Registry
}

func NewPublisher(client RawPublisher, registry *Registry) (*Publisher, error) {
	if client == nil {
		return nil, fmt.Errorf("event publisher client is nil")
	}
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	return &Publisher{client: client, registry: registry}, nil
}

func (p *Publisher) Publish(ctx context.Context, event Event, payload proto.Message, opts PublishOptions) (*jetstream.PublishAck, error) {
	if p == nil || p.client == nil || p.registry == nil {
		return nil, fmt.Errorf("event publisher is not initialized")
	}
	encoded, err := p.registry.Encode(event, payload, opts)
	if err != nil {
		return nil, err
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(encoded.Message)
	if err != nil {
		return nil, fmt.Errorf("marshal event message: %w", err)
	}
	return p.client.PublishRaw(ctx, encoded.Subject, opts.EventID, body, ContentType)
}

// PublishMessage publishes an already encoded EventMessage. Outbox relays use
// this path so the exact bytes committed with local state are sent to NATS.
func (p *Publisher) PublishMessage(ctx context.Context, message *eventpb.EventMessage) (*jetstream.PublishAck, error) {
	if p == nil || p.client == nil || p.registry == nil {
		return nil, fmt.Errorf("event publisher is not initialized")
	}
	subject, err := p.registry.SubjectForMessage(message)
	if err != nil {
		return nil, err
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("marshal event message: %w", err)
	}
	return p.client.PublishRaw(ctx, subject, message.GetEventId(), body, ContentType)
}
