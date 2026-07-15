// Package bootstrap wires the standalone gateway and owns its route lifecycle.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
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
	NodeID  string
	Routes  routeStore
	Control controlClient
	Health  *health.State
}

type Runtime struct {
	nodeID  string
	routes  routeStore
	control controlClient
	health  *health.State
	table   gatewayproxy.Table
	mu      sync.Mutex
}

func New(options Options) *Runtime {
	return &Runtime{nodeID: options.NodeID, routes: options.Routes, control: options.Control, health: options.Health}
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
		if hasCache {
			runtime.report(ctx, err.Error())
			return nil
		}
		return fmt.Errorf("initial route pull failed without a valid cache: %w", err)
	}
	if err := runtime.apply(snapshot, true); err != nil {
		runtime.health.RouteSyncFailed()
		if hasCache {
			runtime.report(ctx, err.Error())
			return nil
		}
		return fmt.Errorf("apply initial route snapshot: %w", err)
	}
	runtime.report(ctx, "")
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
		runtime.report(ctx, err.Error())
		return err
	}
	runtime.report(ctx, "")
	return nil
}

func (runtime *Runtime) apply(snapshot gatewayproxy.Snapshot, persist bool) error {
	if snapshot.NodeID != runtime.nodeID {
		return fmt.Errorf("route snapshot targets %q, want %q", snapshot.NodeID, runtime.nodeID)
	}
	var validated gatewayproxy.Table
	if err := validated.Replace(snapshot); err != nil {
		return err
	}
	if persist {
		if err := runtime.routes.Save(snapshot); err != nil {
			return fmt.Errorf("save route snapshot: %w", err)
		}
	}
	if err := runtime.table.Replace(snapshot); err != nil {
		return err
	}
	runtime.health.ApplyRoutes(snapshot.RouteHash, len(snapshot.Routes), snapshot.Disabled)
	return nil
}

func (runtime *Runtime) report(ctx context.Context, lastError string) {
	hash, count := runtime.health.Current()
	_ = runtime.control.Report(ctx, hash, int32(count), lastError)
}

func Run(ctx context.Context, cfg config.Config) error {
	control, err := controlplane.New(controlplane.Options{NodeID: cfg.Node.ID, BaseURL: cfg.ControlPlane.BaseURL, HMACKeyFile: cfg.ControlPlane.HMACKeyFile, CAFile: cfg.ControlPlane.CAFile})
	if err != nil {
		return err
	}
	credentialsSecret, err := config.ReadSecret(cfg.Auth.HMACKeyFile)
	if err != nil {
		return fmt.Errorf("read service authentication key: %w", err)
	}
	nonces, err := store.OpenNonces(filepath.Join(cfg.Store.Path, "nonces"))
	if err != nil {
		return err
	}
	defer nonces.Close()
	state := health.NewState()
	runtime := New(Options{NodeID: cfg.Node.ID, Routes: store.NewRoutes(cfg.Store.Path), Control: control, Health: state})
	if err := runtime.Initialize(ctx); err != nil {
		return err
	}

	serviceHandler := router.New(router.Options{
		NodeID: cfg.Node.ID, Credentials: gatewayauth.Credentials{KeyID: "moox-gateway-service", Secret: credentialsSecret},
		MaxBodyBytes: cfg.Proxy.MaxBodyBytes, Table: runtime.Table(), Nonces: nonces, Disabled: state.Disabled,
	})
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

	serviceServer := &http.Server{Handler: serviceHandler, ReadHeaderTimeout: 5 * time.Second}
	healthServer := &http.Server{Handler: state.Handler(), ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 2)
	go serve(serviceServer, serviceListener, serverErrors)
	go serve(healthServer, healthListener, serverErrors)
	ticker := time.NewTicker(cfg.ControlPlane.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = serviceServer.Shutdown(shutdownCtx)
			_ = healthServer.Shutdown(shutdownCtx)
			return nil
		case err := <-serverErrors:
			return err
		case <-ticker.C:
			_ = runtime.Refresh(ctx)
		}
	}
}

func serve(server *http.Server, listener net.Listener, errorsOut chan<- error) {
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errorsOut <- err
	}
}
