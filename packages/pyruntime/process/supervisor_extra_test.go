package process

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryWorker struct {
	ready bool
}

func (m *memoryWorker) Load(context.Context, LoadRequest) error { m.ready = true; return nil }
func (m *memoryWorker) Run(context.Context, RunRequest) (RunResult, error) {
	return RunResult{Meta: []byte(`{"done":true}`)}, nil
}
func (m *memoryWorker) State() State {
	if m.ready {
		return StateReady
	}
	return StateStarting
}
func (m *memoryWorker) Close() error { return nil }

func TestSupervisorRunLoadedExecutesLoadAndRun(t *testing.T) {
	w := &memoryWorker{}
	s := NewSupervisor(func(context.Context) (Worker, error) { return w, nil }, SupervisorConfig{BackoffMin: time.Millisecond})
	result, err := s.RunLoaded(context.Background(), LoadRequest{LogicalID: "demo"}, RunRequest{RequestID: "r1"})
	if err != nil || len(result.Meta) == 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := s.Load(context.Background(), LoadRequest{LogicalID: "demo2"}); err != nil {
		t.Fatal(err)
	}
	if s.Restarts() != 0 {
		t.Fatalf("restarts=%d", s.Restarts())
	}
}

type failingWorker struct {
	memoryWorker
	loadErr error
	runErr  error
	closed  bool
}

func (f *failingWorker) Load(context.Context, LoadRequest) error {
	if f.loadErr != nil {
		return f.loadErr
	}
	return f.memoryWorker.Load(context.Background(), LoadRequest{})
}

func (f *failingWorker) Run(context.Context, RunRequest) (RunResult, error) {
	if f.runErr != nil {
		return RunResult{}, f.runErr
	}
	return f.memoryWorker.Run(context.Background(), RunRequest{})
}

func (f *failingWorker) Close() error {
	f.closed = true
	return f.memoryWorker.Close()
}

func TestSupervisorRestartIncrementsCounterAndClearsWorker(t *testing.T) {
	w := &failingWorker{loadErr: errors.New("load failed")}
	s := NewSupervisor(func(context.Context) (Worker, error) { return w, nil }, SupervisorConfig{BackoffMin: time.Millisecond})
	got, err := s.Ensure(context.Background())
	require.NoError(t, err)
	require.Equal(t, w, got)
	err = s.restart(w, errors.New("boom"))
	require.Error(t, err)
	assert.True(t, w.closed)
	assert.Equal(t, 1, s.Restarts())
	assert.Equal(t, StateStarting, s.State())
}

func TestSupervisorStateDeadAfterFactoryFailures(t *testing.T) {
	s := NewSupervisor(func(context.Context) (Worker, error) {
		return nil, errors.New("factory failed")
	}, SupervisorConfig{MaxConsecutiveFailures: 1})
	_, err := s.Ensure(context.Background())
	require.Error(t, err)
	assert.Equal(t, StateDead, s.State())
}

func TestWaitBackoffRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitBackoff(ctx, time.Second, 2*time.Second, 2)
	require.ErrorIs(t, err, context.Canceled)
}

func TestWaitBackoffCompletesAfterDelay(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	err := waitBackoff(ctx, 5*time.Millisecond, 20*time.Millisecond, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 5*time.Millisecond)
}

func TestSupervisorRunRetriesAfterWorkerFailure(t *testing.T) {
	attempts := 0
	s := NewSupervisor(func(context.Context) (Worker, error) {
		attempts++
		if attempts == 1 {
			return &failingWorker{runErr: errors.New("run failed")}, nil
		}
		return &memoryWorker{}, nil
	}, SupervisorConfig{BackoffMin: time.Millisecond, MaxRetries: 1})

	result, err := s.Run(context.Background(), RunRequest{RequestID: "retry"})
	require.NoError(t, err)
	assert.NotEmpty(t, result.Meta)
	assert.Equal(t, 1, s.Restarts())
}
