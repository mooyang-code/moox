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
	DefaultSubjectPrefix      = "moox.storage"
	DefaultRowsChangedSubject = "moox.storage.fact.rows_changed.v1"
)

func RowsChangedSubject(prefix string) string {
	prefix = normalizeSubjectPrefix(prefix)
	return prefix + ".fact.rows_changed.v1"
}

func SubjectPrefixWildcard(prefix string) string {
	prefix = normalizeSubjectPrefix(prefix)
	return prefix + ".>"
}

func normalizeSubjectPrefix(prefix string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), ".")
	if prefix == "" {
		return DefaultSubjectPrefix
	}
	return prefix
}

type ProducerBus struct {
	producer transport.Producer
	subject  string
}

func NewProducerBus(producer transport.Producer, subject string) *ProducerBus {
	if subject == "" {
		subject = DefaultRowsChangedSubject
	}
	return &ProducerBus{producer: producer, subject: subject}
}

func (b *ProducerBus) PublishRowsChanged(ctx context.Context, event *pb.DataRowsChangedEvent) error {
	data, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(event)
	if err != nil {
		return err
	}
	return b.producer.Send(ctx, &transport.Message{
		Subject: b.subject,
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

type PubSub interface {
	transport.Producer
	transport.Subscriber
}

type SubscriberBus struct {
	*ProducerBus
	subscriber      transport.Subscriber
	mu              sync.Mutex
	nextID          uint64
	handlers        map[uint64]coreeventbus.RowsChangedHandler
	subscription    transport.Subscription
	subscribeClosed bool
}

func NewSubscriberBus(pubsub PubSub, subject string) *SubscriberBus {
	base := NewProducerBus(pubsub, subject)
	return &SubscriberBus{ProducerBus: base, subscriber: pubsub, handlers: make(map[uint64]coreeventbus.RowsChangedHandler)}
}

func (b *SubscriberBus) SubscribeRowsChanged(ctx context.Context, handler coreeventbus.RowsChangedHandler) (coreeventbus.Subscription, error) {
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	if b.subscribeClosed {
		b.mu.Unlock()
		return nil, context.Canceled
	}
	if b.subscription == nil {
		subscription, err := b.subscriber.Subscribe(ctx, b.subject, b.handleMessage)
		if err != nil {
			b.mu.Unlock()
			return nil, err
		}
		b.subscription = subscription
	}
	b.nextID++
	id := b.nextID
	b.handlers[id] = handler
	b.mu.Unlock()
	return &subscriberBusSubscription{bus: b, id: id}, nil
}

func (b *SubscriberBus) handleMessage(ctx context.Context, msg *transport.Message) error {
	event := &pb.DataRowsChangedEvent{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(msg.Data, event); err != nil {
		return err
	}
	b.mu.Lock()
	handlers := make([]coreeventbus.RowsChangedHandler, 0, len(b.handlers))
	for _, handler := range b.handlers {
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
	subscription := b.subscription
	b.subscription = nil
	b.subscribeClosed = true
	b.handlers = nil
	b.mu.Unlock()
	var firstErr error
	if subscription != nil {
		if err := subscription.Close(); err != nil {
			firstErr = err
		}
	}
	if err := b.ProducerBus.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

type subscriberBusSubscription struct {
	bus *SubscriberBus
	id  uint64
}

func (s *subscriberBusSubscription) Close() error {
	if s == nil || s.bus == nil {
		return nil
	}
	s.bus.mu.Lock()
	delete(s.bus.handlers, s.id)
	var subscription transport.Subscription
	if len(s.bus.handlers) == 0 && s.bus.subscription != nil {
		subscription = s.bus.subscription
		s.bus.subscription = nil
	}
	s.bus.mu.Unlock()
	if subscription != nil {
		return subscription.Close()
	}
	return nil
}

type noopSubscription struct{}

func (noopSubscription) Close() error { return nil }
