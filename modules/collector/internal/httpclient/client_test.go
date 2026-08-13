package httpclient

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPClientGetUsesDomainDirectly(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	var result map[string]string
	require.NoError(t, NewHTTPClient(server.Client()).Get(context.Background(), parsed.Host, parsed.Path, nil, &result))
	assert.Equal(t, "ok", result["status"])
}

func TestHTTPClientGetRejectsNonOK(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	err = NewHTTPClient(server.Client()).Get(context.Background(), parsed.Host, parsed.Path, nil, &map[string]string{})
	statusErr := &StatusError{}
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, http.StatusBadGateway, statusErr.StatusCode)
}

func TestHTTPClientGetWithIPsPreservesHostnameRequest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "" {
			t.Fatalf("request host is empty")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"host": r.Host})
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	var result map[string]string
	require.NoError(t, NewHTTPClient(server.Client()).GetWithIPs(context.Background(), parsed.Host, []string{"127.0.0.1"}, parsed.Path, nil, &result))
	assert.Equal(t, parsed.Host, result["host"])
}

func TestHTTPClientGetWithIPsFallsBackToHostnameAfterIPFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"host": r.Host})
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	// httptest binds to 127.0.0.1. The neighbouring loopback address is not
	// serving this port. Bound the test so platform-specific loopback routing
	// cannot hold the suite for the default five-second HTTP timeout.
	var result map[string]string
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	require.NoError(t, NewHTTPClient(server.Client()).GetWithIPs(
		ctx, parsed.Host, []string{"127.0.0.2"}, parsed.Path, nil, &result,
	))
	assert.Equal(t, parsed.Host, result["host"])
}

func TestHTTPClientGetWithIPsReservesTimeForHostnameAfterIPTimeout(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"host": r.Host})
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	base, ok := server.Client().Transport.(*http.Transport)
	require.True(t, ok)
	transport := base.Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if strings.HasPrefix(address, "192.0.2.1:") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	client := NewHTTPClient(&http.Client{Transport: transport})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var result map[string]string
	started := time.Now()
	require.NoError(t, client.GetWithIPs(ctx, parsed.Host, []string{"192.0.2.1"}, parsed.Path, nil, &result))
	assert.Equal(t, parsed.Host, result["host"])
	assert.Less(t, time.Since(started), time.Second, "hostname fallback should receive a reserved portion of the request deadline")
}
