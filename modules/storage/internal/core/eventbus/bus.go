package eventbus

import (
	"context"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type Bus interface {
	PublishRowsChanged(ctx context.Context, event *pb.DataRowsChangedEvent) error
}

type MemoryBus struct {
	mu     sync.Mutex
	events []*pb.DataRowsChangedEvent
}

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{}
}

func (b *MemoryBus) PublishRowsChanged(ctx context.Context, event *pb.DataRowsChangedEvent) error {
	_ = ctx
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
	return nil
}

func (b *MemoryBus) Events() []*pb.DataRowsChangedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*pb.DataRowsChangedEvent, len(b.events))
	copy(out, b.events)
	return out
}
