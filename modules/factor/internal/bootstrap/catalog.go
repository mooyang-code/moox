package bootstrap

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalogsync"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const catalogTimerService = "trpc.moox.factor.engine.catalog.timer"

func connectCatalog(ctx context.Context, cfg CatalogBusConfig, name string) (*jetstream.Client, error) {
	bus := jetstream.ConfigFromEnv(cfg.URLs, name)
	if cfg.CredentialFile != "" {
		if err := bus.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.CredentialFile)); err != nil {
			return nil, fmt.Errorf("load catalog credentials: %w", err)
		}
	}
	return jetstream.Connect(ctx, bus)
}

// StartControlCatalog owns a dedicated authenticated connection. Closing it
// removes the responder before the caller closes the authoritative store.
func StartControlCatalog(ctx context.Context, cfg CatalogBusConfig, reader catalogsync.SnapshotReader) (func() error, error) {
	if ctx == nil || reader == nil {
		return nil, fmt.Errorf("control catalog context and reader are required")
	}
	conn, err := connectCatalog(ctx, cfg, "moox-factor-control-catalog")
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	guarded := &controlCatalogReader{ctx: runCtx, cancel: cancel, reader: reader}
	if _, err := catalogsync.ServeSnapshots(runCtx, conn, guarded); err != nil {
		guarded.stop()
		_ = conn.Close()
		return nil, err
	}
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() {
			guarded.stop()
			closeErr = conn.Close()
		})
		return closeErr
	}, nil
}

// Closing a NATS connection does not join a callback already inside SQLite.
// Fence reader admission and drain it before releasing the catalog owner.
type controlCatalogReader struct {
	ctx     context.Context
	cancel  context.CancelFunc
	reader  catalogsync.SnapshotReader
	mu      sync.Mutex
	stopped bool
	active  sync.WaitGroup
}

func (r *controlCatalogReader) CatalogSnapshot(ctx context.Context) (*domain.CatalogSnapshot, error) {
	r.mu.Lock()
	if r.stopped || r.ctx.Err() != nil {
		r.mu.Unlock()
		return nil, context.Canceled
	}
	r.active.Add(1)
	r.mu.Unlock()
	defer r.active.Done()
	return r.reader.CatalogSnapshot(ctx)
}

func (r *controlCatalogReader) stop() {
	r.mu.Lock()
	r.stopped = true
	r.cancel()
	r.mu.Unlock()
	r.active.Wait()
}

// StartEngineCatalog requires an authoritative startup snapshot before any
// computation consumer starts. The returned stop function rejects future timer
// work and drains in-flight synchronization before closing its connection.
func StartEngineCatalog(ctx context.Context, s *server.Server, cfg *EngineApplicationConfig, replica catalogsync.Replica, activate catalogsync.Activation) (func() error, error) {
	if cfg == nil || s == nil || s.Service(catalogTimerService) == nil {
		return nil, fmt.Errorf("factor engine catalog timer is not configured")
	}
	conn, err := connectCatalog(ctx, CatalogBusConfig{URLs: cfg.EventBus.URLs, CredentialFile: cfg.EventBus.CredentialFile}, "moox-factor-engine-catalog")
	if err != nil {
		return nil, err
	}
	job, stop, err := prepareEngineCatalog(ctx, cfg, replica, activate, func(ctx context.Context) (*domain.CatalogSnapshot, error) {
		return catalogsync.FetchSnapshot(ctx, conn)
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := s.Service(catalogTimerService).Register(&timer.ServiceDesc, job); err != nil {
		stop()
		_ = conn.Close()
		return nil, fmt.Errorf("register catalog timer: %w", err)
	}
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() {
			stop()
			closeErr = conn.Close()
		})
		return closeErr
	}, nil
}

func prepareEngineCatalog(ctx context.Context, cfg *EngineApplicationConfig, replica catalogsync.Replica, activate catalogsync.Activation, fetch func(context.Context) (*domain.CatalogSnapshot, error)) (*timerjob.Job, func(), error) {
	syncer, err := catalogsync.NewSynchronizer(cfg.Engine.FactorsDir, replica, fetch, activate)
	if err != nil {
		return nil, nil, err
	}
	lifecycle := newCatalogLifecycle(ctx)
	timeout := cfg.CatalogSyncTimeout
	job, err := catalogsync.NewReconcileJob(cfg.CatalogPollInterval, timeout, time.Now, func(ctx context.Context) (catalogsync.SyncResult, error) {
		return lifecycle.run(ctx, syncer.Sync)
	})
	if err != nil {
		lifecycle.stop()
		return nil, nil, err
	}
	startupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := lifecycle.run(startupCtx, syncer.Sync); err != nil {
		lifecycle.stop()
		return nil, nil, fmt.Errorf("initialize engine catalog: %w", err)
	}
	return job, lifecycle.stop, nil
}

// tRPC timer contexts do not belong to the engine lifetime. Fence callbacks
// explicitly so server shutdown cannot race a catalog write with SQLite close.
type catalogLifecycle struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	stopped bool
	active  sync.WaitGroup
}

func newCatalogLifecycle(ctx context.Context) *catalogLifecycle {
	ctx, cancel := context.WithCancel(ctx)
	return &catalogLifecycle{ctx: ctx, cancel: cancel}
}

func (l *catalogLifecycle) run(ctx context.Context, run func(context.Context) (catalogsync.SyncResult, error)) (catalogsync.SyncResult, error) {
	l.mu.Lock()
	if l.stopped || l.ctx.Err() != nil {
		l.mu.Unlock()
		return catalogsync.SyncResult{}, context.Canceled
	}
	l.active.Add(1)
	l.mu.Unlock()
	defer l.active.Done()
	ctx, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(l.ctx, cancel)
	defer stopCancel()
	defer cancel()
	return run(ctx)
}

func (l *catalogLifecycle) stop() {
	l.mu.Lock()
	l.stopped = true
	l.cancel()
	l.mu.Unlock()
	l.active.Wait()
}
