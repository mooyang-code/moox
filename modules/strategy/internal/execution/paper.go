package execution

import (
	"context"
	"errors"
	"sync"
)

type Paper struct {
	mu   sync.Mutex
	seen map[string]Result
}

func NewPaper() *Paper { return &Paper{seen: map[string]Result{}} }
func (p *Paper) Submit(_ context.Context, r Request) (Result, error) {
	if r.IdempotencyKey == "" {
		return Result{}, errors.New("paper execution idempotency key is required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if got, ok := p.seen[r.IdempotencyKey]; ok {
		return got, nil
	}
	got := Result{ExecutionID: r.ExecutionID, Status: "accepted"}
	p.seen[r.IdempotencyKey] = got
	return got, nil
}
func (p *Paper) Inspect(_ context.Context, id string) (Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range p.seen {
		if v.ExecutionID == id {
			return v, nil
		}
	}
	return Result{ExecutionID: id, Status: "unknown"}, nil
}
