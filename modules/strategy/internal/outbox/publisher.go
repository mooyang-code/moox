package outbox

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	strategyStore "github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

func ValidateJetStreamPublisher(_ context.Context, client JetStreamClient, _ string) error {
	if client == nil || !client.Ready() {
		return errors.New("strategy eventbus publisher is not ready")
	}
	return nil
}

type JetStreamClient interface {
	Ready() bool
	Close() error
	EventPublisher() EventPublisher
}

type managedClient struct {
	client    *jetstream.Client
	publisher EventPublisher
}

func NewManagedClient(client *jetstream.Client) (JetStreamClient, error) {
	if client == nil {
		return nil, errors.New("strategy EventBus client is nil")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		return nil, err
	}
	return &managedClient{client: client, publisher: publisher}, nil
}

func (c *managedClient) Ready() bool                    { return c != nil && c.client != nil && c.client.Ready() }
func (c *managedClient) Close() error                   { return c.client.Close() }
func (c *managedClient) EventPublisher() EventPublisher { return c.publisher }

type JetStreamPublisher struct {
	Publisher  EventPublisher
	InstanceID string
}

type EventPublisher interface {
	PublishMessage(context.Context, *eventpb.EventMessage) (*jetstream.PublishAck, error)
}

func (p *JetStreamPublisher) Publish(ctx context.Context, row domain.OutboxMessage) error {
	if p == nil || p.Publisher == nil {
		return errors.New("strategy JetStream publisher is unavailable")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	rawMessage := new(eventpb.EventMessage)
	if err := proto.Unmarshal(row.EventData, rawMessage); err != nil {
		return err
	}
	message, err := registry.UnmarshalMessage(row.EventData)
	if err != nil {
		return err
	}
	if message.GetEventId() != row.MessageID {
		return errors.New("strategy outbox event_id does not match message_id")
	}
	_, err = p.Publisher.PublishMessage(ctx, message)
	return err
}

func (p *JetStreamPublisher) PublishResult(ctx context.Context, row strategyStore.StrategyResult) error {
	return p.Publish(ctx, domain.OutboxMessage{MessageID: row.ResultID, EventData: row.EventData, CreatedAt: row.CreatedAt})
}
