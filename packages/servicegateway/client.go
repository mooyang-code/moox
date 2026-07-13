// Package servicegateway builds authenticated-service HTTP clients with strict transport security.
package servicegateway

import (
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	CAFileEnv      = "MOOX_SERVICE_GATEWAY_CA_FILE"
	CAPEMBase64Env = "MOOX_SERVICE_GATEWAY_CA_PEM_B64"
)

// NewClient returns a client that allows plaintext only for loopback targets.
func NewClient(timeout time.Duration) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	pem, err := configuredCAPEM()
	if err != nil {
		return nil, err
	}
	if len(pem) > 0 {
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("service gateway CA contains no certificates")
		}
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		transport.TLSClientConfig.RootCAs = roots
	}
	return &http.Client{Timeout: timeout, Transport: secureRoundTripper{next: transport}}, nil
}

func configuredCAPEM() ([]byte, error) {
	if path := strings.TrimSpace(os.Getenv(CAFileEnv)); path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read service gateway CA file: %w", err)
		}
		return pem, nil
	}
	raw := strings.TrimSpace(os.Getenv(CAPEMBase64Env))
	if raw == "" {
		return nil, nil
	}
	pem, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode service gateway CA PEM: %w", err)
	}
	return pem, nil
}

type secureRoundTripper struct{ next http.RoundTripper }

func (t secureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateURL(req.URL); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(req)
}

func validateURL(u *url.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		ip := net.ParseIP(host)
		if ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("service gateway rejects non-loopback HTTP target %q", u.Host)
	default:
		return fmt.Errorf("service gateway URL scheme must be http or https")
	}
}
