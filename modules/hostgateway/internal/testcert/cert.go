// Package testcert builds private, disposable certificate fixtures for tests.
package testcert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
)

type Authority struct {
	Certificate *x509.Certificate
	Key         *ecdsa.PrivateKey
	PEM         []byte
}

func New(t testing.TB, mutate func(*x509.Certificate)) *Authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{
		SerialNumber: serial(t), Subject: pkix.Name{CommonName: "disposable MooX test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	if mutate != nil {
		mutate(cert)
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &Authority{Certificate: parsed, Key: key, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func Files(t testing.TB, ca *Authority, hostID string, mutate func(*x509.Certificate)) hostgatewayconfig.TLS {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{
		SerialNumber: serial(t), Subject: pkix.Name{CommonName: hostID}, DNSNames: []string{hostID},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.0.0.42")},
		NotBefore:   now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if mutate != nil {
		mutate(cert)
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca.Certificate, &key.PublicKey, ca.Key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := hostgatewayconfig.TLS{
		CertificateFile: filepath.Join(dir, "server.crt"), KeyFile: filepath.Join(dir, "server.key"), CAFile: filepath.Join(dir, "moox-ca.crt"),
	}
	for path, value := range map[string][]byte{
		paths.CertificateFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		paths.KeyFile:         pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), paths.CAFile: ca.PEM,
	} {
		Write(t, path, value)
	}
	return paths
}

func Write(t testing.TB, path string, value []byte) {
	t.Helper()
	if err := os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
}

func serial(t testing.TB) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	return n.Add(n, big.NewInt(1))
}
