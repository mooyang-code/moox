package pool

import (
	"context"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
	"hash/fnv"
	"sync"
)

type Factory func(context.Context) (process.Worker, error)
type Request struct {
	ShardKey string
	Run      process.RunRequest
}
type Pool struct {
	workers   []*process.Supervisor
	available chan int
	next      uint64
	mu        sync.Mutex
}

func New(n int, f Factory) *Pool {
	if n < 1 {
		n = 1
	}
	p := &Pool{available: make(chan int, n)}
	for i := 0; i < n; i++ {
		p.workers = append(p.workers, process.NewSupervisor(process.Factory(f), process.SupervisorConfig{}))
		p.available <- i
	}
	return p
}

// RunAnyLoadedMany waits for any free worker, runs the request, and returns the
// worker to the pool. It is intended for workloads that do not need affinity.
func (p *Pool) RunAnyLoadedMany(ctx context.Context, loads []process.LoadRequest, run process.RunRequest) (process.RunResult, error) {
	if p == nil || len(p.workers) == 0 {
		return process.RunResult{}, errors.New("pyruntime: empty pool")
	}
	select {
	case index := <-p.available:
		defer func() { p.available <- index }()
		return p.workers[index].RunLoadedMany(ctx, loads, run)
	case <-ctx.Done():
		return process.RunResult{}, ctx.Err()
	}
}

// Warmup starts every worker and validates that each worker exposes matching
// runtime metadata. Callers that want lazy capacity should use WarmupOne.
func (p *Pool) Warmup(ctx context.Context) (protocol.Hello, error) {
	if p == nil || len(p.workers) == 0 {
		return protocol.Hello{}, errors.New("pyruntime: empty pool")
	}
	var first protocol.Hello
	for i, supervisor := range p.workers {
		hello, err := warmupSupervisor(ctx, supervisor, i)
		if err != nil {
			return protocol.Hello{}, err
		}
		if i == 0 {
			first = hello
			continue
		}
		if hello.ProtocolVersion != first.ProtocolVersion || hello.WorkerVersion != first.WorkerVersion || hello.PythonVersion != first.PythonVersion {
			return protocol.Hello{}, fmt.Errorf("worker %d hello metadata differs from worker 0", i)
		}
	}
	return first, nil
}

// WarmupOne validates one worker while leaving remaining capacity lazy.
func (p *Pool) WarmupOne(ctx context.Context) (protocol.Hello, error) {
	if p == nil || len(p.workers) == 0 {
		return protocol.Hello{}, errors.New("pyruntime: empty pool")
	}
	return warmupSupervisor(ctx, p.workers[0], 0)
}

func warmupSupervisor(ctx context.Context, supervisor *process.Supervisor, index int) (protocol.Hello, error) {
	worker, err := supervisor.Ensure(ctx)
	if err != nil {
		return protocol.Hello{}, fmt.Errorf("warm up worker %d: %w", index, err)
	}
	provider, ok := worker.(interface{ Hello() protocol.Hello })
	if !ok {
		return protocol.Hello{}, fmt.Errorf("warm up worker %d: hello metadata unavailable", index)
	}
	hello := provider.Hello()
	if hello.WorkerVersion == "" || hello.PythonVersion == "" {
		return protocol.Hello{}, fmt.Errorf("warm up worker %d: worker and python versions are required", index)
	}
	return hello, nil
}

// Ready reports whether every configured worker is running and healthy.
func (p *Pool) Ready() bool {
	if p == nil || len(p.workers) == 0 {
		return false
	}
	for _, supervisor := range p.workers {
		state := supervisor.State()
		if state == process.StateStarting || state == process.StateDead {
			return false
		}
	}
	return true
}

// ReadyStarted reports whether a lazily warmed pool has at least one usable
// worker and no slot has entered a permanent crash loop.
func (p *Pool) ReadyStarted() bool {
	if p == nil || len(p.workers) == 0 {
		return false
	}
	ready := false
	for _, supervisor := range p.workers {
		state := supervisor.State()
		if state == process.StateDead {
			return false
		}
		ready = ready || state == process.StateReady || state == process.StateBusy
	}
	return ready
}
func (p *Pool) Run(ctx context.Context, r Request) (process.RunResult, error) {
	idx, err := p.pick(r.ShardKey)
	if err != nil {
		return process.RunResult{}, err
	}
	return p.workers[idx].Run(ctx, r.Run)
}

func (p *Pool) RunLoaded(ctx context.Context, shardKey string, load process.LoadRequest, run process.RunRequest) (process.RunResult, error) {
	idx, err := p.pick(shardKey)
	if err != nil {
		return process.RunResult{}, err
	}
	return p.workers[idx].RunLoaded(ctx, load, run)
}
func (p *Pool) RunLoadedMany(ctx context.Context, shardKey string, loads []process.LoadRequest, run process.RunRequest) (process.RunResult, error) {
	idx, err := p.pick(shardKey)
	if err != nil {
		return process.RunResult{}, err
	}
	return p.workers[idx].RunLoadedMany(ctx, loads, run)
}
func (p *Pool) BroadcastLoad(ctx context.Context, load process.LoadRequest) error {
	for _, s := range p.workers {
		if err := s.Load(ctx, load); err != nil {
			return err
		}
	}
	return nil
}
func (p *Pool) pick(shardKey string) (int, error) {
	if p == nil || len(p.workers) == 0 {
		return 0, errors.New("pyruntime: empty pool")
	}
	if shardKey != "" {
		h := fnv.New32a()
		_, _ = h.Write([]byte(shardKey))
		return int(h.Sum32() % uint32(len(p.workers))), nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	idx := int(p.next % uint64(len(p.workers)))
	p.next++
	return idx, nil
}
func (p *Pool) Close() error {
	var errs []error
	for _, w := range p.workers {
		if err := w.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
