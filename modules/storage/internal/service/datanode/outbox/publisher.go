package outbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

// JetStreamPublisher publishes one durable message to the subject owned by a
// Dataset. The transport message id is stable for a DataNode outbox id.
type JetStreamPublisher struct {
	publisher *events.Publisher
	client    *jetstream.Client
}

func (p *JetStreamPublisher) PublishMessage(ctx context.Context, data []byte) error {
	_, err := p.PublishMessageWithAck(ctx, data)
	return err
}

func (p *JetStreamPublisher) PublishMessageWithAck(ctx context.Context, data []byte) (*jetstream.PublishAck, error) {
	if p == nil || p.publisher == nil {
		return nil, errors.New("storage eventbus client is nil")
	}
	_, _, err := validateDatasetEvent(data)
	if err != nil {
		return nil, err
	}
	message := new(eventpb.EventMessage)
	if err := proto.Unmarshal(data, message); err != nil {
		return nil, err
	}
	return p.publisher.PublishMessage(ctx, message)
}

// Reconnect replaces the underlying NATS connection after an EventBus
// restart. The relay invokes this only after repeated publish failures and
// keeps the outbox entry pending until a subsequent publish succeeds.
func (p *JetStreamPublisher) Reconnect(ctx context.Context) error {
	if p == nil || p.client == nil {
		return errors.New("storage eventbus client is nil")
	}
	return p.client.Reconnect(ctx)
}

func (p *JetStreamPublisher) Ready() bool {
	return p != nil && p.client != nil && p.publisher != nil && p.client.Ready()
}

func validateDatasetEvent(data []byte) (string, string, error) {
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(data, message); err != nil {
		return "", "", err
	}
	if message.GetEventId() == "" || message.GetSubjectId() == "" || message.GetSpaceId() == "" {
		return "", "", errors.New("dataset event envelope is incomplete")
	}
	if message.GetEventId() == "outbox-pending" {
		return "", "", errors.New("dataset event cannot use placeholder event id")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return "", "", err
	}
	event, ok := registry.Lookup(message.GetEventName(), message.GetEventVersion())
	if !ok || !dataNodeOutboxEvent(event) {
		return "", "", fmt.Errorf("unsupported DataNode outbox event %s@%d", message.GetEventName(), message.GetEventVersion())
	}
	subject, err := registry.RenderSubject(event, message.GetSpaceId(), message.GetSubjectId())
	if err != nil {
		return "", "", err
	}
	if _, _, err := events.DecodeRaw(registry, data, subject, message.GetEventId(), events.ContentType); err != nil {
		return "", "", fmt.Errorf("validate dataset event: %w", err)
	}
	return subject, message.GetEventId(), nil
}

func dataNodeOutboxEvent(event events.Event) bool {
	for _, allowed := range []events.Event{events.DatasetRowsUpserted, events.DatasetPeriodCollected, events.FactorPeriodComputed, events.DatasetSyncPoint} {
		if event.Name() == allowed.Name() && event.Version() == allowed.Version() {
			return true
		}
	}
	return false
}

func NewJetStreamPublisher(client *jetstream.Client) *JetStreamPublisher {
	if client == nil {
		return &JetStreamPublisher{}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return &JetStreamPublisher{}
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		return &JetStreamPublisher{}
	}
	return &JetStreamPublisher{publisher: publisher, client: client}
}
