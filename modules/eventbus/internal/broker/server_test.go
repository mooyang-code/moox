package broker

import (
	"context"
	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestConfigPublicHost(t *testing.T) {
	assert.False(t, configPublicHost("127.0.0.1:4222"))
	assert.False(t, configPublicHost("localhost"))
	assert.True(t, configPublicHost("eventbus.example.com"))
	assert.True(t, configPublicHost("eventbus.example.com:4222"))
}

func TestLoadUsersFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.yaml")
	content := "users:\n  - username: svc\n    password: secret\n    permissions:\n      publish:\n        allow: [\"moox.>\"]\n      subscribe:\n        allow: [\"moox.>\"]\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	users, err := loadUsersFile(path)
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "svc", users[0].Username)
	_, err = loadUsersFile("")
	require.Error(t, err)
}

func TestServerStringAndConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Broker.StoreDir = t.TempDir()
	s := &Server{cfg: cfg}
	assert.Equal(t, "", s.String())
	assert.Equal(t, cfg, s.Config())
}
