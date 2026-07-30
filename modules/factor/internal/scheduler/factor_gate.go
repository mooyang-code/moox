package scheduler

import "sync"

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
