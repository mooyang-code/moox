package taskrunner

import (
	"context"
	"sync"
)

// OperationGate serializes a complete factor operation, including its final
// marker write, with lifecycle mutations. The factor gate below protects
// individual Python runs; this gate protects the larger trigger transaction.
type OperationGate struct{ token chan struct{} }

func NewOperationGate() *OperationGate {
	token := make(chan struct{}, 1)
	token <- struct{}{}
	return &OperationGate{token: token}
}

func (g *OperationGate) Acquire() func() {
	if g == nil || g.token == nil {
		return func() {}
	}
	<-g.token
	return func() { g.token <- struct{}{} }
}

// AcquireContext waits for the complete-operation token without making a
// cancelled event consumer or RPC wait behind a long-running calculation.
func (g *OperationGate) AcquireContext(ctx context.Context) (func(), error) {
	if g == nil || g.token == nil {
		return func() {}, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-g.token:
		return func() { g.token <- struct{}{} }, nil
	}
}

// FactorGate prevents one factor's definition or bindings from changing while
// a task for that factor is validating and running.
type FactorGate struct {
	mu    sync.Mutex
	gates map[string]*sync.RWMutex
}

func NewFactorGate() *FactorGate {
	return &FactorGate{gates: make(map[string]*sync.RWMutex)}
}

func (g *FactorGate) gate(factorID string) *sync.RWMutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	gate := g.gates[factorID]
	if gate == nil {
		gate = &sync.RWMutex{}
		g.gates[factorID] = gate
	}
	return gate
}

func (g *FactorGate) AcquireRun(factorID string) func() {
	gate := g.gate(factorID)
	gate.RLock()
	return gate.RUnlock
}

func (g *FactorGate) Mutate(factorID string, fn func()) {
	gate := g.gate(factorID)
	gate.Lock()
	defer gate.Unlock()
	fn()
}
