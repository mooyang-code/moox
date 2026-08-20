package taskrunner

import (
	"context"
	"sort"
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

// AcquireRuns acquires a stable set of factor read locks. Stable ordering is
// required because one period batch may contain several factors and batches
// can be executing concurrently for different subjects.
func (g *FactorGate) AcquireRuns(factorIDs []string) func() {
	if g == nil {
		return func() {}
	}
	ids := append([]string(nil), factorIDs...)
	sort.Strings(ids)
	unique := ids[:0]
	for _, id := range ids {
		if id == "" || (len(unique) > 0 && unique[len(unique)-1] == id) {
			continue
		}
		unique = append(unique, id)
	}
	locks := make([]*sync.RWMutex, 0, len(unique))
	for _, id := range unique {
		lock := g.gate(id)
		lock.RLock()
		locks = append(locks, lock)
	}
	return func() {
		for index := len(locks) - 1; index >= 0; index-- {
			locks[index].RUnlock()
		}
	}
}

func (g *FactorGate) Mutate(factorID string, fn func()) {
	gate := g.gate(factorID)
	gate.Lock()
	defer gate.Unlock()
	fn()
}
