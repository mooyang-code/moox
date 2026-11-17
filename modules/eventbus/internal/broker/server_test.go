package broker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// EventBus 的服务端证书就是所在主机的主机证书：MooX CA 签，SAN 是主机 ID、主机地址和回环地址。
func TestTLSServerUsesHostCertificateAndClientsVerifyWithMooXCA(t *testing.T) {
	ca := newTestCA(t)
	certFile, keyFile := ca.hostCertificate(t, "storage", "203.0.113.11")
	c := tlsTestConfig(t, certFile, keyFile, ca.file)
	c.Broker.ClientAdvertise = "203.0.113.11:4222"
	b, err := New(c)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, b.Start(ctx))
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	address, user := "tls://127.0.0.1:"+fmt.Sprint(c.Broker.Port), nats.UserInfo("tester", "secret")

	nc, err := nats.Connect(address, user, nats.RootCAs(ca.file), nats.TLSHandshakeFirst())
	require.NoError(t, err)
	nc.Close()

	// 没有 MooX CA 根证书的客户端、拿着别的 CA 的客户端都连不上。
	_, err = nats.Connect(address, user, nats.Secure(&tls.Config{MinVersion: tls.VersionTLS12}), nats.TLSHandshakeFirst())
	require.Error(t, err)
	_, err = nats.Connect(address, user, nats.RootCAs(newTestCA(t).file), nats.TLSHandshakeFirst())
	require.Error(t, err)

	// 证书按主机 ID、主机地址和回环地址都能通过校验，别的主机名不行。
	pool := x509.NewCertPool()
	pool.AddCert(ca.certificate)
	for name, ok := range map[string]bool{"127.0.0.1": true, "storage": true, "203.0.113.11": true, "control": false, "203.0.113.10": false} {
		conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(c.Broker.Port)), &tls.Config{RootCAs: pool, ServerName: name, MinVersion: tls.VersionTLS12})
		if ok {
			require.NoError(t, err, name)
			require.NoError(t, conn.Close())
		} else {
			require.Error(t, err, name)
		}
	}
}

// 配置里宣告的对外地址不在证书里（拿了别的主机的证书）时 broker 不启动。
func TestTLSServerRejectsAnotherHostsCertificate(t *testing.T) {
	ca := newTestCA(t)
	certFile, keyFile := ca.hostCertificate(t, "control", "203.0.113.10")
	c := tlsTestConfig(t, certFile, keyFile, ca.file)
	c.Broker.ClientAdvertise = "203.0.113.11:4222"
	_, err := New(c)
	require.ErrorContains(t, err, "does not cover advertised host")
}

func tlsTestConfig(t *testing.T, certFile, keyFile, caFile string) *config.Config {
	t.Helper()
	c, err := config.Load("../../config/app.yaml")
	require.NoError(t, err)
	c.Broker.Host, c.Broker.Port, c.Broker.StoreDir = "127.0.0.1", freePort(t), t.TempDir()
	c.Broker.TLS = config.TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, CAFile: caFile}
	c.Broker.Auth = config.AuthConfig{Enabled: true, Username: "tester", Password: "secret"}
	return c
}

// testCA 按 MooX CA 的形状造一张一次性的根证书（ECDSA P-256）。
type testCA struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	file        string
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MooX test CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), "moox-ca.crt")
	require.NoError(t, os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	return &testCA{certificate: certificate, key: key, file: file}
}

// hostCertificate 按主机证书的形状签一张：SAN 是主机 ID、主机地址和回环地址，只有服务端用途。
func (ca *testCA) hostCertificate(t *testing.T, hostID, address string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: hostID},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{hostID, "localhost"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback, net.ParseIP(address)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.certificate, &key.PublicKey, ca.key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "host.crt"), filepath.Join(dir, "host.tls.key")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certFile, keyFile
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
