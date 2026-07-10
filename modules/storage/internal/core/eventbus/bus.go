package eventbus

import (
	"context"
	"errors"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

type TimeSeriesRowsChangedHandler func(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error
type RecordRowsChangedHandler func(ctx context.Context, event *pb.RecordRowsChangedEvent) error

// Publisher publishes committed PrimaryStore changes.
type Publisher interface {
	PublishTimeSeriesRowsChanged(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error
	PublishRecordRowsChanged(ctx context.Context, event *pb.RecordRowsChangedEvent) error
}

// Subscription 表示一个已建立的事件订阅。
type Subscription interface {
	Close() error
}

// Subscriber consumes PrimaryStore changes.
type Subscriber interface {
	SubscribeTimeSeriesRowsChanged(ctx context.Context, handler TimeSeriesRowsChangedHandler) (Subscription, error)
	SubscribeRecordRowsChanged(ctx context.Context, handler RecordRowsChangedHandler) (Subscription, error)
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
	recordHandlers     map[uint64]RecordRowsChangedHandler
	inFlight           int
	closed             bool
}

func NewMemoryBus() *MemoryBus {
	bus := &MemoryBus{
		timeSeriesHandlers: make(map[uint64]TimeSeriesRowsChangedHandler),
		recordHandlers:     make(map[uint64]RecordRowsChangedHandler),
	}
	bus.closeCond = sync.NewCond(&bus.mu)
	return bus
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

func (b *MemoryBus) SubscribeRecordRowsChanged(ctx context.Context, handler RecordRowsChangedHandler) (Subscription, error) {
	_ = ctx
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, context.Canceled
	}
	if b.recordHandlers == nil {
		b.recordHandlers = make(map[uint64]RecordRowsChangedHandler)
	}
	b.nextID++
	id := b.nextID
	b.recordHandlers[id] = handler
	b.mu.Unlock()
	return &memorySubscription{close: func() { b.deleteRecordHandler(id) }}, nil
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

func (b *MemoryBus) PublishRecordRowsChanged(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return context.Canceled
	}
	handlers := make([]RecordRowsChangedHandler, 0, len(b.recordHandlers))
	for _, handler := range b.recordHandlers {
		handlers = append(handlers, handler)
	}
	b.inFlight++
	b.mu.Unlock()
	defer b.finishPublish()
	var err error
	for _, handler := range handlers {
		err = errors.Join(err, handler(ctx, cloneRecordRowsChangedEvent(event)))
	}
	return err
}

func (b *MemoryBus) Close() error {
	b.mu.Lock()
	b.closed = true
	b.timeSeriesHandlers = nil
	b.recordHandlers = nil
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

func (b *MemoryBus) deleteRecordHandler(id uint64) {
	b.mu.Lock()
	delete(b.recordHandlers, id)
	b.mu.Unlock()
}

func cloneTimeSeriesRowsChangedEvent(event *pb.TimeSeriesRowsChangedEvent) *pb.TimeSeriesRowsChangedEvent {
	if event == nil {
		return nil
	}
	return proto.Clone(event).(*pb.TimeSeriesRowsChangedEvent)
}

func cloneRecordRowsChangedEvent(event *pb.RecordRowsChangedEvent) *pb.RecordRowsChangedEvent {
	if event == nil {
		return nil
	}
	return proto.Clone(event).(*pb.RecordRowsChangedEvent)
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
