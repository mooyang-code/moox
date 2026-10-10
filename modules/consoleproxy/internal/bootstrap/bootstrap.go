package bootstrap

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/modules/consoleproxy/internal/config"
	"github.com/mooyang-code/moox/modules/consoleproxy/internal/engine"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func Serve(ctx context.Context, cfg config.Config, version, commit string) error {
	state := healthz.NewState("console-proxy", "console-proxy", version, commit)
	state.SnapshotFunc = func(ctx context.Context) healthz.Response {
		rsp := healthz.Base("console-proxy", "console-proxy", version, commit, state.StartedAt, state.Ready())
		dependencies := make(map[string]bool)
		for name, addr := range map[string]string{"admin": cfg.Upstreams.Admin, "web-host": cfg.Upstreams.Web} {
			conn, err := (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext(ctx, "tcp", addr)
			dependencies[name] = err == nil
			if conn != nil {
				_ = conn.Close()
			}
		}
		rsp.Details = map[string]any{"tls_mode": cfg.TLS.Mode, "caddy_version": "v2.11.4", "upstream_tcp_reachable": dependencies}
		return rsp
	}
	handler, err := healthz.WrapFromEnv(healthz.StandardMux(state.Snapshot, promhttp.Handler()))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.Health.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), cfg.Lifecycle.CleanupMargin)
		defer cancel()
		_ = server.Shutdown(cleanup)
		_ = server.Close()
	}()
	e, err := engine.Start(ctx, cfg)
	if err != nil {
		return err
	}
	state.SetReady(true)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
running:
	for {
		select {
		case <-ctx.Done():
			break running
		case err = <-done:
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			break running
		case <-ticker.C:
			probe, cancel := context.WithTimeout(ctx, time.Second)
			state.SetReady(e.CheckTLS(probe) == nil)
			cancel()
		}
	}
	state.SetReady(false)
	// Cancellation of the serve context starts graceful drain; it must not
	// propagate as immediate cancellation of the drain's own timeout.
	return errors.Join(err, e.Stop(context.Background()))
}
