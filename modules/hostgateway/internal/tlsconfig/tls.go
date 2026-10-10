// Package tlsconfig validates the persisted MooX trust root and this host's
// identity before any listeners are opened. It never creates certificates.
package tlsconfig

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
)

// Material owns an already validated certificate/key pair. The running server
// uses these bytes, rather than reopening files after validation.
type Material struct {
	hostID      string
	certificate tls.Certificate
	roots       *x509.CertPool
	fingerprint string
}

func Load(hostID string, paths hostgatewayconfig.TLS) (*Material, error) {
	return loadAt(hostID, paths, time.Now())
}

func loadAt(hostID string, paths hostgatewayconfig.TLS, now time.Time) (*Material, error) {
	if !servicecatalog.ValidHostID(hostID) {
		return nil, errors.New("TLS host ID must be canonical")
	}
	caPEM, err := readFile(paths.CAFile, false, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("read MooX CA: %w", err)
	}
	root, err := certificate(caPEM)
	if err != nil || !root.IsCA || !root.BasicConstraintsValid || root.KeyUsage&x509.KeyUsageCertSign == 0 ||
		!bytes.Equal(root.RawSubject, root.RawIssuer) || root.CheckSignatureFrom(root) != nil ||
		now.Before(root.NotBefore) || !now.Before(root.NotAfter) {
		return nil, errors.New("MooX CA must contain exactly one valid, self-signed CA certificate")
	}
	certPEM, err := readFile(paths.CertificateFile, false, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("read host certificate: %w", err)
	}
	leaf, err := certificate(certPEM)
	if err != nil || leaf.IsCA || !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) ||
		leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 || leaf.NotAfter.After(root.NotAfter) ||
		now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("host certificate must contain exactly one current server certificate within the CA lifetime")
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, DNSName: hostID, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return nil, errors.New("host certificate must match this host ID and be signed by the MooX CA")
	}
	keyPEM, err := readFile(paths.KeyFile, true, 64<<10)
	if err != nil {
		return nil, fmt.Errorf("read host private key: %w", err)
	}
	defer clear(keyPEM)
	block, rest := pem.Decode(bytes.TrimSpace(keyPEM))
	if block == nil || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 ||
		(block.Type != "PRIVATE KEY" && block.Type != "EC PRIVATE KEY" && block.Type != "RSA PRIVATE KEY") ||
		!bytes.HasPrefix(bytes.TrimSpace(keyPEM), []byte("-----BEGIN "+block.Type+"-----")) {
		return nil, errors.New("host private key must contain exactly one unencrypted private key PEM block")
	}
	defer clear(block.Bytes)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, errors.New("host private key is invalid or does not match the certificate")
	}
	pair.Leaf = leaf
	digest := sha256.Sum256(root.Raw)
	return &Material{hostID: hostID, certificate: pair, roots: roots, fingerprint: hex.EncodeToString(digest[:])}, nil
}

func certificate(encoded []byte) (*x509.Certificate, error) {
	encoded = bytes.TrimSpace(encoded)
	// pem.Decode skips leading non-PEM text; require an exact certificate file.
	if !bytes.HasPrefix(encoded, []byte("-----BEGIN CERTIFICATE-----")) {
		return nil, errors.New("certificate PEM required")
	}
	block, rest := pem.Decode(encoded)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("exactly one certificate PEM block required")
	}
	return x509.ParseCertificate(block.Bytes)
}

func (m *Material) HostID() string      { return m.hostID }
func (m *Material) Fingerprint() string { return m.fingerprint }

// Server uses TLS for server authentication. Caller authentication is HMAC at
// the RPC layer; installing the CA as ClientCAs would incorrectly require mTLS.
func (m *Material) Server() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{m.certificate}}
}

// Client never consults the OS trust store. Its server name is the canonical
// host ID, even when the TCP connection uses a public or private IP address.
func (m *Material) Client(hostID string) (*tls.Config, error) {
	if !servicecatalog.ValidHostID(hostID) || strings.TrimSpace(hostID) != hostID {
		return nil, errors.New("TLS peer host ID must be canonical")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: m.roots.Clone(), ServerName: hostID}, nil
}
