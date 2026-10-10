// Package bootstrap owns the host gateway's listeners and complete snapshots.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/config"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/controlplane"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/directory"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/health"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/listener"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/router"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/store"
	"github.com/mooyang-code/moox/packages/healthz"
	trpc "trpc.group/trpc-go/trpc-go"
)

type snapshotStore interface {
	Load() (*snapshot.View, error)
	Save(*snapshot.View) error
}
type controlClient interface {
	Pull(context.Context, string) (*snapshot.View, error)
	Report(context.Context, string, int32, string) error
}

type Options struct {
	HostID                                string
	Snapshots                             snapshotStore
	Control                               controlClient
	Health                                *health.State
	Now                                   func() time.Time
	Warn                                  func(string)
	SyncWarningAfter, SyncWarningInterval time.Duration
}

type Runtime struct {
	hostID                    string
	snapshots                 snapshotStore
	control                   controlClient
	health                    *health.State
	state                     snapshot.State
	mu                        sync.Mutex
	dirty                     bool
	now                       func() time.Time
	warn                      func(string)
	warnAfter, warnEvery      time.Duration
	failureSince, lastWarning time.Time
}

func New(options Options) *Runtime {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Warn == nil {
		options.Warn = func(s string) { log.Print(s) }
	}
	if options.Health == nil {
		options.Health = health.NewState()
	}
	if options.SyncWarningAfter <= 0 {
		options.SyncWarningAfter = 10 * time.Minute
	}
	if options.SyncWarningInterval <= 0 {
		options.SyncWarningInterval = 10 * time.Minute
	}
	options.Health.SetClock(options.Now)
	return &Runtime{hostID: options.HostID, snapshots: options.Snapshots, control: options.Control, health: options.Health,
		now: options.Now, warn: options.Warn, warnAfter: options.SyncWarningAfter, warnEvery: options.SyncWarningInterval}
}

func (r *Runtime) State() *snapshot.State { return &r.state }

func (r *Runtime) Initialize(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if cached, err := r.snapshots.Load(); err == nil && cached != nil && cached.HostID() == r.hostID {
		r.apply(cached)
	}
	err := r.refresh(ctx)
	if err != nil && (r.state.Load() == nil || ctx.Err() != nil) {
		return fmt.Errorf("initial host snapshot unavailable: %w", err)
	}
	return nil
}

func (r *Runtime) Refresh(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refresh(ctx)
}

func (r *Runtime) apply(view *snapshot.View) {
	r.state.Apply(view)
	r.health.ApplyRoutes(view.Hash(), view.Count(), view.Disabled())
}

func (r *Runtime) refresh(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	hash, _ := r.health.Current()
	view, err := r.control.Pull(ctx, hash)
	if err == nil && view != nil {
		if view.HostID() != r.hostID {
			err = snapshot.ErrInvalid
		} else {
			// Apply revocation immediately, even if persistence fails. A dirty
			// view is retried on unchanged responses; no restart is required.
			r.apply(view)
			r.dirty = true
		}
	}
	if err == nil && r.state.Load() == nil {
		err = snapshot.ErrInvalid
	}
	if err == nil && r.dirty {
		err = r.snapshots.Save(r.state.Load())
		if err == nil {
			r.dirty = false
		}
	}
	lastError := ""
	if err != nil {
		r.health.RouteSyncFailed()
		if errors.Is(err, snapshot.ErrInvalid) {
			r.health.RouteValidationFailed()
		}
		r.noteSyncFailure()
		// Reports and logs deliberately exclude upstream response/error bodies.
		lastError = "host snapshot synchronization or persistence failed"
	} else {
		r.failureSince, r.lastWarning = time.Time{}, time.Time{}
		r.health.RouteSyncSucceeded(r.now())
	}
	hash, count := r.health.Current()
	reportErr := r.control.Report(ctx, hash, int32(count), lastError)
	if reportErr != nil {
		r.health.RouteReportFailed()
		r.warn("host gateway heartbeat failed: host_id=" + r.hostID)
	} else {
		r.health.RouteReportSucceeded(r.now())
	}
	return errors.Join(err, reportErr)
}

func (r *Runtime) noteSyncFailure() {
	now := r.now()
	if r.failureSince.IsZero() {
		r.failureSince = now
		return
	}
	if now.Sub(r.failureSince) < r.warnAfter || !r.lastWarning.IsZero() && now.Sub(r.lastWarning) < r.warnEvery {
		return
	}
	r.lastWarning = now
	r.warn("host gateway snapshot sync stale: host_id=" + r.hostID + "; keeping last validated snapshot")
}

func Run(ctx context.Context, cfg config.Config, version string) error {
	material, credentials, err := config.LoadIdentity(cfg)
	if err != nil {
		return err
	}
	health := health.NewState()
	healthHandler, err := authenticatedHealthHandler(health.Handler())
	if err != nil {
		return err
	}
	// tRPC repairs process-wide codec/client configuration. Finish setup before
	// creating any control connection or accepting requests.
	timers := trpc.NewServer()
	var closeTimersOnce sync.Once
	closeTimers := func() { closeTimersOnce.Do(func() { _ = timers.Close(nil) }) }
	defer closeTimers()
	control, err := controlplane.NewRPC(cfg, material, credentials, version)
	if err != nil {
		return err
	}
	defer control.Close()
	cache := store.NewSnapshots(cfg.Store.Path, cfg.Host.ID)
	if err := cache.Prepare(); err != nil {
		return err
	}
	nonces, err := store.OpenNonces(filepath.Join(cfg.Store.Path, "nonces"))
	if err != nil {
		return err
	}
	defer nonces.Close()
	runtime := New(Options{HostID: cfg.Host.ID, Snapshots: cache, Control: control, Health: health})
	initialCtx, cancelInitial := context.WithTimeout(ctx, 15*time.Second)
	err = runtime.Initialize(initialCtx)
	cancelInitial()
	if err != nil {
		return err
	}
	health.SetStorageCheck(func() error { return errors.Join(cache.Check(), nonces.Check()) })
	proxy, err := router.NewService(router.ServiceOptions{State: runtime.State(), Nonces: nonces, Metrics: health})
	if err != nil {
		return err
	}
	defer proxy.Close()
	if err := registerRouteRefreshTimer(timers, runtime); err != nil {
		return err
	}
	if err := registerMetricsReporter(timers); err != nil {
		return err
	}
	opened, err := listener.Open(ctx, cfg, material)
	if err != nil {
		return err
	}
	defer opened.Close()
	remote := listener.TRPC(opened.Remote, "trpc.moox.hostgateway.Remote")
	local := listener.TRPC(opened.Local, "trpc.moox.hostgateway.Local")
	if err := proxy.Register(remote); err != nil {
		return err
	}
	if err := proxy.Register(local); err != nil {
		return err
	}
	if err := directory.Register(local, runtime.State()); err != nil {
		return err
	}
	healthServer := newHealthHTTPServer(healthHandler)
	results := make(chan serverResult, 4)
	go func() { results <- serverResult{"remote tRPC", remote.Serve()} }()
	go func() { results <- serverResult{"local tRPC", local.Serve()} }()
	go func() {
		err := healthServer.Serve(opened.Health)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		results <- serverResult{"health", err}
	}()
	go func() { results <- serverResult{"timers", timers.Serve()} }()
	var firstErr error
	completed := 0
	select {
	case <-ctx.Done():
	case result := <-results:
		completed = 1
		if ctx.Err() == nil {
			firstErr = fmt.Errorf("%s stopped unexpectedly: %v", result.name, result.err)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	shutdownErr := healthServer.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = healthServer.Close()
	}
	_ = remote.Close(nil)
	_ = local.Close(nil)
	closeTimers()
	for completed < 4 {
		<-results
		completed++
	}
	return errors.Join(firstErr, shutdownErr)
}

func authenticatedHealthHandler(next http.Handler) (http.Handler, error) {
	return healthz.WrapFromEnv(next)
}
func newHealthHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
}

type serverResult struct {
	name string
	err  error
}
