package eventconsumer

import (
	"github.com/nats-io/nats.go"
	"testing"
)

func TestDefaultConfigDoesNotReadProcessEnvironment(t *testing.T) {
	t.Setenv("MOOX_OBSERVABILITY_DELIVER_POLICY", "new")
	if got := DefaultConfig().DeliverPolicy; got != nats.DeliverAllPolicy {
		t.Fatalf("default config must be independent of process environment, got %v", got)
	}
}
