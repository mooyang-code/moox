package execution

import (
	"context"
	"errors"
	"sync"
)

type Observe struct {
	mu   sync.Mutex
	seen map[string]Result
}

func NewObserve() *Observe { return &Observe{seen: map[string]Result{}} }

func (o *Observe) Submit(_ context.Context, r Request) (Result, error) {
	if r.ExecutionID == "" {
		return Result{}, errors.New("observe execution id is required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	result := Result{ExecutionID: r.ExecutionID, Status: "observed"}
	o.seen[r.ExecutionID] = result
	return result, nil
}
func (o *Observe) Inspect(_ context.Context, id string) (Result, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if result, ok := o.seen[id]; ok {
		return result, nil
	}
	return Result{ExecutionID: id, Status: "unknown"}, nil
}
