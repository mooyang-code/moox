package taskrunner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOperationGateAcquireContextCanBeCancelled(t *testing.T) {
	gate := NewOperationGate()
	release := gate.Acquire()
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := gate.AcquireContext(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

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

func TestFactorGateAcquireRunsDeduplicatesAndReleasesAllLocks(t *testing.T) {
	gate := NewFactorGate()
	release := gate.AcquireRuns([]string{"factor-b", "factor-a", "factor-a"})
	require.Len(t, gate.gates, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		gate.Mutate("factor-a", func() {})
	}()
	select {
	case <-done:
		t.Fatal("mutation entered while batch still held a read lock")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("opposite-order batch remained blocked after release")
	}
}
