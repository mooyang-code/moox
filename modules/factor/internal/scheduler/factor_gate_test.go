package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFactorGateMutationWaitsForRunningTask(t *testing.T) {
	gate := NewFactorGate()
	release := gate.AcquireRun("factor-a")
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		gate.Mutate("factor-a", func() {
			close(entered)
		})
		close(done)
	}()

	select {
	case <-entered:
		t.Fatal("mutation entered while task still held read lock")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
}
