package pool

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"hash/fnv"
	"sync"
)

type Factory func(context.Context) (process.Worker, error)
type Request struct {
	ShardKey string
	Run      process.RunRequest
}
type Pool struct {
	workers []*process.Supervisor
	next    uint64
	mu      sync.Mutex
}

func New(n int, f Factory) *Pool {
	if n < 1 {
		n = 1
	}
	p := &Pool{}
	for i := 0; i < n; i++ {
		p.workers = append(p.workers, process.NewSupervisor(process.Factory(f), process.SupervisorConfig{}))
	}
	return p
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
	for _, w := range p.workers {
		if err := w.Close(); err != nil {
			return err
		}
	}
	return nil
}
