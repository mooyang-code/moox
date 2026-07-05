package jobqueue

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	natsserver "github.com/nats-io/nats-server/v2/server"
)

// StartEmbedded starts a private NATS server with JetStream enabled for CloudNode.
func StartEmbedded(ctx context.Context, cfg config.EmbeddedJetStreamConfig) (*Runtime, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Port <= 0 {
		cfg.Port = 4223
	}
	if cfg.StoreDir == "" {
		cfg.StoreDir = "../data/cloudnode/nats"
	}
	if err := os.MkdirAll(cfg.StoreDir, 0o755); err != nil {
		return nil, fmt.Errorf("create embedded nats store dir %s: %w", cfg.StoreDir, err)
	}
	opts := &natsserver.Options{
		Host:      cfg.Host,
		Port:      cfg.Port,
		JetStream: true,
		StoreDir:  cfg.StoreDir,
		NoLog:     true,
		NoSigs:    true,
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("create embedded nats server: %w", err)
	}
	go srv.Start()
	timeout := time.Duration(cfg.StartupTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if !srv.ReadyForConnections(timeout) {
		srv.Shutdown()
		return nil, fmt.Errorf("embedded nats server not ready within %s", timeout)
	}
	rt, err := connectURL(ctx, fmt.Sprintf("nats://%s:%d", cfg.Host, cfg.Port))
	if err != nil {
		srv.Shutdown()
		return nil, err
	}
	rt.server = srv
	return rt, nil
}
