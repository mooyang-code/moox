package execution

import (
	"context"
	"errors"
	"sync"
)

type Paper struct {
	mu        sync.Mutex
	seen      map[string]Result
	positions map[string]string
}

func NewPaper() *Paper { return &Paper{seen: map[string]Result{}, positions: map[string]string{}} }
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
	for key := range p.positions {
		delete(p.positions, key)
	}
	for _, target := range r.Targets {
		p.positions[target.InstrumentID] = target.TargetWeight
	}
	return got, nil
}

func (p *Paper) Positions() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	copy := make(map[string]string, len(p.positions))
	for key, value := range p.positions {
		copy[key] = value
	}
	return copy
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
