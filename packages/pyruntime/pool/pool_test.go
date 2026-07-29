package pool

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

type stubWorker struct {
	state    process.State
	closeErr error
	closed   bool
}

func (s *stubWorker) Load(context.Context, process.LoadRequest) error { return nil }
func (s *stubWorker) Run(context.Context, process.RunRequest) (process.RunResult, error) {
	return process.RunResult{Meta: []byte(`{"ok":true}`)}, nil
}
func (s *stubWorker) State() process.State { return s.state }
func (s *stubWorker) Close() error {
	s.closed = true
	return s.closeErr
}

func TestPoolCloseClosesEveryWorkerAndJoinsErrors(t *testing.T) {
	firstErr := errors.New("first close failed")
	secondErr := errors.New("second close failed")
	workers := []*stubWorker{
		{state: process.StateReady, closeErr: firstErr},
		{state: process.StateReady, closeErr: secondErr},
		{state: process.StateReady},
	}
	next := 0
	p := New(len(workers), func(context.Context) (process.Worker, error) {
		w := workers[next]
		next++
		return w, nil
	})
	for _, supervisor := range p.workers {
		_, err := supervisor.Ensure(context.Background())
		require.NoError(t, err)
	}

	err := p.Close()
	require.ErrorIs(t, err, firstErr)
	require.ErrorIs(t, err, secondErr)
	for _, worker := range workers {
		assert.True(t, worker.closed)
	}
}

func TestPoolRejectsNilFactoryResult(t *testing.T) {
	p := New(1, func(context.Context) (process.Worker, error) { return nil, context.Canceled })
	_, err := p.Run(context.Background(), Request{Run: process.RunRequest{RequestID: "x"}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPoolRunLoadedAndBroadcastLoad(t *testing.T) {
	factory := func(context.Context) (process.Worker, error) {
		return &stubWorker{state: process.StateReady}, nil
	}
	p := New(2, factory)
	ctx := context.Background()
	result, err := p.RunLoaded(ctx, "shard-a", process.LoadRequest{LogicalID: "demo"}, process.RunRequest{RequestID: "r1"})
	if err != nil || len(result.Meta) == 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := p.RunLoadedMany(ctx, "shard-a", []process.LoadRequest{{LogicalID: "demo"}}, process.RunRequest{RequestID: "r2"}); err != nil {
		t.Fatal(err)
	}
	if err := p.BroadcastLoad(ctx, process.LoadRequest{LogicalID: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPoolPickUsesShardKeyAndRoundRobin(t *testing.T) {
	p := New(2, func(context.Context) (process.Worker, error) { return &stubWorker{state: process.StateReady}, nil })
	a, err := p.pick("same")
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.pick("same")
	if err != nil || a != b {
		t.Fatalf("shard pick inconsistent: %d %d err=%v", a, b, err)
	}
	first, _ := p.pick("")
	second, _ := p.pick("")
	if first == second {
		t.Fatalf("expected round-robin picks to differ: %d %d", first, second)
	}
}

func TestPoolNewClampsToOne(t *testing.T) {
	p := New(0, func(context.Context) (process.Worker, error) {
		return &stubWorker{state: process.StateReady}, nil
	})
	require.NotNil(t, p)
	assert.Len(t, p.workers, 1)
	require.NoError(t, p.Close())
}

func TestPoolPickEmptyPool(t *testing.T) {
	var p *Pool
	_, err := p.pick("x")
	require.Error(t, err)

	empty := &Pool{}
	_, err = empty.Run(context.Background(), Request{Run: process.RunRequest{RequestID: "r"}})
	require.Error(t, err)
	_, err = empty.RunLoaded(context.Background(), "s", process.LoadRequest{}, process.RunRequest{})
	require.Error(t, err)
	_, err = empty.RunLoadedMany(context.Background(), "s", nil, process.RunRequest{})
	require.Error(t, err)
	assert.NoError(t, empty.Close())
}
