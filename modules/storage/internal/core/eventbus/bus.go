package eventbus

import (
	"context"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// RowsChangedHandler 处理一次主存行变更事件。
type RowsChangedHandler func(ctx context.Context, event *pb.DataRowsChangedEvent) error

// Bus 是 storage 领域事件总线，负责发布主存行变更事件。
type Bus interface {
	PublishRowsChanged(ctx context.Context, event *pb.DataRowsChangedEvent) error
}

// Subscription 表示一个已建立的事件订阅。
type Subscription interface {
	Close() error
}

// Subscriber 是可订阅行变更事件的总线扩展能力。实现该接口的总线支持异步派生消费者。
type Subscriber interface {
	SubscribeRowsChanged(ctx context.Context, handler RowsChangedHandler) (Subscription, error)
}

// MemoryBus 是进程内事件总线。发布的事件会被记录（用于测试与回放），
// 同时同步分发给所有已注册的处理器。处理器在 PublishRowsChanged 调用栈内执行，
// 由调用方决定是否放入独立 goroutine 以实现异步派生。
type MemoryBus struct {
	mu       sync.Mutex
	events   []*pb.DataRowsChangedEvent
	nextID   uint64
	handlers map[uint64]RowsChangedHandler
}

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{handlers: make(map[uint64]RowsChangedHandler)}
}

// SubscribeRowsChanged 注册一个行变更处理器。
func (b *MemoryBus) SubscribeRowsChanged(ctx context.Context, handler RowsChangedHandler) (Subscription, error) {
	_ = ctx
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	if b.handlers == nil {
		b.handlers = make(map[uint64]RowsChangedHandler)
	}
	b.nextID++
	id := b.nextID
	b.handlers[id] = handler
	b.mu.Unlock()
	return &memorySubscription{bus: b, id: id}, nil
}

func (b *MemoryBus) PublishRowsChanged(ctx context.Context, event *pb.DataRowsChangedEvent) error {
	b.mu.Lock()
	b.events = append(b.events, event)
	handlers := make([]RowsChangedHandler, 0, len(b.handlers))
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

func (b *MemoryBus) Events() []*pb.DataRowsChangedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*pb.DataRowsChangedEvent, len(b.events))
	copy(out, b.events)
	return out
}

type memorySubscription struct {
	bus *MemoryBus
	id  uint64
}

func (s *memorySubscription) Close() error {
	if s == nil || s.bus == nil {
		return nil
	}
	s.bus.mu.Lock()
	delete(s.bus.handlers, s.id)
	s.bus.mu.Unlock()
	return nil
}

type noopSubscription struct{}

func (noopSubscription) Close() error { return nil }
