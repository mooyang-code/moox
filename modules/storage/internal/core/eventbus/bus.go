package eventbus

import (
	"context"
	"errors"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

type TimeSeriesRowsUpdatedHandler func(ctx context.Context, event *pb.TimeSeriesRowsUpdated) error
type RecordRowsUpdatedHandler func(ctx context.Context, event *pb.RecordRowsUpdated) error

// Publisher publishes committed PrimaryStore changes.
type Publisher interface {
	PublishTimeSeriesRowsUpdated(ctx context.Context, event *pb.TimeSeriesRowsUpdated) error
	PublishRecordRowsUpdated(ctx context.Context, event *pb.RecordRowsUpdated) error
}

// Subscription 表示一个已建立的事件订阅。
type Subscription interface {
	Close() error
}

// Subscriber consumes PrimaryStore changes.
type Subscriber interface {
	SubscribeTimeSeriesRowsUpdated(ctx context.Context, handler TimeSeriesRowsUpdatedHandler) (Subscription, error)
	SubscribeRecordRowsUpdated(ctx context.Context, handler RecordRowsUpdatedHandler) (Subscription, error)
}

// Bus combines capabilities for bootstrap-owned transports.
type Bus interface {
	Publisher
	Subscriber
	Ready() bool
	Close() error
}

// MemoryBus 是进程内事件总线实现，用于测试和单进程部署。
type MemoryBus struct {
	mu                 sync.Mutex
	closeCond          *sync.Cond
	nextID             uint64
	timeSeriesHandlers map[uint64]*memoryTimeSeriesHandler
	recordHandlers     map[uint64]*memoryRecordHandler
	inFlight           int
	closed             bool
}

func NewMemoryBus() *MemoryBus {
	bus := &MemoryBus{
		timeSeriesHandlers: make(map[uint64]*memoryTimeSeriesHandler),
		recordHandlers:     make(map[uint64]*memoryRecordHandler),
	}
	bus.closeCond = sync.NewCond(&bus.mu)
	return bus
}

func (b *MemoryBus) Ready() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closed
}

func (b *MemoryBus) SubscribeTimeSeriesRowsUpdated(ctx context.Context, handler TimeSeriesRowsUpdatedHandler) (Subscription, error) {
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
		b.timeSeriesHandlers = make(map[uint64]*memoryTimeSeriesHandler)
	}
	b.nextID++
	id := b.nextID
	entry := &memoryTimeSeriesHandler{handler: handler, lifecycle: newHandlerLifecycle()}
	b.timeSeriesHandlers[id] = entry
	b.mu.Unlock()
	return &memorySubscription{close: func() { b.deleteTimeSeriesHandler(id, entry) }}, nil
}

func (b *MemoryBus) SubscribeRecordRowsUpdated(ctx context.Context, handler RecordRowsUpdatedHandler) (Subscription, error) {
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
		b.recordHandlers = make(map[uint64]*memoryRecordHandler)
	}
	b.nextID++
	id := b.nextID
	entry := &memoryRecordHandler{handler: handler, lifecycle: newHandlerLifecycle()}
	b.recordHandlers[id] = entry
	b.mu.Unlock()
	return &memorySubscription{close: func() { b.deleteRecordHandler(id, entry) }}, nil
}

func (b *MemoryBus) PublishTimeSeriesRowsUpdated(ctx context.Context, event *pb.TimeSeriesRowsUpdated) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return context.Canceled
	}
	handlers := make([]*memoryTimeSeriesHandler, 0, len(b.timeSeriesHandlers))
	for _, entry := range b.timeSeriesHandlers {
		if entry.lifecycle.acquire() {
			handlers = append(handlers, entry)
		}
	}
	b.inFlight++
	b.mu.Unlock()
	defer b.finishPublish()
	var err error
	next := 0
	defer func() {
		for ; next < len(handlers); next++ {
			handlers[next].lifecycle.release()
		}
	}()
	for next < len(handlers) {
		entry := handlers[next]
		next++
		err = errors.Join(err, entry.call(ctx, cloneTimeSeriesRowsUpdated(event)))
	}
	return err
}

func (b *MemoryBus) PublishRecordRowsUpdated(ctx context.Context, event *pb.RecordRowsUpdated) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return context.Canceled
	}
	handlers := make([]*memoryRecordHandler, 0, len(b.recordHandlers))
	for _, entry := range b.recordHandlers {
		if entry.lifecycle.acquire() {
			handlers = append(handlers, entry)
		}
	}
	b.inFlight++
	b.mu.Unlock()
	defer b.finishPublish()
	var err error
	next := 0
	defer func() {
		for ; next < len(handlers); next++ {
			handlers[next].lifecycle.release()
		}
	}()
	for next < len(handlers) {
		entry := handlers[next]
		next++
		err = errors.Join(err, entry.call(ctx, cloneRecordRowsUpdated(event)))
	}
	return err
}

func (b *MemoryBus) Close() error {
	b.mu.Lock()
	b.closed = true
	timeSeriesHandlers := b.timeSeriesHandlers
	recordHandlers := b.recordHandlers
	b.timeSeriesHandlers = nil
	b.recordHandlers = nil
	for _, entry := range timeSeriesHandlers {
		entry.lifecycle.beginClose()
	}
	for _, entry := range recordHandlers {
		entry.lifecycle.beginClose()
	}
	for b.inFlight > 0 {
		b.closeCond.Wait()
	}
	b.mu.Unlock()
	for _, entry := range timeSeriesHandlers {
		entry.lifecycle.wait()
	}
	for _, entry := range recordHandlers {
		entry.lifecycle.wait()
	}
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

func (b *MemoryBus) deleteTimeSeriesHandler(id uint64, entry *memoryTimeSeriesHandler) {
	b.mu.Lock()
	delete(b.timeSeriesHandlers, id)
	entry.lifecycle.beginClose()
	b.mu.Unlock()
	entry.lifecycle.wait()
}

func (b *MemoryBus) deleteRecordHandler(id uint64, entry *memoryRecordHandler) {
	b.mu.Lock()
	delete(b.recordHandlers, id)
	entry.lifecycle.beginClose()
	b.mu.Unlock()
	entry.lifecycle.wait()
}

type memoryTimeSeriesHandler struct {
	handler   TimeSeriesRowsUpdatedHandler
	lifecycle *handlerLifecycle
}

func (h *memoryTimeSeriesHandler) call(ctx context.Context, event *pb.TimeSeriesRowsUpdated) error {
	defer h.lifecycle.release()
	return h.handler(ctx, event)
}

type memoryRecordHandler struct {
	handler   RecordRowsUpdatedHandler
	lifecycle *handlerLifecycle
}

func (h *memoryRecordHandler) call(ctx context.Context, event *pb.RecordRowsUpdated) error {
	defer h.lifecycle.release()
	return h.handler(ctx, event)
}

type handlerLifecycle struct {
	mu       sync.Mutex
	cond     *sync.Cond
	closing  bool
	inFlight int
}

func newHandlerLifecycle() *handlerLifecycle {
	lifecycle := &handlerLifecycle{}
	lifecycle.cond = sync.NewCond(&lifecycle.mu)
	return lifecycle
}

func (l *handlerLifecycle) acquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closing {
		return false
	}
	l.inFlight++
	return true
}

func (l *handlerLifecycle) release() {
	l.mu.Lock()
	l.inFlight--
	if l.inFlight == 0 {
		l.cond.Broadcast()
	}
	l.mu.Unlock()
}

func (l *handlerLifecycle) beginClose() {
	l.mu.Lock()
	l.closing = true
	l.mu.Unlock()
}

func (l *handlerLifecycle) wait() {
	l.mu.Lock()
	for l.inFlight > 0 {
		l.cond.Wait()
	}
	l.mu.Unlock()
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
