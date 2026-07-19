package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
)

type TimeSeriesRowsCommittedHandler func(ctx context.Context, event *pb.TimeSeriesRowsCommitted) error
type RecordRowsCommittedHandler func(ctx context.Context, event *pb.RecordRowsCommitted) error

// RowsCommittedEvent is the single logical delivery lane for DataShard
// commits. The two payload types retain their domain-specific schemas, but a
// ViewBuilder must consume them through one ordered subscription.
type RowsCommittedEvent struct {
	TimeSeries *pb.TimeSeriesRowsCommitted
	Record     *pb.RecordRowsCommitted
}

type RowsCommittedHandler func(ctx context.Context, event *RowsCommittedEvent) error

// Publisher publishes committed PrimaryStore changes.
type Publisher interface {
	PublishTimeSeriesRowsCommitted(ctx context.Context, event *pb.TimeSeriesRowsCommitted) error
	PublishRecordRowsCommitted(ctx context.Context, event *pb.RecordRowsCommitted) error
}

// Subscription 表示一个已建立的事件订阅。
type Subscription interface {
	Close() error
}

// Subscriber consumes PrimaryStore changes.
type Subscriber interface {
	SubscribeRowsCommitted(ctx context.Context, handler RowsCommittedHandler) (Subscription, error)
	SubscribeTimeSeriesRowsCommitted(ctx context.Context, handler TimeSeriesRowsCommittedHandler) (Subscription, error)
	SubscribeRecordRowsCommitted(ctx context.Context, handler RecordRowsCommittedHandler) (Subscription, error)
}

var ErrNoSubscribers = errors.New("storage eventbus has no subscribers")

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
	committedHandlers  map[uint64]*memoryCommittedHandler
	inFlight           int
	closed             bool
}

func NewMemoryBus() *MemoryBus {
	bus := &MemoryBus{
		timeSeriesHandlers: make(map[uint64]*memoryTimeSeriesHandler),
		recordHandlers:     make(map[uint64]*memoryRecordHandler),
		committedHandlers:  make(map[uint64]*memoryCommittedHandler),
	}
	bus.closeCond = sync.NewCond(&bus.mu)
	return bus
}

func (b *MemoryBus) SubscribeRowsCommitted(ctx context.Context, handler RowsCommittedHandler) (Subscription, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, context.Canceled
	}
	b.nextID++
	id := b.nextID
	entry := &memoryCommittedHandler{handler: handler, lifecycle: newHandlerLifecycle()}
	b.committedHandlers[id] = entry
	return &memorySubscription{close: func() { b.deleteCommittedHandler(id, entry) }}, nil
}

func (b *MemoryBus) Ready() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closed
}

// PublishMessage replays the deterministic outbox payload through the same
// in-process publication path used by direct publishers.
func (b *MemoryBus) PublishMessage(ctx context.Context, data []byte) error {
	message := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(data, message); err != nil {
		return err
	}
	if err := validateRowsCommittedMessage(message, ""); err != nil {
		return err
	}
	switch message.GetMessageType() {
	case defaultTimeSeriesRowsCommittedType:
		event := &pb.TimeSeriesRowsCommitted{}
		if err := proto.Unmarshal(message.GetPayload(), event); err != nil {
			return err
		}
		return b.PublishTimeSeriesRowsCommitted(ctx, event)
	case defaultRecordRowsCommittedType:
		event := &pb.RecordRowsCommitted{}
		if err := proto.Unmarshal(message.GetPayload(), event); err != nil {
			return err
		}
		return b.PublishRecordRowsCommitted(ctx, event)
	default:
		return fmt.Errorf("unknown storage message_type %q", message.GetMessageType())
	}
}

func (b *MemoryBus) SubscribeTimeSeriesRowsCommitted(ctx context.Context, handler TimeSeriesRowsCommittedHandler) (Subscription, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
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

func (b *MemoryBus) SubscribeRecordRowsCommitted(ctx context.Context, handler RecordRowsCommittedHandler) (Subscription, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
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

func (b *MemoryBus) PublishTimeSeriesRowsCommitted(ctx context.Context, event *pb.TimeSeriesRowsCommitted) error {
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
	if len(handlers) == 0 && len(b.committedHandlers) == 0 {
		b.mu.Unlock()
		return ErrNoSubscribers
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
		err = errors.Join(err, entry.call(ctx, cloneTimeSeriesRowsCommitted(event)))
	}
	err = errors.Join(err, b.publishCommitted(ctx, &RowsCommittedEvent{TimeSeries: cloneTimeSeriesRowsCommitted(event)}))
	return err
}

func (b *MemoryBus) PublishRecordRowsCommitted(ctx context.Context, event *pb.RecordRowsCommitted) error {
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
	if len(handlers) == 0 && len(b.committedHandlers) == 0 {
		b.mu.Unlock()
		return ErrNoSubscribers
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
		err = errors.Join(err, entry.call(ctx, cloneRecordRowsCommitted(event)))
	}
	err = errors.Join(err, b.publishCommitted(ctx, &RowsCommittedEvent{Record: cloneRecordRowsCommitted(event)}))
	return err
}

func (b *MemoryBus) publishCommitted(ctx context.Context, event *RowsCommittedEvent) error {
	b.mu.Lock()
	handlers := make([]*memoryCommittedHandler, 0, len(b.committedHandlers))
	for _, entry := range b.committedHandlers {
		if entry.lifecycle.acquire() {
			handlers = append(handlers, entry)
		}
	}
	b.mu.Unlock()
	var err error
	for _, entry := range handlers {
		err = errors.Join(err, entry.call(ctx, event))
	}
	return err
}

func (b *MemoryBus) Close() error {
	b.mu.Lock()
	b.closed = true
	timeSeriesHandlers := b.timeSeriesHandlers
	recordHandlers := b.recordHandlers
	committedHandlers := b.committedHandlers
	b.timeSeriesHandlers = nil
	b.recordHandlers = nil
	b.committedHandlers = nil
	for _, entry := range timeSeriesHandlers {
		entry.lifecycle.beginClose()
	}
	for _, entry := range recordHandlers {
		entry.lifecycle.beginClose()
	}
	for _, entry := range committedHandlers {
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
	for _, entry := range committedHandlers {
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

func (b *MemoryBus) deleteCommittedHandler(id uint64, entry *memoryCommittedHandler) {
	b.mu.Lock()
	delete(b.committedHandlers, id)
	entry.lifecycle.beginClose()
	b.mu.Unlock()
	entry.lifecycle.wait()
}

type memoryTimeSeriesHandler struct {
	handler   TimeSeriesRowsCommittedHandler
	lifecycle *handlerLifecycle
}

func (h *memoryTimeSeriesHandler) call(ctx context.Context, event *pb.TimeSeriesRowsCommitted) error {
	defer h.lifecycle.release()
	return h.handler(ctx, event)
}

type memoryRecordHandler struct {
	handler   RecordRowsCommittedHandler
	lifecycle *handlerLifecycle
}

type memoryCommittedHandler struct {
	handler   RowsCommittedHandler
	lifecycle *handlerLifecycle
}

func (h *memoryCommittedHandler) call(ctx context.Context, event *RowsCommittedEvent) error {
	defer h.lifecycle.release()
	return h.handler(ctx, event)
}

func (h *memoryRecordHandler) call(ctx context.Context, event *pb.RecordRowsCommitted) error {
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

func cloneTimeSeriesRowsCommitted(event *pb.TimeSeriesRowsCommitted) *pb.TimeSeriesRowsCommitted {
	if event == nil {
		return nil
	}
	return proto.Clone(event).(*pb.TimeSeriesRowsCommitted)
}

func cloneRecordRowsCommitted(event *pb.RecordRowsCommitted) *pb.RecordRowsCommitted {
	if event == nil {
		return nil
	}
	return proto.Clone(event).(*pb.RecordRowsCommitted)
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
