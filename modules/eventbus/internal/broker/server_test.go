package broker

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerStartsJetStreamAndShutsDown(t *testing.T) {
	c, err := config.Load("../../config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
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

func TestTLSServerAndClientUseHandshakeFirst(t *testing.T) {
	certFile, keyFile, caFile := writeTestTLSBundle(t)
	c, err := config.Load("../../config/app.yaml")
	require.NoError(t, err)
	c.Broker.Host = "127.0.0.1"
	c.Broker.Port = freePort(t)
	c.Broker.StoreDir = t.TempDir()
	c.Broker.TLS.Enabled = true
	c.Broker.TLS.CertFile = certFile
	c.Broker.TLS.KeyFile = keyFile
	c.Broker.TLS.CAFile = caFile
	b, err := New(c)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, b.Start(ctx))
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })

	nc, err := nats.Connect(
		"tls://127.0.0.1:"+fmt.Sprint(c.Broker.Port),
		nats.RootCAs(caFile),
		nats.TLSHandshakeFirst(),
	)
	require.NoError(t, err)
	nc.Close()

	pool := x509.NewCertPool()
	caPEM, err := os.ReadFile(caFile)
	require.NoError(t, err)
	require.True(t, pool.AppendCertsFromPEM(caPEM))
	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(c.Broker.Port)), &tls.Config{
		RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12,
	})
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func writeTestTLSBundle(t *testing.T) (string, string, string) {
	t.Helper()
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "eventbus-test"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	require.NoError(t, err)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.pem")
	keyFile := filepath.Join(dir, "server-key.pem")
	caFile := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)}), 0o600))
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600))
	return certFile, keyFile, caFile
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
