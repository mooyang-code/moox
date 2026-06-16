package changefeed

import (
	"context"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type Publisher interface {
	PublishRowsChanged(ctx context.Context, event *pb.DataRowsChangedEvent) error
}

type MemoryPublisher struct {
	mu     sync.Mutex
	events []*pb.DataRowsChangedEvent
}

func NewMemoryPublisher() *MemoryPublisher {
	return &MemoryPublisher{}
}

func (p *MemoryPublisher) PublishRowsChanged(ctx context.Context, event *pb.DataRowsChangedEvent) error {
	_ = ctx
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

func (p *MemoryPublisher) Events() []*pb.DataRowsChangedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*pb.DataRowsChangedEvent, len(p.events))
	copy(out, p.events)
	return out
}
