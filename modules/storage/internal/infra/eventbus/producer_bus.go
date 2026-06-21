package eventbus

import (
	"context"
	"strings"
	"sync"
	"time"

	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	DefaultSubjectPrefix                  = "moox.storage"
	DefaultTimeSeriesRowsChangedSubject   = "moox.storage.time_series.rows_changed.v1"
	DefaultRecordRowsChangedSubject       = "moox.storage.record.rows_changed.v1"
	defaultTimeSeriesRowsChangedSuffix    = "time_series.rows_changed.v1"
	defaultRecordRowsChangedSubjectSuffix = "record.rows_changed.v1"
)

func TimeSeriesRowsChangedSubject(prefix string) string {
	return normalizeSubjectPrefix(prefix) + "." + defaultTimeSeriesRowsChangedSuffix
}

func RecordRowsChangedSubject(prefix string) string {
	return normalizeSubjectPrefix(prefix) + "." + defaultRecordRowsChangedSubjectSuffix
}

func SubjectPrefixWildcard(prefix string) string {
	return normalizeSubjectPrefix(prefix) + ".>"
}

func normalizeSubjectPrefix(prefix string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), ".")
	if prefix == "" {
		return DefaultSubjectPrefix
	}
	return prefix
}

// ProducerBus 将核心事件总线事件发布到外部传输。
type ProducerBus struct {
	producer          transport.Producer
	timeSeriesSubject string
	recordSubject     string
}

func NewProducerBus(producer transport.Producer, prefix string) *ProducerBus {
	return &ProducerBus{
		producer:          producer,
		timeSeriesSubject: TimeSeriesRowsChangedSubject(prefix),
		recordSubject:     RecordRowsChangedSubject(prefix),
	}
}

func (b *ProducerBus) PublishTimeSeriesRowsChanged(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error {
	data, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(event)
	if err != nil {
		return err
	}
	return b.producer.Send(ctx, &transport.Message{
		Subject: b.timeSeriesSubject,
		Data:    data,
		ID:      event.GetEventId(),
		Time:    time.Now(),
	})
}

func (b *ProducerBus) PublishRecordRowsChanged(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
	data, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(event)
	if err != nil {
		return err
	}
	return b.producer.Send(ctx, &transport.Message{
		Subject: b.recordSubject,
		Data:    data,
		ID:      event.GetEventId(),
		Time:    time.Now(),
	})
}

func (b *ProducerBus) Close() error {
	if b == nil || b.producer == nil {
		return nil
	}
	return b.producer.Close()
}

// PubSub 定义同时支持发布和订阅的事件传输接口。
type PubSub interface {
	transport.Producer
	transport.Subscriber
}

// SubscriberBus 将外部传输订阅适配为核心事件总线订阅。
type SubscriberBus struct {
	*ProducerBus
	subscriber             transport.Subscriber
	mu                     sync.Mutex
	nextID                 uint64
	timeSeriesHandlers     map[uint64]coreeventbus.TimeSeriesRowsChangedHandler
	recordHandlers         map[uint64]coreeventbus.RecordRowsChangedHandler
	timeSeriesSubscription transport.Subscription
	recordSubscription     transport.Subscription
	subscribeClosed        bool
}

func NewSubscriberBus(pubsub PubSub, prefix string) *SubscriberBus {
	base := NewProducerBus(pubsub, prefix)
	return &SubscriberBus{
		ProducerBus:        base,
		subscriber:         pubsub,
		timeSeriesHandlers: make(map[uint64]coreeventbus.TimeSeriesRowsChangedHandler),
		recordHandlers:     make(map[uint64]coreeventbus.RecordRowsChangedHandler),
	}
}

func (b *SubscriberBus) SubscribeTimeSeriesRowsChanged(ctx context.Context, handler coreeventbus.TimeSeriesRowsChangedHandler) (coreeventbus.Subscription, error) {
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	if b.subscribeClosed {
		b.mu.Unlock()
		return nil, context.Canceled
	}
	if b.timeSeriesSubscription == nil {
		subscription, err := b.subscriber.Subscribe(ctx, b.timeSeriesSubject, b.handleTimeSeriesMessage)
		if err != nil {
			b.mu.Unlock()
			return nil, err
		}
		b.timeSeriesSubscription = subscription
	}
	b.nextID++
	id := b.nextID
	b.timeSeriesHandlers[id] = handler
	b.mu.Unlock()
	return &subscriberBusSubscription{close: func() error { return b.closeTimeSeriesSubscription(id) }}, nil
}

func (b *SubscriberBus) SubscribeRecordRowsChanged(ctx context.Context, handler coreeventbus.RecordRowsChangedHandler) (coreeventbus.Subscription, error) {
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	if b.subscribeClosed {
		b.mu.Unlock()
		return nil, context.Canceled
	}
	if b.recordSubscription == nil {
		subscription, err := b.subscriber.Subscribe(ctx, b.recordSubject, b.handleRecordMessage)
		if err != nil {
			b.mu.Unlock()
			return nil, err
		}
		b.recordSubscription = subscription
	}
	b.nextID++
	id := b.nextID
	b.recordHandlers[id] = handler
	b.mu.Unlock()
	return &subscriberBusSubscription{close: func() error { return b.closeRecordSubscription(id) }}, nil
}

func (b *SubscriberBus) handleTimeSeriesMessage(ctx context.Context, msg *transport.Message) error {
	event := &pb.TimeSeriesRowsChangedEvent{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(msg.Data, event); err != nil {
		return err
	}
	b.mu.Lock()
	handlers := make([]coreeventbus.TimeSeriesRowsChangedHandler, 0, len(b.timeSeriesHandlers))
	for _, handler := range b.timeSeriesHandlers {
		handlers = append(handlers, handler)
	}
	b.mu.Unlock()
	for _, handler := range handlers {
		if err := handler(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (b *SubscriberBus) handleRecordMessage(ctx context.Context, msg *transport.Message) error {
	event := &pb.RecordRowsChangedEvent{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(msg.Data, event); err != nil {
		return err
	}
	b.mu.Lock()
	handlers := make([]coreeventbus.RecordRowsChangedHandler, 0, len(b.recordHandlers))
	for _, handler := range b.recordHandlers {
		handlers = append(handlers, handler)
	}
	b.mu.Unlock()
	for _, handler := range handlers {
		if err := handler(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (b *SubscriberBus) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	timeSeriesSubscription := b.timeSeriesSubscription
	recordSubscription := b.recordSubscription
	b.timeSeriesSubscription = nil
	b.recordSubscription = nil
	b.subscribeClosed = true
	b.timeSeriesHandlers = nil
	b.recordHandlers = nil
	b.mu.Unlock()
	var firstErr error
	if timeSeriesSubscription != nil {
		if err := timeSeriesSubscription.Close(); err != nil {
			firstErr = err
		}
	}
	if recordSubscription != nil {
		if err := recordSubscription.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := b.ProducerBus.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (b *SubscriberBus) closeTimeSeriesSubscription(id uint64) error {
	b.mu.Lock()
	delete(b.timeSeriesHandlers, id)
	var subscription transport.Subscription
	if len(b.timeSeriesHandlers) == 0 && b.timeSeriesSubscription != nil {
		subscription = b.timeSeriesSubscription
		b.timeSeriesSubscription = nil
	}
	b.mu.Unlock()
	if subscription != nil {
		return subscription.Close()
	}
	return nil
}

func (b *SubscriberBus) closeRecordSubscription(id uint64) error {
	b.mu.Lock()
	delete(b.recordHandlers, id)
	var subscription transport.Subscription
	if len(b.recordHandlers) == 0 && b.recordSubscription != nil {
		subscription = b.recordSubscription
		b.recordSubscription = nil
	}
	b.mu.Unlock()
	if subscription != nil {
		return subscription.Close()
	}
	return nil
}

// subscriberBusSubscription 表示 SubscriberBus 创建的复合订阅句柄。
type subscriberBusSubscription struct {
	close func() error
}

func (s *subscriberBusSubscription) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	return s.close()
}

// noopSubscription 表示无需关闭动作的空订阅句柄。
type noopSubscription struct{}

func (noopSubscription) Close() error { return nil }
