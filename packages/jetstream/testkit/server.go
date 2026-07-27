// Package testkit provides embedded JetStream infrastructure for tests.
package testkit

import (
	"fmt"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

type Server struct {
	server     *natsserver.Server
	connection *nats.Conn
	jetStream  nats.JetStreamContext
	url        string
}

func Start(t testing.TB) *Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	})
	if err != nil {
		t.Fatalf("create embedded NATS server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		srv.Shutdown()
		srv.WaitForShutdown()
		t.Fatal("embedded NATS server did not become ready")
	}

	url := fmt.Sprintf("nats://%s", srv.Addr())
	connection, err := nats.Connect(url)
	if err != nil {
		srv.Shutdown()
		srv.WaitForShutdown()
		t.Fatalf("connect to embedded NATS server: %v", err)
	}
	js, err := connection.JetStream()
	if err != nil {
		connection.Close()
		srv.Shutdown()
		srv.WaitForShutdown()
		t.Fatalf("create embedded JetStream context: %v", err)
	}
	server := &Server{server: srv, connection: connection, jetStream: js, url: url}
	t.Cleanup(func() {
		connection.Close()
		srv.Shutdown()
		srv.WaitForShutdown()
	})
	return server
}

func (s *Server) URL() string {
	return s.url
}

func (s *Server) JetStream() nats.JetStreamContext {
	return s.jetStream
}

func (s *Server) AddStream(t testing.TB, cfg *nats.StreamConfig) *nats.StreamInfo {
	t.Helper()
	info, err := s.jetStream.AddStream(cfg)
	if err != nil {
		t.Fatalf("add stream: %v", err)
	}
	return info
}
