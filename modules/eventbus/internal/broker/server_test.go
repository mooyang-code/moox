package broker

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	"github.com/nats-io/nats.go"
)

func TestServerStartsJetStreamAndShutsDown(t *testing.T) {
	c := config.Default()
	c.Broker.StoreDir = t.TempDir()
	c.Broker.Port = freePort(t)
	c.Broker.ServerName = "eventbus-test"
	b, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !b.Ready() {
		t.Fatal("broker is not ready")
	}
	nc, err := nats.Connect(b.URL())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nc.JetStream(); err != nil {
		t.Fatal(err)
	}
	_ = nc.Drain()
	nc.Close()
	if err := b.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if b.Ready() {
		t.Fatal("broker remains ready after shutdown")
	}
}

func TestServerRejectsInvalidTLS(t *testing.T) {
	c := config.Default()
	c.Broker.StoreDir = t.TempDir()
	c.Broker.TLS.Enabled = true
	c.Broker.TLS.CertFile = "missing"
	c.Broker.TLS.KeyFile = "missing"
	if _, err := New(c); err == nil {
		t.Fatal("New returned nil for invalid TLS")
	}
}

func TestClusterServerCopiesRouteCredentials(t *testing.T) {
	c := config.Default()
	c.Broker.Cluster.Enabled = true
	c.Broker.Auth.Enabled = true
	c.Broker.Auth.Username = "route-user"
	c.Broker.Auth.Password = "route-password"
	username, password := clusterCredentials(c)
	if username != "route-user" || password != "route-password" {
		t.Fatalf("cluster credentials = %q/%q", username, password)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
