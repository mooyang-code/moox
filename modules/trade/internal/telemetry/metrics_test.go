package telemetry

import (
	"testing"
	"time"
)

func TestPrivateStreamFreshness(t *testing.T) {
	now := time.Now()
	MarkPrivateEvent("test", now.Add(-time.Second))
	age, ok := PrivateAge("test", now)
	if !ok || age != time.Second {
		t.Fatalf("age=%v ok=%v", age, ok)
	}
}
