// Package bootstrap wires the standalone gateway and owns its route lifecycle.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/gateway/internal/config"
	"github.com/mooyang-code/moox/modules/gateway/internal/controlplane"
	"github.com/mooyang-code/moox/modules/gateway/internal/health"
	"github.com/mooyang-code/moox/modules/gateway/internal/router"
	"github.com/mooyang-code/moox/modules/gateway/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"github.com/mooyang-code/moox/packages/healthz"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	trpcserver "trpc.group/trpc-go/trpc-go/server"
)

type routeStore interface {
	Load() (gatewayproxy.Snapshot, error)
	Save(gatewayproxy.Snapshot) error
}

type controlClient interface {
	Pull(context.Context, string) (gatewayproxy.Snapshot, error)
	Report(context.Context, string, int32, string) error
}

type Options struct {
	NodeID              string
	Routes              routeStore
	Control             controlClient
	Health              *health.State
	Now                 func() time.Time
	Warn                func(string)
	SyncWarningAfter    time.Duration
	SyncWarningInterval time.Duration
}

const (
	serviceReadTimeout = 15 * time.Second
	// CloudNode synchronous SCF canaries may use the full 300s function
	// timeout. Keep the public gateway envelope longer than the route timeout.
	serviceWriteTimeout = 360 * time.Second
	serviceIdleTimeout  = 60 * time.Second
	healthReadTimeout   = 5 * time.Second
	healthWriteTimeout  = 10 * time.Second
	healthIdleTimeout   = 30 * time.Second
)

type Runtime struct {
	nodeID        string
	routes        routeStore
	control       controlClient
	health        *health.State
	table         gatewayproxy.Table
	mu            sync.Mutex
	now           func() time.Time
	warn          func(string)
	warnAfter     time.Duration
	warnEvery     time.Duration
	failureSince  time.Time
	failureActive bool
	lastWarning   time.Time
}

func New(options Options) *Runtime {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	warn := options.Warn
	if warn == nil {
		warn = func(message string) { log.Print(message) }
	}
	warnAfter := options.SyncWarningAfter
	if warnAfter <= 0 {
		warnAfter = 10 * time.Minute
	}
	warnEvery := options.SyncWarningInterval
	if warnEvery <= 0 {
		warnEvery = 10 * time.Minute
	}
	if options.Health != nil {
		options.Health.SetClock(now)
	}
	return &Runtime{
		nodeID: options.NodeID, routes: options.Routes, control: options.Control, health: options.Health,
		now: now, warn: warn, warnAfter: warnAfter, warnEvery: warnEvery,
	}
}

func (runtime *Runtime) Table() *gatewayproxy.Table { return &runtime.table }

func (runtime *Runtime) Initialize(ctx context.Context) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	hasCache := false
	if cached, err := runtime.routes.Load(); err == nil {
		if err := runtime.apply(cached, false); err == nil {
			hasCache = true
		}
	}
	hash, _ := runtime.health.Current()
	snapshot, err := runtime.control.Pull(ctx, hash)
	if err != nil {
		runtime.health.RouteSyncFailed()
		runtime.noteSyncFailure()
		if errors.Is(err, controlplane.ErrInvalidSnapshot) {
			runtime.health.RouteValidationFailed()
		}
		if hasCache {
			_ = runtime.report(ctx, err.Error())
			return nil
		}
		return fmt.Errorf("initial route pull failed without a valid cache: %w", err)
	}
	if err := runtime.apply(snapshot, true); err != nil {
		runtime.health.RouteSyncFailed()
		runtime.noteSyncFailure()
		if hasCache {
			_ = runtime.report(ctx, err.Error())
			return nil
		}
		return fmt.Errorf("apply initial route snapshot: %w", err)
	}
	runtime.resetSyncFailure()
	// Keep the process alive when the route pull succeeded but the heartbeat
	// could not be acknowledged. Readiness remains degraded until a later
	// refresh reports successfully.
	_ = runtime.report(ctx, "")
	return nil
}

func (runtime *Runtime) Refresh(ctx context.Context) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	hash, _ := runtime.health.Current()
	snapshot, err := runtime.control.Pull(ctx, hash)
	if err == nil {
		err = runtime.apply(snapshot, true)
	}
	if err != nil {
		runtime.health.RouteSyncFailed()
		runtime.noteSyncFailure()
		if errors.Is(err, controlplane.ErrInvalidSnapshot) {
			runtime.health.RouteValidationFailed()
		}
		_ = runtime.report(ctx, err.Error())
		return err
	}
	runtime.resetSyncFailure()
	if err := runtime.report(ctx, ""); err != nil {
		return err
	}
	return nil
}

func (runtime *Runtime) noteSyncFailure() {
	now := runtime.now()
	if !runtime.failureActive {
		runtime.failureSince = now
		runtime.failureActive = true
		return
	}
	if now.Sub(runtime.failureSince) < runtime.warnAfter {
		return
	}
	if !runtime.lastWarning.IsZero() && now.Sub(runtime.lastWarning) < runtime.warnEvery {
		return
	}
	runtime.lastWarning = now
	runtime.warn(fmt.Sprintf(
		"gateway route sync stale: node_id=%s continuous_failure=%s; retaining cached routes while readiness is degraded",
		runtime.nodeID, now.Sub(runtime.failureSince).Truncate(time.Second),
	))
}

func (runtime *Runtime) resetSyncFailure() {
	runtime.failureSince = time.Time{}
	runtime.failureActive = false
	runtime.lastWarning = time.Time{}
}

func (runtime *Runtime) apply(snapshot gatewayproxy.Snapshot, persist bool) error {
	if snapshot.NodeID != runtime.nodeID {
		runtime.health.RouteValidationFailed()
		return fmt.Errorf("route snapshot targets %q, want %q", snapshot.NodeID, runtime.nodeID)
	}
	var validated gatewayproxy.Table
	if err := validated.Replace(snapshot); err != nil {
		runtime.health.RouteValidationFailed()
		return err
	}
	if persist {
		currentHash, _ := runtime.health.Current()
		if snapshot.RouteHash == currentHash {
			runtime.health.RouteSyncSucceeded(runtime.now())
			return nil
		}
		if err := runtime.routes.Save(snapshot); err != nil {
			return fmt.Errorf("save route snapshot: %w", err)
		}
	}
	if err := runtime.table.Replace(snapshot); err != nil {
		return err
	}
	runtime.health.ApplyRoutes(snapshot.RouteHash, len(snapshot.Routes), snapshot.Disabled)
	if persist {
		runtime.health.RouteSyncSucceeded(runtime.now())
	}
	return nil
}

func (runtime *Runtime) report(ctx context.Context, lastError string) error {
	hash, count := runtime.health.Current()
	if err := runtime.control.Report(ctx, hash, int32(count), lastError); err != nil {
		runtime.health.RouteReportFailed()
		runtime.warn(fmt.Sprintf("gateway heartbeat report failed: node_id=%s", runtime.nodeID))
		return err
	}
	runtime.health.RouteReportSucceeded(runtime.now())
	return nil
}

func Run(ctx context.Context, cfg config.Config) error {
	control, err := controlplane.New(controlplane.Options{NodeID: cfg.Node.ID, BaseURL: cfg.ControlPlane.BaseURL, HMACKeyFile: cfg.ControlPlane.HMACKeyFile, CAFile: cfg.ControlPlane.CAFile})
	if err != nil {
		return err
	}
	var credentialRegistry *gatewayauth.CredentialRegistry
	var credentialsSecret string
	if cfg.Auth.CredentialsFile != "" {
		credentialRegistry, err = gatewayauth.LoadCredentialRegistry(cfg.Auth.CredentialsFile)
		if err != nil {
			return err
		}
	} else {
		credentialsSecret, err = config.ReadSecret(cfg.Auth.HMACKeyFile)
		if err != nil {
			return fmt.Errorf("read service authentication key: %w", err)
		}
	}
	nonces, err := store.OpenNonces(filepath.Join(cfg.Store.Path, "nonces"))
	if err != nil {
		return err
	}
	defer nonces.Close()
	state := health.NewState()
	routeStore := store.NewRoutes(cfg.Store.Path)
	runtime := New(Options{NodeID: cfg.Node.ID, Routes: routeStore, Control: control, Health: state})
	if err := runtime.Initialize(ctx); err != nil {
		return err
	}
	timerServer := trpc.NewServer()
	if err := registerRouteRefreshTimer(timerServer, runtime); err != nil {
		return err
	}
	if err := registerMetricsReporter(timerServer); err != nil {
		return err
	}
	state.SetStorageCheck(func() error {
		if err := routeStore.Check(); err != nil {
			return err
		}
		return nonces.Check()
	})

	serviceCredentials := gatewayauth.Credentials{KeyID: "moox-gateway-service", Caller: cfg.Auth.Caller, Secret: credentialsSecret}
	serviceHandler := router.New(router.Options{
		NodeID: cfg.Node.ID, Credentials: serviceCredentials, CredentialRegistry: credentialRegistry,
		MaxBodyBytes: cfg.Proxy.MaxBodyBytes, Table: runtime.Table(), Nonces: nonces, Disabled: state.Disabled, Metrics: state,
	})
	nativeDesc, nativeImpl := router.NativeServiceDesc(router.NativeOptions{
		NodeID: cfg.Node.ID, Credentials: serviceCredentials, CredentialRegistry: credentialRegistry,
		Table: runtime.Table(), Nonces: nonces, Disabled: state.Disabled,
	})
	nativeService := trpcserver.New(
		trpcserver.WithAddress(cfg.Server.NativeAddr), trpcserver.WithNetwork("tcp"), trpcserver.WithProtocol("trpc"),
		trpcserver.WithCurrentSerializationType(codec.SerializationTypeNoop), trpcserver.WithServiceName("trpc.moox.gateway.ServiceGateway"),
	)
	if err := nativeService.Register(nativeDesc, nativeImpl); err != nil {
		return fmt.Errorf("register native gateway service: %w", err)
	}
	serviceListener, err := net.Listen("tcp", cfg.Server.ServiceAddr)
	if err != nil {
		return fmt.Errorf("listen service endpoint: %w", err)
	}
	defer serviceListener.Close()
	healthListener, err := net.Listen("tcp", cfg.Server.HealthAddr)
	if err != nil {
		return fmt.Errorf("listen health endpoint: %w", err)
	}
	defer healthListener.Close()
	healthHandler, err := authenticatedHealthHandler(state.Handler())
	if err != nil {
		return fmt.Errorf("configure health authentication: %w", err)
	}

	serviceServer := newServiceHTTPServer(serviceHandler)
	healthServer := newHealthHTTPServer(healthHandler)
	serverResults := make(chan serverResult, 4)
	go serveHTTP("gateway service", serviceServer, serviceListener, serverResults)
	go serveHTTP("gateway health", healthServer, healthListener, serverResults)
	go func() { serverResults <- serverResult{name: "gateway native service", err: nativeService.Serve()} }()
	go func() {
		serverResults <- serverResult{name: "gateway timer", err: timerServer.Serve()}
	}()

	var firstErr error
	completed := 0
	select {
	case <-ctx.Done():
	case result := <-serverResults:
		completed = 1
		if result.err != nil && !errors.Is(result.err, context.Canceled) {
			firstErr = fmt.Errorf("%s server: %w", result.name, result.err)
		} else {
			firstErr = fmt.Errorf("%s server stopped unexpectedly", result.name)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 5*time.Second)
	defer cancel()
	shutdownErr := errors.Join(serviceServer.Shutdown(shutdownCtx), healthServer.Shutdown(shutdownCtx))
	_ = nativeService.Close(make(chan struct{}, 1))
	_ = timerServer.Close(nil)
	for completed < 4 {
		<-serverResults
		completed++
	}
	return errors.Join(firstErr, shutdownErr)
}

func authenticatedHealthHandler(next http.Handler) (http.Handler, error) {
	return healthz.WrapFromEnv(next)
}

func newServiceHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       serviceReadTimeout,
		WriteTimeout:      serviceWriteTimeout,
		IdleTimeout:       serviceIdleTimeout,
	}
}

func newHealthHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       healthReadTimeout,
		WriteTimeout:      healthWriteTimeout,
		IdleTimeout:       healthIdleTimeout,
	}
}

type serverResult struct {
	name string
	err  error
}

func serveHTTP(name string, server *http.Server, listener net.Listener, results chan<- serverResult) {
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	results <- serverResult{name: name, err: err}
}
