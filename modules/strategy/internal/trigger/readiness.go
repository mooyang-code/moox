package trigger

import "sync"

type Event struct{ Dataset, Bar, Revision string }
type Gate struct {
	mu        sync.Mutex
	expected  map[string]bool
	seen      map[string]map[string]bool
	pending   []string
	lastReady string
	ready     chan string
}

func New(expected []string) *Gate {
	m := map[string]bool{}
	for _, x := range expected {
		m[x] = true
	}
	return &Gate{expected: m, seen: map[string]map[string]bool{}, ready: make(chan string, 16)}
}
func (g *Gate) Observe(e Event) {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Bars are monotonic in the trigger stream. Once a bar is emitted, an
	// out-of-order duplicate must never be emitted again.
	if e.Bar == "" || (g.lastReady != "" && e.Bar <= g.lastReady) {
		return
	}
	if g.seen[e.Bar] == nil {
		g.seen[e.Bar] = map[string]bool{}
		g.pending = append(g.pending, e.Bar)
		if len(g.pending) > 1024 {
			oldest := g.pending[0]
			g.pending = g.pending[1:]
			delete(g.seen, oldest)
		}
	}
	g.seen[e.Bar][e.Dataset] = true
	ok := true
	for d := range g.expected {
		if !g.seen[e.Bar][d] {
			ok = false
		}
	}
	if ok {
		select {
		case g.ready <- e.Bar:
			g.lastReady = e.Bar
			delete(g.seen, e.Bar)
		default:
		}
	}
}
func (g *Gate) Ready() <-chan string { return g.ready }
