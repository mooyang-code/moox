package eventbus

import (
	"context"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type TimeSeriesRowsChangedHandler func(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error
type RecordRowsChangedHandler func(ctx context.Context, event *pb.RecordRowsChangedEvent) error

// Bus 是 storage 领域事件总线，负责发布主存行变更事件。
type Bus interface {
	PublishTimeSeriesRowsChanged(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error
	PublishRecordRowsChanged(ctx context.Context, event *pb.RecordRowsChangedEvent) error
}

// Subscription 表示一个已建立的事件订阅。
type Subscription interface {
	Close() error
}

// Subscriber 是可订阅行变更事件的总线扩展能力。实现该接口的总线支持异步派生消费者。
type Subscriber interface {
	SubscribeTimeSeriesRowsChanged(ctx context.Context, handler TimeSeriesRowsChangedHandler) (Subscription, error)
	SubscribeRecordRowsChanged(ctx context.Context, handler RecordRowsChangedHandler) (Subscription, error)
}

// MemoryBus 是进程内事件总线实现，用于测试和单进程部署。
type MemoryBus struct {
	mu                 sync.Mutex
	timeSeriesEvents   []*pb.TimeSeriesRowsChangedEvent
	recordEvents       []*pb.RecordRowsChangedEvent
	nextID             uint64
	timeSeriesHandlers map[uint64]TimeSeriesRowsChangedHandler
	recordHandlers     map[uint64]RecordRowsChangedHandler
	inFlight           int
	idle               chan struct{}
}

func NewMemoryBus() *MemoryBus {
	idle := make(chan struct{})
	close(idle)
	return &MemoryBus{
		timeSeriesHandlers: make(map[uint64]TimeSeriesRowsChangedHandler),
		recordHandlers:     make(map[uint64]RecordRowsChangedHandler),
		idle:               idle,
	}
}

func (b *MemoryBus) SubscribeTimeSeriesRowsChanged(ctx context.Context, handler TimeSeriesRowsChangedHandler) (Subscription, error) {
	_ = ctx
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
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
	b.timeSeriesEvents = append(b.timeSeriesEvents, event)
	handlers := make([]TimeSeriesRowsChangedHandler, 0, len(b.timeSeriesHandlers))
	for _, handler := range b.timeSeriesHandlers {
		handlers = append(handlers, handler)
	}
	b.addInFlightLocked(len(handlers))
	b.mu.Unlock()
	for _, handler := range handlers {
		go func(handler TimeSeriesRowsChangedHandler) {
			defer b.finishHandler()
			_ = handler(ctx, event)
		}(handler)
	}
	return nil
}

func (b *MemoryBus) PublishRecordRowsChanged(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
	b.mu.Lock()
	b.recordEvents = append(b.recordEvents, event)
	handlers := make([]RecordRowsChangedHandler, 0, len(b.recordHandlers))
	for _, handler := range b.recordHandlers {
		handlers = append(handlers, handler)
	}
	b.addInFlightLocked(len(handlers))
	b.mu.Unlock()
	for _, handler := range handlers {
		go func(handler RecordRowsChangedHandler) {
			defer b.finishHandler()
			_ = handler(ctx, event)
		}(handler)
	}
	return nil
}

func (b *MemoryBus) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	b.ensureIdleLocked()
	idle := b.idle
	b.mu.Unlock()

	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *MemoryBus) Close() error {
	return b.Wait(context.Background())
}

func (b *MemoryBus) TimeSeriesEvents() []*pb.TimeSeriesRowsChangedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*pb.TimeSeriesRowsChangedEvent, len(b.timeSeriesEvents))
	copy(out, b.timeSeriesEvents)
	return out
}

func (b *MemoryBus) RecordEvents() []*pb.RecordRowsChangedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*pb.RecordRowsChangedEvent, len(b.recordEvents))
	copy(out, b.recordEvents)
	return out
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

func (b *MemoryBus) addInFlightLocked(count int) {
	if count == 0 {
		return
	}
	b.ensureIdleLocked()
	if b.inFlight == 0 {
		b.idle = make(chan struct{})
	}
	b.inFlight += count
}

func (b *MemoryBus) finishHandler() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inFlight--
	if b.inFlight == 0 {
		close(b.idle)
	}
}

func (b *MemoryBus) ensureIdleLocked() {
	if b.idle != nil {
		return
	}
	b.idle = make(chan struct{})
	close(b.idle)
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
