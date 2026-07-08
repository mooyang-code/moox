package eventbus

import (
	"context"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

type TimeSeriesRowsUpdatedHandler func(ctx context.Context, event *pb.TimeSeriesRowsUpdated) error
type RecordRowsUpdatedHandler func(ctx context.Context, event *pb.RecordRowsUpdated) error

// Bus 是 storage 领域事件总线，负责发布主存写入流水。
type Bus interface {
	PublishTimeSeriesRowsUpdated(ctx context.Context, event *pb.TimeSeriesRowsUpdated) error
	PublishRecordRowsUpdated(ctx context.Context, event *pb.RecordRowsUpdated) error
}

// Subscription 表示一个已建立的事件订阅。
type Subscription interface {
	Close() error
}

// Subscriber 是可订阅写入流水的总线扩展能力。实现该接口的总线支持异步派生消费者。
type Subscriber interface {
	SubscribeTimeSeriesRowsUpdated(ctx context.Context, handler TimeSeriesRowsUpdatedHandler) (Subscription, error)
	SubscribeRecordRowsUpdated(ctx context.Context, handler RecordRowsUpdatedHandler) (Subscription, error)
}

// MemoryBus 是进程内事件总线实现，用于测试和单进程部署。
type MemoryBus struct {
	mu                 sync.Mutex
	timeSeriesEvents   []*pb.TimeSeriesRowsUpdated
	recordEvents       []*pb.RecordRowsUpdated
	nextID             uint64
	timeSeriesHandlers map[uint64]TimeSeriesRowsUpdatedHandler
	recordHandlers     map[uint64]RecordRowsUpdatedHandler
	inFlight           int
	idle               chan struct{}
}

func NewMemoryBus() *MemoryBus {
	idle := make(chan struct{})
	close(idle)
	return &MemoryBus{
		timeSeriesHandlers: make(map[uint64]TimeSeriesRowsUpdatedHandler),
		recordHandlers:     make(map[uint64]RecordRowsUpdatedHandler),
		idle:               idle,
	}
}

func (b *MemoryBus) SubscribeTimeSeriesRowsUpdated(ctx context.Context, handler TimeSeriesRowsUpdatedHandler) (Subscription, error) {
	_ = ctx
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	if b.timeSeriesHandlers == nil {
		b.timeSeriesHandlers = make(map[uint64]TimeSeriesRowsUpdatedHandler)
	}
	b.nextID++
	id := b.nextID
	b.timeSeriesHandlers[id] = handler
	b.mu.Unlock()
	return &memorySubscription{close: func() { b.deleteTimeSeriesHandler(id) }}, nil
}

func (b *MemoryBus) SubscribeRecordRowsUpdated(ctx context.Context, handler RecordRowsUpdatedHandler) (Subscription, error) {
	_ = ctx
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	if b.recordHandlers == nil {
		b.recordHandlers = make(map[uint64]RecordRowsUpdatedHandler)
	}
	b.nextID++
	id := b.nextID
	b.recordHandlers[id] = handler
	b.mu.Unlock()
	return &memorySubscription{close: func() { b.deleteRecordHandler(id) }}, nil
}

func (b *MemoryBus) PublishTimeSeriesRowsUpdated(ctx context.Context, event *pb.TimeSeriesRowsUpdated) error {
	stored := cloneTimeSeriesRowsUpdated(event)
	b.mu.Lock()
	b.timeSeriesEvents = append(b.timeSeriesEvents, stored)
	handlers := make([]TimeSeriesRowsUpdatedHandler, 0, len(b.timeSeriesHandlers))
	for _, handler := range b.timeSeriesHandlers {
		handlers = append(handlers, handler)
	}
	b.addInFlightLocked(len(handlers))
	b.mu.Unlock()
	for _, handler := range handlers {
		eventCopy := cloneTimeSeriesRowsUpdated(event)
		// 内存总线按发布顺序同步完成 handler 入队，避免同 key 流水乱序。
		func(handler TimeSeriesRowsUpdatedHandler, event *pb.TimeSeriesRowsUpdated) {
			defer b.finishHandler()
			_ = handler(ctx, event)
		}(handler, eventCopy)
	}
	return nil
}

func (b *MemoryBus) PublishRecordRowsUpdated(ctx context.Context, event *pb.RecordRowsUpdated) error {
	stored := cloneRecordRowsUpdated(event)
	b.mu.Lock()
	b.recordEvents = append(b.recordEvents, stored)
	handlers := make([]RecordRowsUpdatedHandler, 0, len(b.recordHandlers))
	for _, handler := range b.recordHandlers {
		handlers = append(handlers, handler)
	}
	b.addInFlightLocked(len(handlers))
	b.mu.Unlock()
	for _, handler := range handlers {
		eventCopy := cloneRecordRowsUpdated(event)
		// 内存总线按发布顺序同步完成 handler 入队，避免同 key 流水乱序。
		func(handler RecordRowsUpdatedHandler, event *pb.RecordRowsUpdated) {
			defer b.finishHandler()
			_ = handler(ctx, event)
		}(handler, eventCopy)
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

func (b *MemoryBus) TimeSeriesEvents() []*pb.TimeSeriesRowsUpdated {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*pb.TimeSeriesRowsUpdated, len(b.timeSeriesEvents))
	for i, event := range b.timeSeriesEvents {
		out[i] = cloneTimeSeriesRowsUpdated(event)
	}
	return out
}

func (b *MemoryBus) RecordEvents() []*pb.RecordRowsUpdated {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*pb.RecordRowsUpdated, len(b.recordEvents))
	for i, event := range b.recordEvents {
		out[i] = cloneRecordRowsUpdated(event)
	}
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

func cloneTimeSeriesRowsUpdated(event *pb.TimeSeriesRowsUpdated) *pb.TimeSeriesRowsUpdated {
	if event == nil {
		return nil
	}
	return proto.Clone(event).(*pb.TimeSeriesRowsUpdated)
}

func cloneRecordRowsUpdated(event *pb.RecordRowsUpdated) *pb.RecordRowsUpdated {
	if event == nil {
		return nil
	}
	return proto.Clone(event).(*pb.RecordRowsUpdated)
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
