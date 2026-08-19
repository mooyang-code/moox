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
	Name   string
	Event  Event
	Stream string
	// Events binds one durable to multiple governed families in the same Stream.
	Events []Event
	// FilterSubjects binds one durable to an explicit set of exact subjects.
	// It is mutually exclusive with Event/Events and requires Stream.
	FilterSubjects      []string
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
	stream, filters, err := consumerEventFilters(registry, cfg)
	if err != nil {
		return nil, err
	}
	return newConsumer(ctx, client, registry, cfg, stream, filters)
}

func NewSpaceConsumer(ctx context.Context, client *jetstream.Client, registry *Registry, cfg SpaceConsumerConfig) (*Consumer, error) {
	filter, err := spaceConsumerFilter(registry, cfg)
	if err != nil {
		return nil, err
	}
	return newConsumer(ctx, client, registry, cfg.ConsumerConfig, cfg.Event.Stream(), []string{filter})
}

func EnsureSubjectConsumer(ctx context.Context, client *jetstream.Client, registry *Registry, cfg SubjectConsumerConfig) (*jetstream.ConsumerInfo, error) {
	filter, err := subjectConsumerFilter(registry, cfg)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("event consumer client is nil")
	}
	return client.EnsureConsumer(ctx, jetstreamConsumerConfig(cfg.ConsumerConfig, cfg.Event.Stream(), []string{filter}))
}

func BindSubjectConsumer(ctx context.Context, client *jetstream.Client, registry *Registry, cfg SubjectConsumerConfig) (*Consumer, error) {
	filter, err := subjectConsumerFilter(registry, cfg)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("event consumer client is nil")
	}
	consumer, err := client.BindConsumer(ctx, jetstreamConsumerConfig(cfg.ConsumerConfig, cfg.Event.Stream(), []string{filter}))
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
	event, err := singleConsumerEvent(cfg.ConsumerConfig)
	if err != nil {
		return "", err
	}
	return registry.RenderSubject(event, cfg.SpaceID, cfg.SubjectID)
}

func spaceConsumerFilter(registry *Registry, cfg SpaceConsumerConfig) (string, error) {
	if strings.TrimSpace(cfg.SpaceID) == "" {
		return "", fmt.Errorf("event space consumer space_id is required")
	}
	if err := registry.Validate(); err != nil {
		return "", err
	}
	event, err := singleConsumerEvent(cfg.ConsumerConfig)
	if err != nil {
		return "", err
	}
	return registry.SpacePattern(event, cfg.SpaceID)
}

func newConsumer(ctx context.Context, client *jetstream.Client, registry *Registry, cfg ConsumerConfig, stream string, filters []string) (*Consumer, error) {
	if client == nil {
		return nil, fmt.Errorf("event consumer client is nil")
	}
	consumer, err := client.NewConsumer(ctx, jetstreamConsumerConfig(cfg, stream, filters))
	if err != nil {
		return nil, err
	}
	return &Consumer{consumer: consumer, registry: registry}, nil
}

func jetstreamConsumerConfig(cfg ConsumerConfig, stream string, filters []string) jetstream.ConsumerConfig {
	transport := jetstream.ConsumerConfig{
		Stream: stream, Durable: cfg.Name,
		AckWait: cfg.AckWait, MaxDeliver: cfg.MaxDeliver, MaxAckPending: cfg.MaxAckPending,
		FetchMaxWait: cfg.FetchMaxWait, DeliverPolicy: cfg.DeliverPolicy,
		DeliverDecodeErrors: cfg.DeliverDecodeErrors,
	}
	if len(filters) == 1 {
		transport.FilterSubject = filters[0]
	} else {
		transport.FilterSubjects = append([]string(nil), filters...)
	}
	return transport
}

func consumerEventFilters(registry *Registry, cfg ConsumerConfig) (string, []string, error) {
	if err := registry.Validate(); err != nil {
		return "", nil, err
	}
	if len(cfg.FilterSubjects) != 0 {
		if cfg.Event.Name() != "" || len(cfg.Events) != 0 {
			return "", nil, fmt.Errorf("event consumer exact filters cannot be combined with Event/Events")
		}
		stream := strings.TrimSpace(cfg.Stream)
		if stream == "" {
			return "", nil, fmt.Errorf("event consumer exact filters require stream")
		}
		filters := make([]string, 0, len(cfg.FilterSubjects))
		seen := make(map[string]struct{}, len(cfg.FilterSubjects))
		for _, raw := range cfg.FilterSubjects {
			filter := strings.TrimSpace(raw)
			if filter == "" || strings.ContainsAny(filter, ">* ") {
				return "", nil, fmt.Errorf("event consumer exact filter %q is invalid", raw)
			}
			if _, ok := seen[filter]; ok {
				return "", nil, fmt.Errorf("event consumer exact filter %q is duplicated", filter)
			}
			seen[filter] = struct{}{}
			filters = append(filters, filter)
		}
		return stream, filters, nil
	}
	if strings.TrimSpace(cfg.Stream) != "" {
		return "", nil, fmt.Errorf("event consumer stream requires exact filter subjects")
	}
	if cfg.Event.Name() != "" && len(cfg.Events) != 0 {
		return "", nil, fmt.Errorf("event consumer Event and Events are mutually exclusive")
	}
	eventList := cfg.Events
	if cfg.Event.Name() != "" {
		eventList = []Event{cfg.Event}
	}
	if len(eventList) == 0 {
		return "", nil, fmt.Errorf("event consumer requires at least one event")
	}
	stream := eventList[0].Stream()
	seen := make(map[string]struct{}, len(eventList))
	filters := make([]string, 0, len(eventList))
	for _, event := range eventList {
		key := eventKey(event)
		if _, ok := seen[key]; ok {
			return "", nil, fmt.Errorf("event consumer event %s is duplicated", key)
		}
		seen[key] = struct{}{}
		if event.Stream() != stream {
			return "", nil, fmt.Errorf("event consumer events must share one stream")
		}
		filter, err := registry.FamilyPattern(event)
		if err != nil {
			return "", nil, err
		}
		filters = append(filters, filter)
	}
	return stream, filters, nil
}

func singleConsumerEvent(cfg ConsumerConfig) (Event, error) {
	if len(cfg.Events) != 0 {
		return Event{}, fmt.Errorf("space and subject consumers require exactly one Event")
	}
	if cfg.Event.Name() == "" {
		return Event{}, fmt.Errorf("event consumer Event is required")
	}
	return cfg.Event, nil
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
