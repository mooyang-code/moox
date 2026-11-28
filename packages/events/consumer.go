package events

import (
	"context"
	"fmt"
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

// ConsumerConfig 是一个持久消费者的配置：直接收拓扑里声明的 DurableConsumer，不再拆开传 Name 与 Event。
type ConsumerConfig struct {
	Consumer            DurableConsumer
	AckWait             time.Duration
	MaxDeliver          int
	MaxAckPending       int
	FetchMaxWait        time.Duration
	DeliverPolicy       nats.DeliverPolicy
	DeliverDecodeErrors bool
}

func NewConsumer(ctx context.Context, client *jetstream.Client, registry *Registry, cfg ConsumerConfig) (*Consumer, error) {
	filters, err := consumerEventFilters(registry, cfg)
	if err != nil {
		return nil, err
	}
	return newConsumer(ctx, client, registry, cfg, filters)
}

// consumerEventFilters 校验 DurableConsumer 并返回其订阅事件全部主题族的过滤模式。
func consumerEventFilters(registry *Registry, cfg ConsumerConfig) ([]string, error) {
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	if cfg.Consumer.Name() == "" {
		return nil, fmt.Errorf("事件消费者缺少 durable 名称")
	}
	consumerEvents := cfg.Consumer.Events()
	if len(consumerEvents) == 0 {
		return nil, fmt.Errorf("事件消费者缺少事件声明")
	}
	stream := consumerEvents[0].Stream()
	if stream == "" {
		return nil, fmt.Errorf("事件消费者缺少所属 Stream")
	}
	filters := make([]string, 0, len(consumerEvents))
	for _, ev := range consumerEvents {
		if ev.Stream() != stream {
			return nil, fmt.Errorf("事件消费者声明的事件不属于同一个 Stream: %s vs %s", stream, ev.Stream())
		}
		pattern, err := registry.FamilyPattern(ev)
		if err != nil {
			return nil, err
		}
		filters = append(filters, pattern)
	}
	return filters, nil
}

func newConsumer(ctx context.Context, client *jetstream.Client, registry *Registry, cfg ConsumerConfig, filters []string) (*Consumer, error) {
	if client == nil {
		return nil, fmt.Errorf("event consumer client is nil")
	}
	consumer, err := client.NewConsumer(ctx, jetstreamConsumerConfig(cfg, filters))
	if err != nil {
		return nil, err
	}
	return &Consumer{consumer: consumer, registry: registry}, nil
}

func jetstreamConsumerConfig(cfg ConsumerConfig, filters []string) jetstream.ConsumerConfig {
	transport := jetstream.ConsumerConfig{
		Stream: cfg.Consumer.Stream(), Durable: cfg.Consumer.Name(),
		AckWait: cfg.AckWait, MaxDeliver: cfg.MaxDeliver, MaxAckPending: cfg.MaxAckPending,
		FetchMaxWait: cfg.FetchMaxWait, DeliverPolicy: cfg.DeliverPolicy,
		DeliverDecodeErrors: cfg.DeliverDecodeErrors,
	}
	if len(filters) == 1 {
		transport.FilterSubject = filters[0]
	} else {
		transport.FilterSubjects = filters
	}
	return transport
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

func (c *Consumer) Close() error {
	if c == nil || c.consumer == nil {
		return nil
	}
	return c.consumer.Close()
}
