package trigger

import "testing"

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
