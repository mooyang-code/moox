package eventbus

import (
	"context"
	"errors"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

type TimeSeriesRowsChangedHandler func(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error
type RecordRowsCommittedHandler func(ctx context.Context, event *pb.RecordRowsCommittedEvent) error

// Publisher publishes committed PrimaryStore changes.
type Publisher interface {
	PublishTimeSeriesRowsChanged(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error
	PublishRecordRowsCommitted(ctx context.Context, event *pb.RecordRowsCommittedEvent) error
}

// Subscription 表示一个已建立的事件订阅。
type Subscription interface {
	Close() error
}

// Subscriber consumes PrimaryStore changes.
type Subscriber interface {
	SubscribeTimeSeriesRowsChanged(ctx context.Context, handler TimeSeriesRowsChangedHandler) (Subscription, error)
	SubscribeRecordRowsCommitted(ctx context.Context, handler RecordRowsCommittedHandler) (Subscription, error)
}

// Bus combines capabilities for bootstrap-owned transports.
type Bus interface {
	Publisher
	Subscriber
	Close() error
}

// MemoryBus 是进程内事件总线实现，用于测试和单进程部署。
type MemoryBus struct {
	mu                 sync.Mutex
	closeCond          *sync.Cond
	nextID             uint64
	timeSeriesHandlers map[uint64]TimeSeriesRowsChangedHandler
	committedHandlers  map[uint64]RecordRowsCommittedHandler
	inFlight           int
	closed             bool
}

func NewMemoryBus() *MemoryBus {
	bus := &MemoryBus{
		timeSeriesHandlers: make(map[uint64]TimeSeriesRowsChangedHandler),
		committedHandlers:  make(map[uint64]RecordRowsCommittedHandler),
	}
	bus.closeCond = sync.NewCond(&bus.mu)
	return bus
}

func (b *MemoryBus) SubscribeRecordRowsCommitted(ctx context.Context, handler RecordRowsCommittedHandler) (Subscription, error) {
	_ = ctx
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, context.Canceled
	}
	b.nextID++
	id := b.nextID
	b.committedHandlers[id] = handler
	b.mu.Unlock()
	return &memorySubscription{close: func() { b.deleteCommittedHandler(id) }}, nil
}

func (b *MemoryBus) SubscribeTimeSeriesRowsChanged(ctx context.Context, handler TimeSeriesRowsChangedHandler) (Subscription, error) {
	_ = ctx
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, context.Canceled
	}
	if b.timeSeriesHandlers == nil {
		b.timeSeriesHandlers = make(map[uint64]TimeSeriesRowsChangedHandler)
	}
	b.nextID++
	id := b.nextID
	b.timeSeriesHandlers[id] = handler
	b.mu.Unlock()
	return &memorySubscription{close: func() { b.deleteTimeSeriesHandler(id) }}, nil
}

func (b *MemoryBus) PublishTimeSeriesRowsChanged(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return context.Canceled
	}
	handlers := make([]TimeSeriesRowsChangedHandler, 0, len(b.timeSeriesHandlers))
	for _, handler := range b.timeSeriesHandlers {
		handlers = append(handlers, handler)
	}
	b.inFlight++
	b.mu.Unlock()
	defer b.finishPublish()
	var err error
	for _, handler := range handlers {
		err = errors.Join(err, handler(ctx, cloneTimeSeriesRowsChangedEvent(event)))
	}
	return err
}

func (b *MemoryBus) PublishRecordRowsCommitted(ctx context.Context, event *pb.RecordRowsCommittedEvent) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return context.Canceled
	}
	handlers := make([]RecordRowsCommittedHandler, 0, len(b.committedHandlers))
	for _, handler := range b.committedHandlers {
		handlers = append(handlers, handler)
	}
	b.inFlight++
	b.mu.Unlock()
	defer b.finishPublish()
	var err error
	for _, handler := range handlers {
		err = errors.Join(err, handler(ctx, cloneRecordRowsCommittedEvent(event)))
	}
	return err
}

func (b *MemoryBus) Close() error {
	b.mu.Lock()
	b.closed = true
	b.timeSeriesHandlers = nil
	b.committedHandlers = nil
	for b.inFlight > 0 {
		b.closeCond.Wait()
	}
	b.mu.Unlock()
	return nil
}

func (b *MemoryBus) finishPublish() {
	b.mu.Lock()
	b.inFlight--
	if b.inFlight == 0 {
		b.closeCond.Broadcast()
	}
	b.mu.Unlock()
}

func (b *MemoryBus) deleteTimeSeriesHandler(id uint64) {
	b.mu.Lock()
	delete(b.timeSeriesHandlers, id)
	b.mu.Unlock()
}

func (b *MemoryBus) deleteCommittedHandler(id uint64) {
	b.mu.Lock()
	delete(b.committedHandlers, id)
	b.mu.Unlock()
}

func cloneTimeSeriesRowsChangedEvent(event *pb.TimeSeriesRowsChangedEvent) *pb.TimeSeriesRowsChangedEvent {
	if event == nil {
		return nil
	}
	return proto.Clone(event).(*pb.TimeSeriesRowsChangedEvent)
}

func cloneRecordRowsCommittedEvent(event *pb.RecordRowsCommittedEvent) *pb.RecordRowsCommittedEvent {
	if event == nil {
		return nil
	}
	return proto.Clone(event).(*pb.RecordRowsCommittedEvent)
}

// memorySubscription 表示 MemoryBus 返回的订阅句柄。
type memorySubscription struct {
	close func()
}

func (s *memorySubscription) Close() error {
	if s != nil && s.close != nil {
		s.close()
	}
	return nil
}

// noopSubscription 表示无需关闭动作的空订阅句柄。
type noopSubscription struct{}

func (noopSubscription) Close() error { return nil }
