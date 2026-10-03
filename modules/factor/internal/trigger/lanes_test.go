package trigger

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/stretchr/testify/require"
)

type laneTestLocks struct {
	mu     sync.Mutex
	active map[string]int
}

func (l *laneTestLocks) Lock(setID string) func() {
	l.mu.Lock()
	if l.active == nil {
		l.active = make(map[string]int)
	}
	l.active[setID]++
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		l.active[setID]--
		l.mu.Unlock()
	}
}

func TestLanesSerialPerSetParallelAcrossSets(t *testing.T) {
	lanes := NewLanes(nil)
	defer lanes.Close()
	started := make(chan string, 3)
	releaseFirst := make(chan struct{})
	done := make(chan error, 3)
	go func() {
		_, err := lanes.Do(context.Background(), "set-a", func(context.Context) (pipeline.Outcome, error) {
			started <- "a1"
			<-releaseFirst
			return pipeline.Outcome{}, nil
		})
		done <- err
	}()
	require.Equal(t, "a1", <-started)
	go func() {
		_, err := lanes.Do(context.Background(), "set-a", func(context.Context) (pipeline.Outcome, error) {
			started <- "a2"
			return pipeline.Outcome{}, nil
		})
		done <- err
	}()
	go func() {
		_, err := lanes.Do(context.Background(), "set-b", func(context.Context) (pipeline.Outcome, error) {
			started <- "b1"
			return pipeline.Outcome{}, nil
		})
		done <- err
	}()

	require.Equal(t, "b1", <-started, "a different set should run while set-a is occupied")
	select {
	case got := <-started:
		require.NotEqual(t, "a2", got, "same-set work must not overlap")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)
	require.Equal(t, "a2", <-started)
	for range 3 {
		require.NoError(t, <-done)
	}
}

func TestLaneHoldsSetLockDuringRun(t *testing.T) {
	locks := &laneTestLocks{active: make(map[string]int)}
	lanes := NewLanes(locks)
	defer lanes.Close()
	_, err := lanes.Do(context.Background(), "set-a", func(context.Context) (pipeline.Outcome, error) {
		locks.mu.Lock()
		defer locks.mu.Unlock()
		require.Equal(t, 1, locks.active["set-a"])
		return pipeline.Outcome{}, nil
	})
	require.NoError(t, err)
	locks.mu.Lock()
	require.Zero(t, locks.active["set-a"])
	locks.mu.Unlock()
}

func TestLaneStatusReportsActiveAndQueuedPeriods(t *testing.T) {
	lanes := NewLanes(nil)
	defer lanes.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{}, 2)
	go func() {
		_, _ = lanes.Do(context.Background(), "set-a", func(context.Context) (pipeline.Outcome, error) {
			close(started)
			<-release
			return pipeline.Outcome{}, nil
		})
		done <- struct{}{}
	}()
	<-started
	go func() {
		_, _ = lanes.Do(context.Background(), "set-a", func(context.Context) (pipeline.Outcome, error) {
			return pipeline.Outcome{}, nil
		})
		done <- struct{}{}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		status := lanes.Status()
		if len(status) == 1 && status[0].Active && status[0].Queued == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lane status did not include active and queued work: %#v", status)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	<-done
	<-done
}
