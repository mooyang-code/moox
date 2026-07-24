package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
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
	Now        func() time.Time
}

type EventPublisher interface {
	Publish(context.Context, events.EventType, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error)
}

func (p *JetStreamPublisher) Publish(ctx context.Context, row domain.OutboxMessage) error {
	if p == nil || p.Publisher == nil {
		return errors.New("strategy JetStream publisher is unavailable")
	}
	var values map[string]any
	if len(row.Payload) == 0 {
		values = map[string]any{}
	} else if err := json.Unmarshal(row.Payload, &values); err != nil {
		return fmt.Errorf("decode strategy outbox payload: %w", err)
	}
	if values["space_id"] == nil {
		values["space_id"] = "moox_system"
	}
	if values["strategy_id"] == nil {
		values["strategy_id"] = p.InstanceID
	}
	spaceID, _ := values["space_id"].(string)
	subjectID, _ := values["strategy_id"].(string)
	if strings.TrimSpace(subjectID) == "" {
		subjectID = p.InstanceID
	}
	payload, err := structpb.NewStruct(values)
	if err != nil {
		return fmt.Errorf("build strategy event payload: %w", err)
	}
	event := events.StrategyOutputAccepted
	occurredAt := row.CreatedAt.UTC()
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
		if p.Now != nil {
			occurredAt = p.Now().UTC()
		}
	}
	_, err = p.Publisher.Publish(ctx, event, payload, events.PublishOptions{EventID: row.MessageID, OccurredAt: occurredAt, SpaceID: spaceID, SubjectID: subjectID})
	return err
}
