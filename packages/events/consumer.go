package events

import (
	"context"
	"fmt"
	"strings"

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
	rawDeliveries, fetchErr := c.pull.FetchRaw(ctx, batch)
	deliveries := make([]*EventDelivery, 0, len(rawDeliveries))
	var firstErr error
	if fetchErr != nil {
		firstErr = fetchErr
	}
	for _, raw := range rawDeliveries {
		delivery := &EventDelivery{Delivery: raw}
		if raw.ContentType != ContentType {
			delivery.Err = fmt.Errorf("unexpected event content type %q", raw.ContentType)
			deliveries = append(deliveries, delivery)
			continue
		}
		message := new(eventpb.EventMessage)
		if err := proto.Unmarshal(raw.RawData, message); err != nil {
			delivery.Err = fmt.Errorf("decode event message: %w", err)
			deliveries = append(deliveries, delivery)
			continue
		}
		if raw.RawMessageID == "" || message.GetEventId() != raw.RawMessageID {
			delivery.Err = fmt.Errorf("event_id %q does not match NATS message id %q", message.GetEventId(), raw.RawMessageID)
			delivery.Message = message
			deliveries = append(deliveries, delivery)
			continue
		}
		payload, err := c.decode(message, raw.Subject)
		if err != nil {
			delivery.Err = err
			delivery.Message = message
			deliveries = append(deliveries, delivery)
			continue
		}
		delivery.Message = message
		delivery.Payload = payload
		deliveries = append(deliveries, delivery)
	}
	return deliveries, firstErr
}

func (c *Consumer) decode(message *eventpb.EventMessage, subject string) (proto.Message, error) {
	if message == nil {
		return nil, fmt.Errorf("event message is nil")
	}
	if strings.TrimSpace(message.GetEventId()) == "" || strings.TrimSpace(message.GetEventName()) == "" || message.GetEventVersion() == 0 {
		return nil, fmt.Errorf("event message metadata is incomplete")
	}
	if message.GetOccurredAt() == nil {
		return nil, fmt.Errorf("event message occurred_at is required")
	}
	if err := message.GetOccurredAt().CheckValid(); err != nil {
		return nil, fmt.Errorf("event message occurred_at: %w", err)
	}
	event := EventType{Name: message.GetEventName(), Version: message.GetEventVersion()}
	spec, ok := c.registry.Spec(event)
	if !ok {
		return nil, fmt.Errorf("event %s is not registered", eventKey(event))
	}
	template, err := NewSubjectTemplate(spec.Subject)
	if err != nil {
		return nil, err
	}
	expected, err := template.Render(message.GetSpaceId(), message.GetSubjectId())
	if err != nil {
		return nil, err
	}
	if subject != expected {
		return nil, fmt.Errorf("event subject mismatch: got %q, want %q", subject, expected)
	}
	factory, ok := c.registry.PayloadFactory(spec.Payload)
	if !ok {
		return nil, fmt.Errorf("payload %q is not registered", spec.Payload)
	}
	payload := factory()
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		return nil, fmt.Errorf("decode %s payload: %w", spec.Name, err)
	}
	return payload, nil
}

func (c *Consumer) Close() error {
	if c == nil || c.pull == nil {
		return nil
	}
	return c.pull.Close()
}
