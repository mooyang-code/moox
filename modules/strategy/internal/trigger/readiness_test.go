package trigger

import (
	"fmt"
	"testing"
)

func TestGateEmitsWhenDatasetsArrive(t *testing.T) {
	g := New([]string{"kline", "factor"})
	g.Observe(Event{Dataset: "kline", Bar: "10"})
	g.Observe(Event{Dataset: "factor", Bar: "10"})
	select {
	case <-g.Ready():
	default:
		t.Fatal("not ready")
	}
}

func TestGateBoundsIncompleteBarsAndSuppressesDuplicates(t *testing.T) {
	g := New([]string{"kline", "factor"})
	for i := 0; i < 2000; i++ {
		g.Observe(Event{Dataset: "kline", Bar: fmt.Sprintf("%04d", i)})
	}
	if len(g.seen) > 1024 {
		t.Fatalf("pending bars grew without bound: %d", len(g.seen))
	}
	g.Observe(Event{Dataset: "factor", Bar: "2000"})
	g.Observe(Event{Dataset: "kline", Bar: "2000"})
	select {
	case <-g.Ready():
	default:
		t.Fatal("expected bar to become ready")
	}
	g.Observe(Event{Dataset: "factor", Bar: "2000"})
	select {
	case <-g.Ready():
		t.Fatal("duplicate bar was emitted")
	default:
	}
}
