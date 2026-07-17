package bootstrap

import (
	"testing"
	"time"
)

func TestWorkerProbeTimeoutAllowsColdRuntimeStartup(t *testing.T) {
	if got := workerProbeTimeout(); got < 30*time.Second {
		t.Fatalf("worker probe timeout %s is too short for a cold pandas runtime", got)
	}
}
