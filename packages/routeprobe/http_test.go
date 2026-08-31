package routeprobe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPProbePreservesHostAndSNIWhileDialingCandidateAddress(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/ping"; got != want {
			t.Errorf("request path = %q, want %q", got, want)
		}
		if got, want := r.Host, "api.example.test"; got != want {
			t.Errorf("request Host = %q, want %q", got, want)
		}
		if r.TLS == nil || r.TLS.ServerName != "api.example.test" {
			t.Errorf("TLS SNI = %q, want api.example.test", r.TLS.ServerName)
		}
		fmt.Fprint(w, `{"ready":true}`)
	}))
	defer server.Close()

	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		t.Fatalf("split test server address: %v", err)
	}
	var dialed string
	probe := HTTPProbe{Config: HTTPProbeConfig{
		Scheme: "https", Path: "/ping", ExpectedStatuses: []int{http.StatusOK},
		TLSConfig: &tls.Config{InsecureSkipVerify: true},
		ResponseValidator: func(response HTTPProbeResponse) error {
			if string(response.Body) != `{"ready":true}` {
				return fmt.Errorf("unexpected body %q", response.Body)
			}
			return nil
		},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = address
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}}
	candidate := Candidate{Transport: TransportHTTPS, Host: "api.example.test", Address: "127.0.0.1", Port: mustPort(t, portText), SourceKey: SourceKey{ProviderID: "binance", SourceID: "spot_http"}}
	result, err := probe.Probe(context.Background(), ProbeRequest{Candidate: candidate})
	if err != nil {
		t.Fatalf("HTTPProbe.Probe() error = %v", err)
	}
	if !result.Success || result.StatusCode != http.StatusOK || result.FirstResponseLatency <= 0 {
		t.Fatalf("unexpected HTTP probe result: %+v", result)
	}
	if dialed != net.JoinHostPort("127.0.0.1", portText) {
		t.Fatalf("dialed %q, want candidate address %q", dialed, net.JoinHostPort("127.0.0.1", portText))
	}
}

func TestHTTPProbeMarksUnexpectedStatusAsRemoteFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("split test server address: %v", err)
	}
	result, err := (HTTPProbe{Config: HTTPProbeConfig{Scheme: "http", ExpectedStatuses: []int{http.StatusOK}}}).Probe(context.Background(), ProbeRequest{Candidate: Candidate{
		Transport: TransportHTTP, Host: host, Address: host, Port: mustPort(t, portText), SourceKey: SourceKey{ProviderID: "binance", SourceID: "spot_http"},
	}})
	if err != nil {
		t.Fatalf("HTTPProbe.Probe() error = %v", err)
	}
	if result.Success || !result.RemoteError || result.ErrorKind != ErrorRemote || result.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("unexpected remote failure result: %+v", result)
	}
}

func TestHTTPProbeRejectsNonHTTPTransport(t *testing.T) {
	result, err := (HTTPProbe{}).Probe(context.Background(), ProbeRequest{Candidate: Candidate{Transport: TransportTCP, Host: "quotes.example", Address: "192.0.2.10", Port: 7709}})
	if !errors.Is(err, ErrUnsupportedTransport) {
		t.Fatalf("HTTPProbe.Probe() error = %v, want unsupported transport", err)
	}
	if result.ErrorKind != ErrorUnsupported || result.Success {
		t.Fatalf("unexpected unsupported result: %+v", result)
	}
}

func mustPort(t *testing.T, value string) int {
	t.Helper()
	var port int
	if _, err := fmt.Sscanf(value, "%d", &port); err != nil {
		t.Fatalf("parse port %q: %v", value, err)
	}
	return port
}
