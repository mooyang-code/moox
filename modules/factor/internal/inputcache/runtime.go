package inputcache

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/timerjob"
)

// Runtime owns the disposable manager and its synchronous tRPC maintenance job.
// The engine registers Job; construction itself starts no timer or goroutine.
type Runtime struct {
	Manager   *Manager
	Job       *timerjob.Job
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	stopped   bool
	active    sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

func NewRuntime(ctx context.Context, cfg Config, now func() time.Time) (*Runtime, error) {
	if ctx == nil || now == nil {
		return nil, errors.New("cache runtime requires context and clock")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manager, err := NewManager(cfg)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	r := &Runtime{Manager: manager, ctx: runCtx, cancel: cancel}
	r.Job, err = NewMaintenanceJob(cfg, now, r.maintain)
	if err != nil {
		return nil, errors.Join(err, r.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, r.Close())
	}
	return r, nil
}

func (r *Runtime) maintain(ctx context.Context) error {
	r.mu.Lock()
	if r.stopped || r.ctx.Err() != nil {
		r.mu.Unlock()
		return context.Canceled
	}
	r.active.Add(1)
	r.mu.Unlock()
	defer r.active.Done()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	defer cancel()
	_, err := r.Manager.Maintain(ctx)
	return err
}

func (r *Runtime) Cancel() { r.cancel() }

// Close fences and drains maintenance before closing manager-owned databases.
// Manager.Close also drains reader/fill callbacks before releasing its lock.
func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.stopped = true
		r.cancel()
		r.mu.Unlock()
		r.active.Wait()
		r.closeErr = r.Manager.Close()
	})
	return r.closeErr
}
