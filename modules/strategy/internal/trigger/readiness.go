package trigger

import "sync"

type Event struct{ Dataset, Bar, Revision string }
type Gate struct {
	mu       sync.Mutex
	expected map[string]bool
	seen     map[string]map[string]bool
	emitted  map[string]bool
	ready    chan string
}

func New(expected []string) *Gate {
	m := map[string]bool{}
	for _, x := range expected {
		m[x] = true
	}
	return &Gate{expected: m, seen: map[string]map[string]bool{}, emitted: map[string]bool{}, ready: make(chan string, 16)}
}
func (g *Gate) Observe(e Event) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen[e.Bar] == nil {
		g.seen[e.Bar] = map[string]bool{}
	}
	g.seen[e.Bar][e.Dataset] = true
	ok := true
	for d := range g.expected {
		if !g.seen[e.Bar][d] {
			ok = false
		}
	}
	if ok {
		if g.emitted[e.Bar] {
			return
		}
		select {
		case g.ready <- e.Bar:
			g.emitted[e.Bar] = true
			if len(g.emitted) > 1024 {
				for bar := range g.emitted {
					delete(g.emitted, bar)
					break
				}
			}
			// Keep only a bounded readiness window; bars are immutable once
			// emitted and no longer need to occupy the gate's maps.
			delete(g.seen, e.Bar)
		default:
		}
	}
}
func (g *Gate) Ready() <-chan string { return g.ready }
