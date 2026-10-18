// Package engine owns one in-process Caddy instance and its bounded lifetime.
package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
	"github.com/caddyserver/certmagic"
	"github.com/mooyang-code/moox/modules/consoleproxy/internal/config"
)

type Engine struct {
	cfg     config.Config
	gate    *admission
	root    *x509.Certificate
	once    sync.Once
	stopErr error
}

func Start(ctx context.Context, cfg config.Config) (_ *Engine, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := Render(cfg)
	if err != nil {
		return nil, err
	}
	var e *Engine
	// Registered before ownership.Unlock, so cleanup uses the same bounded
	// Stop path after releasing the startup mutex. Ownership remains reserved
	// until cleanup succeeds; a failed startup cannot overlap another engine.
	defer func() {
		if err != nil && e != nil {
			err = errors.Join(err, e.Stop(context.Background()))
		}
	}()
	ownership.Lock()
	defer ownership.Unlock()
	if ownership.current != nil {
		return nil, errors.New("console-proxy already owns the process Caddy engine")
	}
	previous, err := prepareCA(cfg)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.TLS.StorageRoot, 0700); err != nil {
		return nil, err
	}
	e = &Engine{cfg: cfg, gate: newAdmission()}
	ownership.current = e.gate
	// Match only necessary CLI initialization; CLI never owns main/signals.
	certmagic.DefaultACME.Agreed = true
	certmagic.UserAgent = "moox-console-proxy embedded-caddy/2.11.4"
	if err = caddy.Load(raw, true); err != nil {
		return nil, fmt.Errorf("load embedded Caddy: %w", err)
	}
	root, err := publishCA(cfg, previous)
	if err != nil {
		return nil, err
	}
	e.root = root
	startup, cancel := context.WithTimeout(ctx, cfg.Lifecycle.StartupTimeout)
	defer cancel()
	if err = waitTLS(startup, cfg, root); err != nil {
		return nil, err
	}
	if cfg.TLS.Mode == "public" {
		for _, name := range []string{"root.crt", "root.sha256"} {
			if err = os.Remove(filepath.Join(cfg.TLS.CAPublishDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	e.gate.allow()
	return e, nil
}

// CheckTLS verifies the configured identity and trust independently of upstream
// HTTP availability. It never bypasses certificate verification.
func (e *Engine) CheckTLS(ctx context.Context) error { return waitTLS(ctx, e.cfg, e.root) }

func waitTLS(ctx context.Context, cfg config.Config, root *x509.Certificate) error {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.Public.Host}
	if cfg.TLS.Mode == "internal" {
		tlsConfig.RootCAs = x509.NewCertPool()
		tlsConfig.RootCAs.AddCert(root)
	}
	host := cfg.Public.Bind
	if net.ParseIP(host).IsUnspecified() {
		if net.ParseIP(host).To4() != nil {
			host = "127.0.0.1"
		} else {
			host = "::1"
		}
	}
	addr := net.JoinHostPort(host, fmt.Sprint(cfg.Public.Port))
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		dial := tls.Dialer{NetDialer: &net.Dialer{Timeout: time.Second}, Config: tlsConfig}
		conn, err := dial.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("TLS not ready for %s: %w", cfg.Public.Host, ctx.Err())
		case <-ticker.C:
		}
	}
}

// Stop first rejects new business requests, then waits for admitted requests.
// At the drain deadline it cancels their contexts. A hung engine is an error;
// the supervising installer owns the final process kill after StopBudget.
func (e *Engine) Stop(ctx context.Context) error {
	e.once.Do(func() {
		ownership.Lock()
		defer ownership.Unlock()
		idle := e.gate.close()
		drain, cancel := context.WithTimeout(ctx, e.cfg.Lifecycle.DrainTimeout)
		select {
		case <-idle:
		case <-drain.Done():
			e.gate.cancel()
		}
		cancel()
		stop, cancel := context.WithTimeout(ctx, e.cfg.Lifecycle.EngineStopTimeout)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- caddy.Stop() }()
		select {
		case err := <-done:
			e.stopErr = err
		case <-stop.Done():
			e.stopErr = fmt.Errorf("stop Caddy: %w", stop.Err())
			return
		}
		select {
		case <-idle:
		case <-stop.Done():
			e.stopErr = errors.Join(e.stopErr, errors.New("active requests did not terminate after cancellation"))
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), e.cfg.Lifecycle.CleanupMargin)
		defer cancel()
		certmagic.CleanUpOwnLocks(cleanup, caddy.Log())
		ownership.current = nil
	})
	return e.stopErr
}
