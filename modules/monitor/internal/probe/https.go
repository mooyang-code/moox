package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
)

func httpsClient(check domain.Check, timeout time.Duration) (*http.Client, func(), error) {
	configuration := domain.HTTPSConfig{URL: check.URL, ConnectAddress: check.ConnectAddress, ServerName: check.ServerName, TrustMode: check.TrustMode, CAFile: check.CAFile, CABaseline: check.CABaseline}
	if err := configuration.Validate(); err != nil {
		return nil, nil, err
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: check.ServerName}
	if check.TrustMode == "internal" {
		root, err := verifiedRoot(check.CAFile, check.CABaseline)
		if err != nil {
			return nil, nil, err
		}
		tlsConfig.RootCAs = x509.NewCertPool()
		tlsConfig.RootCAs.AddCert(root)
	}
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		TLSClientConfig: tlsConfig, TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, check.ConnectAddress)
		},
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: stopRedirect}, transport.CloseIdleConnections, nil
}

func stopRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func verifiedRoot(caFile, baselineFile string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read HTTPS probe CA: %w", err)
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return nil, fmt.Errorf("HTTPS probe CA must contain exactly one PEM certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse HTTPS probe CA: %w", err)
	}
	if !certificate.IsCA || certificate.CheckSignatureFrom(certificate) != nil {
		return nil, fmt.Errorf("HTTPS probe CA must be a self-signed root")
	}
	baseline, err := os.ReadFile(baselineFile)
	if err != nil {
		return nil, fmt.Errorf("read verified HTTPS probe CA baseline: %w", err)
	}
	expected, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(string(baseline)), ":", ""))
	hash := sha256.Sum256(certificate.Raw)
	if err != nil || len(expected) != len(hash) || !bytes.Equal(expected, hash[:]) {
		return nil, fmt.Errorf("HTTPS probe CA does not match the deployment-verified fingerprint")
	}
	return certificate, nil
}
