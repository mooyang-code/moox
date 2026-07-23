package engine

import (
	"context"
	"sync/atomic"
)

// WorkerPoolConfig configures a pool of stdio executors.
type WorkerPoolConfig struct {
	Workers int
	Stdio   StdioConfig
}

// WorkerPoolStatus is a lightweight runtime status snapshot.
type WorkerPoolStatus struct {
	Workers        int
	Next           uint64
	Ready          bool
	WorkerVersion  string
	PythonVersion  string
	RuntimeEnvHash string
	ArrowAvailable bool
}

// WorkerPool dispatches tasks over multiple stdio executors.
type WorkerPool struct {
	workers []Executor
	next    atomic.Uint64
}

// NewWorkerPool starts a stdio worker pool.
func NewWorkerPool(cfg WorkerPoolConfig) (*WorkerPool, error) {
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	pool := &WorkerPool{workers: make([]Executor, 0, cfg.Workers)}
	for i := 0; i < cfg.Workers; i++ {
		worker, err := NewStdioExecutor(cfg.Stdio)
		if err != nil {
			_ = pool.Close()
			return nil, err
		}
		pool.workers = append(pool.workers, worker)
	}
	return pool, nil
}

// Execute dispatches a task to the next worker.
func (p *WorkerPool) Execute(ctx context.Context, task *FactorTask, frame *DataFrame) (*FactorResult, error) {
	if p == nil || len(p.workers) == 0 {
		return nil, retryable("worker pool is empty")
	}
	idx := int(p.next.Add(1)-1) % len(p.workers)
	return p.workers[idx].Execute(ctx, task, frame)
}

// Status returns a lightweight status snapshot.
func (p *WorkerPool) Status() WorkerPoolStatus {
	if p == nil {
		return WorkerPoolStatus{}
	}
	return WorkerPoolStatus{Workers: len(p.workers), Next: p.next.Load()}
}

// Close closes all workers.
func (p *WorkerPool) Close() error {
	if p == nil {
		return nil
	}
	var firstErr error
	for _, worker := range p.workers {
		if err := worker.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
