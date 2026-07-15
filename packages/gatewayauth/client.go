package gatewayauth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type ClientOptions struct {
	Timeout time.Duration
	CAFile  string
}

// NewHTTPClient returns a client that permits plaintext only for loopback targets.
func NewHTTPClient(options ClientOptions) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caFile := strings.TrimSpace(options.CAFile); caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read gateway CA file: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("gateway CA file contains no certificates")
		}
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{RootCAs: roots}
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.RootCAs = roots
		}
	}
	return &http.Client{
		Timeout:   options.Timeout,
		Transport: secureRoundTripper{next: transport},
	}, nil
}

type secureRoundTripper struct {
	next http.RoundTripper
}

func (t secureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateURL(req.URL); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(req)
}

func validateURL(target *url.URL) error {
	switch strings.ToLower(target.Scheme) {
	case "https":
		return nil
	case "http":
		host := target.Hostname()
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		ip := net.ParseIP(host)
		if ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("gateway rejects non-loopback HTTP target %q", target.Host)
	default:
		return fmt.Errorf("gateway URL scheme must be http or https")
	}
}
