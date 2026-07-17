package pool

import (
	"context"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

type stubWorker struct {
	state process.State
}

func (s *stubWorker) Load(context.Context, process.LoadRequest) error { return nil }
func (s *stubWorker) Run(context.Context, process.RunRequest) (process.RunResult, error) {
	return process.RunResult{Meta: []byte(`{"ok":true}`)}, nil
}
func (s *stubWorker) State() process.State { return s.state }
func (s *stubWorker) Close() error         { return nil }

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
