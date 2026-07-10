package eventbus

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	DefaultSubjectPrefix                = "moox.storage"
	DefaultTimeSeriesRowsChangedSubject = "moox.storage.time_series.rows_changed.v1"
	DefaultRecordRowsCommittedSubject   = "moox.storage.record.rows_committed.v1"
	defaultTimeSeriesRowsChangedSuffix  = "time_series.rows_changed.v1"
	defaultRecordRowsCommittedSuffix    = "record.rows_committed.v1"
)

func TimeSeriesRowsChangedSubject(prefix string) string {
	return normalizeSubjectPrefix(prefix) + "." + defaultTimeSeriesRowsChangedSuffix
}

func RecordRowsCommittedSubject(prefix string) string {
	return normalizeSubjectPrefix(prefix) + "." + defaultRecordRowsCommittedSuffix
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
	committedSubject  string
}

func NewProducerBus(producer transport.Producer, prefix string) *ProducerBus {
	return &ProducerBus{
		producer:          producer,
		timeSeriesSubject: TimeSeriesRowsChangedSubject(prefix),
		committedSubject:  RecordRowsCommittedSubject(prefix),
	}
}

func (b *ProducerBus) PublishRecordRowsCommitted(ctx context.Context, event *pb.RecordRowsCommittedEvent) error {
	data, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(event)
	if err != nil {
		return err
	}
	return b.producer.Send(ctx, &transport.Message{Subject: b.committedSubject, Data: data, ID: event.GetEventId(), Time: time.Now()})
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
	committedHandlers      map[uint64]coreeventbus.RecordRowsCommittedHandler
	timeSeriesSubscription transport.Subscription
	committedSubscription  transport.Subscription
	subscribeClosed        bool
}

func NewSubscriberBus(pubsub PubSub, prefix string) *SubscriberBus {
	base := NewProducerBus(pubsub, prefix)
	return &SubscriberBus{
		ProducerBus:        base,
		subscriber:         pubsub,
		timeSeriesHandlers: make(map[uint64]coreeventbus.TimeSeriesRowsChangedHandler),
		committedHandlers:  make(map[uint64]coreeventbus.RecordRowsCommittedHandler),
	}
}

func (b *SubscriberBus) SubscribeRecordRowsCommitted(ctx context.Context, handler coreeventbus.RecordRowsCommittedHandler) (coreeventbus.Subscription, error) {
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	if b.subscribeClosed {
		b.mu.Unlock()
		return nil, context.Canceled
	}
	if b.committedSubscription == nil {
		subscription, err := b.subscriber.Subscribe(ctx, b.committedSubject, b.handleRecordCommittedMessage)
		if err != nil {
			b.mu.Unlock()
			return nil, err
		}
		b.committedSubscription = subscription
	}
	b.nextID++
	id := b.nextID
	b.committedHandlers[id] = handler
	b.mu.Unlock()
	return &subscriberBusSubscription{close: func() error { return b.closeCommittedSubscription(id) }}, nil
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
	var handlerErr error
	for _, handler := range handlers {
		handlerErr = errors.Join(handlerErr, handler(ctx, event))
	}
	return handlerErr
}

func (b *SubscriberBus) handleRecordCommittedMessage(ctx context.Context, msg *transport.Message) error {
	event := &pb.RecordRowsCommittedEvent{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(msg.Data, event); err != nil {
		return err
	}
	b.mu.Lock()
	handlers := make([]coreeventbus.RecordRowsCommittedHandler, 0, len(b.committedHandlers))
	for _, handler := range b.committedHandlers {
		handlers = append(handlers, handler)
	}
	b.mu.Unlock()
	var handlerErr error
	for _, handler := range handlers {
		handlerErr = errors.Join(handlerErr, handler(ctx, event))
	}
	return handlerErr
}

func (b *SubscriberBus) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	timeSeriesSubscription := b.timeSeriesSubscription
	committedSubscription := b.committedSubscription
	b.timeSeriesSubscription = nil
	b.committedSubscription = nil
	b.subscribeClosed = true
	b.timeSeriesHandlers = nil
	b.committedHandlers = nil
	b.mu.Unlock()
	var firstErr error
	if timeSeriesSubscription != nil {
		if err := timeSeriesSubscription.Close(); err != nil {
			firstErr = err
		}
	}
	if committedSubscription != nil {
		if err := committedSubscription.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := b.ProducerBus.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (b *SubscriberBus) closeCommittedSubscription(id uint64) error {
	b.mu.Lock()
	delete(b.committedHandlers, id)
	var subscription transport.Subscription
	if len(b.committedHandlers) == 0 && b.committedSubscription != nil {
		subscription = b.committedSubscription
		b.committedSubscription = nil
	}
	b.mu.Unlock()
	if subscription != nil {
		return subscription.Close()
	}
	return nil
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
