package bootstrap

import (
	"context"
	"errors"
	"sync"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/server"
)

// Runtime cancels and joins background work before closing RPC connections and
// persistence. The same cleanup runs on initialization failure and normal exit.
type Runtime struct {
	*server.Server
	ctx           context.Context
	cancel        context.CancelFunc
	gateway       *gatewayclient.Client
	mu            sync.Mutex
	closing       bool
	workers       sync.WaitGroup
	beforeClose   []func() error
	closeDatabase func() error
	once          sync.Once
	err           error
}

func newRuntime(ctx context.Context, s *server.Server) *Runtime {
	ctx, cancel := context.WithCancel(ctx)
	if s != nil {
		s.RegisterOnShutdown(cancel)
	}
	return &Runtime{Server: s, ctx: ctx, cancel: cancel}
}

func (r *Runtime) launch(work func()) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing || r.ctx.Err() != nil {
		return false
	}
	r.workers.Add(1)
	go func() { defer r.workers.Done(); work() }()
	return true
}

func (r *Runtime) join(done <-chan struct{}, err error) error {
	if err != nil {
		return err
	}
	if done != nil {
		r.workers.Add(1)
		go func() { defer r.workers.Done(); <-done }()
	}
	return nil
}

// run tracks a synchronous timer callback and links it to process shutdown.
func (r *Runtime) run(ctx context.Context, work func(context.Context) error) error {
	r.mu.Lock()
	if r.closing || r.ctx.Err() != nil {
		r.mu.Unlock()
		return context.Canceled
	}
	r.workers.Add(1)
	r.mu.Unlock()
	defer r.workers.Done()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	if r.ctx.Err() != nil {
		cancel()
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	err := work(runCtx)
	if err == nil {
		err = runCtx.Err()
	}
	return err
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		r.mu.Lock()
		r.closing = true
		r.cancel()
		r.mu.Unlock()
		r.workers.Wait()
		for _, close := range r.beforeClose {
			r.err = errors.Join(r.err, close())
		}
		if r.gateway != nil {
			r.err = errors.Join(r.err, r.gateway.Close())
		}
		if r.closeDatabase != nil {
			r.err = errors.Join(r.err, r.closeDatabase())
		}
	})
	return r.err
}
