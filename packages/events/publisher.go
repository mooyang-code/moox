package events

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

const ContentType = "application/vnd.moox.event+protobuf"

type Publisher struct {
	client   *jetstream.Client
	registry *Registry
}

func NewPublisher(client *jetstream.Client, registry *Registry) (*Publisher, error) {
	if client == nil {
		return nil, fmt.Errorf("event publisher client is nil")
	}
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	return &Publisher{client: client, registry: registry}, nil
}

func (p *Publisher) Publish(ctx context.Context, event EventType, payload proto.Message, opts PublishOptions) (*jetstream.PublishAck, error) {
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
