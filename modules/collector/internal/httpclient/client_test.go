package httpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPClientGetWithIPsReusesTLSConnection(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	client := NewHTTPClient(server.Client())
	for i := 0; i < 3; i++ {
		var result map[string]string
		require.NoError(t, client.GetWithIPs(context.Background(), parsed.Host, []string{"127.0.0.1"}, "/", nil, &result))
	}
	require.Equal(t, int32(1), connections.Load())
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var result map[string]string
			assert.NoError(t, client.GetWithIPs(context.Background(), parsed.Host, []string{"127.0.0.1"}, "/", nil, &result))
		}()
	}
	wg.Wait()
}

func TestHTTPClientIPClientCacheConcurrentAndBounded(t *testing.T) {
	client := NewHTTPClient()
	want := client.clientForIP("127.0.0.1")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.Same(t, want, client.clientForIP("127.0.0.1"))
		}()
	}
	wg.Wait()
	for i := 1; i <= maxIPClients; i++ {
		require.NotNil(t, client.clientForIP(fmt.Sprintf("192.0.2.%d", i)))
	}
	require.Len(t, client.ipClients, maxIPClients)
	require.Len(t, client.ipOrder, maxIPClients)
	require.NotSame(t, want, client.clientForIP("127.0.0.1"), "oldest client must be evicted")
	require.Len(t, client.ipClients, maxIPClients)
}

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

func TestHTTPClientGetWithIPsTriesNextAddressAfterFirstTimeout(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var result map[string]string
	require.NoError(t, client.GetWithIPs(ctx, parsed.Host, []string{"192.0.2.1", "127.0.0.1"}, parsed.Path, nil, &result))
	assert.Equal(t, parsed.Host, result["host"])
}
