package jetstream

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// EventBus 的服务端证书是所在主机的主机证书（MooX CA 签，SAN 是主机 ID、主机地址与回环地址），
// 客户端按角色文件里的 ca_file（MooX CA 根证书）校验。
func TestConnectVerifiesHostCertificateWithMooXCA(t *testing.T) {
	ca := newMooXTestCA(t)
	secrets := t.TempDir()
	writePrivate(t, filepath.Join(secrets, "moox-ca.crt"), ca.pem)
	if err := os.Mkdir(filepath.Join(secrets, "eventbus"), 0o700); err != nil {
		t.Fatal(err)
	}
	role := filepath.Join(secrets, "eventbus", "strategy-eventbus.yaml")
	writePrivate(t, role, []byte("version: 1\nusername: strategy-eventbus\ntoken: secret\nca_file: ../moox-ca.crt\n"))

	connect := func(port int, mutate func(*Config)) error {
		cfg := Config{URLs: []string{fmt.Sprintf("tls://127.0.0.1:%d", port)}, Name: "tls-test", ConnectTimeout: time.Second, ReconnectWait: 20 * time.Millisecond}
		if err := cfg.ApplyCredentialFile(role); err != nil {
			t.Fatal(err)
		}
		if mutate != nil {
			mutate(&cfg)
		}
		client, err := Connect(context.Background(), cfg)
		if err == nil {
			_ = client.Close()
		}
		return err
	}

	// 主机证书：能连。
	port := startTLSTestServer(t, ca.hostCertificate(t, "storage"))
	if err := connect(port, nil); err != nil {
		t.Fatalf("connect with MooX CA: %v", err)
	}
	// 客户端拿的是别的 CA：连不上。
	other := filepath.Join(t.TempDir(), "other-ca.crt")
	writePrivate(t, other, newMooXTestCA(t).pem)
	if err := connect(port, func(cfg *Config) { cfg.TLSCAFile = other }); !errors.Is(err, ErrConnection) {
		t.Fatalf("connect with another CA = %v, want connection error", err)
	}
	// 服务端的证书是别的 CA 签的：连不上。
	port = startTLSTestServer(t, newMooXTestCA(t).hostCertificate(t, "storage"))
	if err := connect(port, nil); !errors.Is(err, ErrConnection) {
		t.Fatalf("connect to a server signed by another CA = %v, want connection error", err)
	}
	// 服务端的证书没有客户端所连的地址：连不上。
	port = startTLSTestServer(t, ca.certificate(t, "control", []string{"control"}, []net.IP{net.ParseIP("203.0.113.10")}))
	if err := connect(port, nil); !errors.Is(err, ErrConnection) {
		t.Fatalf("connect to a certificate without the dialed address = %v, want connection error", err)
	}
}

func startTLSTestServer(t *testing.T, certificate tls.Certificate) int {
	t.Helper()
	port := reserveTestPort(t)
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: port, NoLog: true, NoSigs: true, Username: "strategy-eventbus", Password: "secret",
		TLS: true, TLSHandshakeFirst: true, TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12},
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		srv.Shutdown()
		t.Fatal("nats server not ready")
	}
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })
	return port
}

// mooxTestCA 按 MooX CA 的形状造一张一次性的根证书（ECDSA P-256）。
type mooxTestCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newMooXTestCA(t *testing.T) *mooxTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MooX test CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &mooxTestCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// hostCertificate 按主机证书的形状签一张：SAN 是主机 ID 与回环地址。
func (ca *mooxTestCA) hostCertificate(t *testing.T, hostID string) tls.Certificate {
	return ca.certificate(t, hostID, []string{hostID, "localhost"}, []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback})
}

func (ca *mooxTestCA) certificate(t *testing.T, commonName string, names []string, ips []net.IP) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: commonName},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: names, IPAddresses: ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func writePrivate(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
