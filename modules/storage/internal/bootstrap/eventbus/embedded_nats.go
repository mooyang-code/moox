package eventbus

import (
	"fmt"
	"os"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	natsserver "github.com/nats-io/nats-server/v2/server"
)

type EmbeddedServer interface {
	Close() error
}

type embeddedNATSServer struct {
	server *natsserver.Server
}

func StartEmbeddedServer(cfg storageconfig.StorageEventBus) (EmbeddedServer, error) {
	if cfg.Type != "nats" || !cfg.Embedded.Enabled {
		return nil, nil
	}
	if err := os.MkdirAll(cfg.Embedded.StoreDir, 0o755); err != nil {
		return nil, fmt.Errorf("create embedded nats store dir %s: %w", cfg.Embedded.StoreDir, err)
	}
	opts := &natsserver.Options{
		Host:      cfg.Embedded.Host,
		Port:      cfg.Embedded.Port,
		JetStream: true,
		StoreDir:  cfg.Embedded.StoreDir,
		NoSigs:    true,
		NoLog:     true,
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("create embedded nats server: %w", err)
	}
	go srv.Start()

	timeout := time.Duration(cfg.Embedded.StartupTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if !srv.ReadyForConnections(timeout) {
		srv.Shutdown()
		return nil, fmt.Errorf("embedded nats server not ready within %s", timeout)
	}
	return &embeddedNATSServer{server: srv}, nil
}

func (s *embeddedNATSServer) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	s.server.Shutdown()
	s.server.WaitForShutdown()
	return nil
}
