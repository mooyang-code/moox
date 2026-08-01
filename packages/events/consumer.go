package events

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

type EventDelivery struct {
	Delivery *jetstream.Delivery
	Message  *eventpb.EventMessage
	Payload  proto.Message
	Err      error
}

// DecodeDelivery 在业务边界解码一条 JetStream 原始投递。
// JetStream 基础层不感知 EventMessage。
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
	consumer *jetstream.Consumer
	registry *Registry
}

type ConsumerConfig struct {
	Name                string
	Event               Event
	AckWait             time.Duration
	MaxDeliver          int
	MaxAckPending       int
	FetchMaxWait        time.Duration
	DeliverPolicy       nats.DeliverPolicy
	DeliverDecodeErrors bool
}

type SubjectConsumerConfig struct {
	ConsumerConfig
	SpaceID   string
	SubjectID string
}

// SpaceConsumerConfig binds every subject route for one event family and one
// space. It deliberately does not consume the same event from another space.
type SpaceConsumerConfig struct {
	ConsumerConfig
	SpaceID string
}

func NewConsumer(ctx context.Context, client *jetstream.Client, registry *Registry, cfg ConsumerConfig) (*Consumer, error) {
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	filter, err := registry.FamilyPattern(cfg.Event)
	if err != nil {
		return nil, err
	}
	return newConsumer(ctx, client, registry, cfg, filter)
}

func NewSpaceConsumer(ctx context.Context, client *jetstream.Client, registry *Registry, cfg SpaceConsumerConfig) (*Consumer, error) {
	filter, err := spaceConsumerFilter(registry, cfg)
	if err != nil {
		return nil, err
	}
	return newConsumer(ctx, client, registry, cfg.ConsumerConfig, filter)
}

func EnsureSubjectConsumer(ctx context.Context, client *jetstream.Client, registry *Registry, cfg SubjectConsumerConfig) (*jetstream.ConsumerInfo, error) {
	filter, err := subjectConsumerFilter(registry, cfg)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("event consumer client is nil")
	}
	return client.EnsureConsumer(ctx, jetstreamConsumerConfig(cfg.ConsumerConfig, filter))
}

func BindSubjectConsumer(ctx context.Context, client *jetstream.Client, registry *Registry, cfg SubjectConsumerConfig) (*Consumer, error) {
	filter, err := subjectConsumerFilter(registry, cfg)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("event consumer client is nil")
	}
	consumer, err := client.BindConsumer(ctx, jetstreamConsumerConfig(cfg.ConsumerConfig, filter))
	if err != nil {
		return nil, err
	}
	return &Consumer{consumer: consumer, registry: registry}, nil
}

func subjectConsumerFilter(registry *Registry, cfg SubjectConsumerConfig) (string, error) {
	if strings.TrimSpace(cfg.SpaceID) == "" {
		return "", fmt.Errorf("event subject consumer space_id is required")
	}
	if strings.TrimSpace(cfg.SubjectID) == "" {
		return "", fmt.Errorf("event subject consumer subject_id is required")
	}
	if err := registry.Validate(); err != nil {
		return "", err
	}
	return registry.RenderSubject(cfg.Event, cfg.SpaceID, cfg.SubjectID)
}

func spaceConsumerFilter(registry *Registry, cfg SpaceConsumerConfig) (string, error) {
	if strings.TrimSpace(cfg.SpaceID) == "" {
		return "", fmt.Errorf("event space consumer space_id is required")
	}
	if err := registry.Validate(); err != nil {
		return "", err
	}
	return registry.SpacePattern(cfg.Event, cfg.SpaceID)
}

func newConsumer(ctx context.Context, client *jetstream.Client, registry *Registry, cfg ConsumerConfig, filter string) (*Consumer, error) {
	if client == nil {
		return nil, fmt.Errorf("event consumer client is nil")
	}
	consumer, err := client.NewConsumer(ctx, jetstreamConsumerConfig(cfg, filter))
	if err != nil {
		return nil, err
	}
	return &Consumer{consumer: consumer, registry: registry}, nil
}

func jetstreamConsumerConfig(cfg ConsumerConfig, filter string) jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Stream: cfg.Event.Stream(), Durable: cfg.Name, FilterSubject: filter,
		AckWait: cfg.AckWait, MaxDeliver: cfg.MaxDeliver, MaxAckPending: cfg.MaxAckPending,
		FetchMaxWait: cfg.FetchMaxWait, DeliverPolicy: cfg.DeliverPolicy,
		DeliverDecodeErrors: cfg.DeliverDecodeErrors,
	}
}

func (c *Consumer) Fetch(ctx context.Context, batch int) ([]*jetstream.Delivery, error) {
	if c == nil || c.consumer == nil || c.registry == nil {
		return nil, fmt.Errorf("event consumer is not initialized")
	}
	return c.consumer.Fetch(ctx, batch)
}

func (c *Consumer) MaxDeliver() int {
	if c == nil || c.consumer == nil {
		return 0
	}
	return c.consumer.MaxDeliver()
}

func (c *Consumer) FetchEvents(ctx context.Context, batch int) ([]*EventDelivery, error) {
	rawDeliveries, fetchErr := c.Fetch(ctx, batch)
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
	if c == nil || c.consumer == nil {
		return nil
	}
	return c.consumer.Close()
}
