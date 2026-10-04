package gatewayauth

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
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
	Timeout     time.Duration
	CAFile      string
	CAPEMBase64 string
}

// NewHTTPClient returns a client that permits plaintext only for loopback targets.
func NewHTTPClient(options ClientOptions) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	caFile := strings.TrimSpace(options.CAFile)
	caMaterial := strings.TrimSpace(options.CAPEMBase64)
	if caFile != "" && caMaterial != "" {
		return nil, errors.New("gateway CA file and CA PEM material are mutually exclusive")
	}
	var pem []byte
	if caFile != "" {
		var err error
		pem, err = os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read gateway CA file: %w", err)
		}
	} else if caMaterial != "" {
		var err error
		pem, err = base64.StdEncoding.Strict().DecodeString(caMaterial)
		if err != nil {
			return nil, errors.New("gateway CA PEM material is not valid base64")
		}
	}
	if len(pem) > 0 {
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
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("gateway HTTP redirects are disabled")
		},
	}, nil
}

// CloseIdleConnections drops keep-alive sockets held by a gateway client.
// Some short-lived local gateway workers close an idle connection from their
// side; the next request can otherwise fail before a fresh socket is opened.
func CloseIdleConnections(client *http.Client) {
	if client == nil || client.Transport == nil {
		return
	}
	if closer, ok := client.Transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
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

func (t secureRoundTripper) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
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
