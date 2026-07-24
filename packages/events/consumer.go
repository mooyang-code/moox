package events

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

type EventDelivery struct {
	Delivery *jetstream.Delivery
	Message  *eventpb.EventMessage
	Payload  proto.Message
	Err      error
}

// DecodeDelivery decodes one raw JetStream delivery at the business boundary.
// JetStream itself deliberately knows nothing about EventMessage.
func DecodeDelivery(registry *Registry, delivery *jetstream.Delivery) *EventDelivery {
	result := &EventDelivery{Delivery: delivery}
	if delivery == nil {
		result.Err = fmt.Errorf("event delivery is nil")
		return result
	}
	message, payload, err := DecodeRaw(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	result.Message = message
	result.Payload = payload
	result.Err = err
	return result
}

type Consumer struct {
	pull     *jetstream.PullConsumer
	registry *Registry
}

func NewConsumer(client *jetstream.Client, ref jetstream.ConsumerBindRef, registry *Registry) (*Consumer, error) {
	if client == nil {
		return nil, fmt.Errorf("event consumer client is nil")
	}
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	pull, err := client.BindManagedPullConsumer(context.Background(), ref)
	if err != nil {
		return nil, err
	}
	return &Consumer{pull: pull, registry: registry}, nil
}

func NewConsumerFromPull(pull *jetstream.PullConsumer, registry *Registry) (*Consumer, error) {
	if pull == nil {
		return nil, fmt.Errorf("event consumer pull consumer is nil")
	}
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	return &Consumer{pull: pull, registry: registry}, nil
}

func (c *Consumer) Fetch(ctx context.Context, batch int) ([]*EventDelivery, error) {
	if c == nil || c.pull == nil || c.registry == nil {
		return nil, fmt.Errorf("event consumer is not initialized")
	}
	rawDeliveries, fetchErr := c.pull.Fetch(ctx, batch)
	deliveries := make([]*EventDelivery, 0, len(rawDeliveries))
	var firstErr error
	if fetchErr != nil {
		firstErr = fetchErr
	}
	for _, raw := range rawDeliveries {
		delivery := DecodeDelivery(c.registry, raw)
		if delivery.Err != nil {
			deliveries = append(deliveries, delivery)
			continue
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, firstErr
}

func (c *Consumer) Close() error {
	if c == nil || c.pull == nil {
		return nil
	}
	return c.pull.Close()
}
