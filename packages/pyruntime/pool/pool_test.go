package pool

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

type stubWorker struct {
	state    process.State
	closeErr error
	closed   bool
}

type helloWorker struct{ stubWorker }

func (*helloWorker) Hello() protocol.Hello {
	return protocol.Hello{ProtocolVersion: "1", WorkerVersion: "test", PythonVersion: "3.12"}
}

func TestWarmupStartsOnlyOneLazyWorker(t *testing.T) {
	started := 0
	p := New(100, func(context.Context) (process.Worker, error) {
		started++
		return &helloWorker{stubWorker: stubWorker{state: process.StateReady}}, nil
	})
	t.Cleanup(func() { require.NoError(t, p.Close()) })

	hello, err := p.WarmupOne(context.Background())
	require.NoError(t, err)
	require.Equal(t, "test", hello.WorkerVersion)
	require.Equal(t, 1, started)
	require.True(t, p.ReadyStarted())
}

type blockingWorker struct {
	entered chan<- string
	release <-chan struct{}
}

func (*blockingWorker) Load(context.Context, process.LoadRequest) error { return nil }
func (w *blockingWorker) Run(_ context.Context, req process.RunRequest) (process.RunResult, error) {
	w.entered <- req.RequestID
	<-w.release
	return process.RunResult{Meta: []byte(`{"results":[]}`)}, nil
}
func (*blockingWorker) State() process.State { return process.StateReady }
func (*blockingWorker) Close() error         { return nil }

func TestRunAnyLoadedManyUsesTwoFreeWorkersForSameShard(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	workers := []process.Worker{
		&blockingWorker{entered: entered, release: release},
		&blockingWorker{entered: entered, release: release},
	}
	var factoryMu sync.Mutex
	next := 0
	p := New(2, func(context.Context) (process.Worker, error) {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		worker := workers[next]
		next++
		return worker, nil
	})
	t.Cleanup(func() { require.NoError(t, p.Close()) })

	errCh := make(chan error, 2)
	for _, id := range []string{"bias-5", "bias-20"} {
		go func(id string) {
			_, err := p.RunAnyLoadedMany(context.Background(), nil, process.RunRequest{RequestID: id})
			errCh <- err
		}(id)
	}

	require.ElementsMatch(t, []string{"bias-5", "bias-20"}, []string{<-entered, <-entered})
	close(release)
	require.NoError(t, <-errCh)
	require.NoError(t, <-errCh)
}

func TestRunAnyLoadedManyNeverExceedsPoolSize(t *testing.T) {
	entered := make(chan string, 3)
	release := make(chan struct{})
	p := New(2, func(context.Context) (process.Worker, error) {
		return &blockingWorker{entered: entered, release: release}, nil
	})
	t.Cleanup(func() { require.NoError(t, p.Close()) })

	errCh := make(chan error, 3)
	for _, id := range []string{"one", "two", "three"} {
		go func(id string) {
			_, err := p.RunAnyLoadedMany(context.Background(), nil, process.RunRequest{RequestID: id})
			errCh <- err
		}(id)
	}
	<-entered
	<-entered
	select {
	case id := <-entered:
		t.Fatalf("third request %q entered before a worker was released", id)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-entered
	for range 3 {
		require.NoError(t, <-errCh)
	}
}

func TestRunAnyLoadedManyHonorsContextWhileWaiting(t *testing.T) {
	entered := make(chan string, 1)
	release := make(chan struct{})
	p := New(1, func(context.Context) (process.Worker, error) {
		return &blockingWorker{entered: entered, release: release}, nil
	})
	t.Cleanup(func() { require.NoError(t, p.Close()) })

	firstDone := make(chan error, 1)
	go func() {
		_, err := p.RunAnyLoadedMany(context.Background(), nil, process.RunRequest{RequestID: "first"})
		firstDone <- err
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.RunAnyLoadedMany(ctx, nil, process.RunRequest{RequestID: "second"})
	require.ErrorIs(t, err, context.Canceled)
	close(release)
	require.NoError(t, <-firstDone)
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
	_, err = empty.RunAnyLoadedMany(context.Background(), nil, process.RunRequest{})
	require.Error(t, err)
	assert.NoError(t, empty.Close())
}
