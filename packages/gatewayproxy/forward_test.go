package gatewayproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func routeForServer(t *testing.T, server *httptest.Server) Route {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return Route{ServiceID: "admin", Address: u.Host, ServicePath: "trpc.moox.Admin", TimeoutMS: 1000, MaxBodyBytes: 1024}
}

func TestForwardProxiesRequestAndPreservesAllowedResponse(t *testing.T) {
	var gotHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		if r.Method != http.MethodPost || r.URL.Path != "/trpc.moox.Admin/GetStatus" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"node":"one"}` {
			t.Errorf("body = %q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("trpc-ret", "23")
		w.Header().Set("trpc-func-ret", "failed")
		w.Header().Set("X-Trace-Id", "upstream-trace")
		w.Header().Set("Set-Cookie", "secret=1")
		w.WriteHeader(http.StatusTeapot)
		zw := gzip.NewWriter(w)
		_, _ = zw.Write([]byte(`{"ok":false}`))
		_ = zw.Close()
	}))
	defer server.Close()

	headers := http.Header{
		"Content-Type":        {"application/json"},
		"Accept-Encoding":     {"gzip"},
		"X-Trace-Id":          {"client-trace"},
		"X-Space-Id":          {"space-1"},
		"Authorization":       {"Bearer secret"},
		"X-Moox-Service-Auth": {"internal-secret"},
		"X-User-Id":           {"user-1"},
		"User-Agent":          {"browser-secret"},
	}
	response, err := Forward(context.Background(), nil, routeForServer(t, server), "GetStatus", []byte(`{"node":"one"}`), headers)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusTeapot || !bytes.HasPrefix(response.Body, []byte{0x1f, 0x8b}) {
		t.Fatalf("response = %+v", response)
	}
	for key, want := range map[string]string{"Content-Type": "application/json", "Content-Encoding": "gzip", "trpc-ret": "23", "trpc-func-ret": "failed", "X-Trace-Id": "upstream-trace"} {
		if got := response.Header.Get(key); got != want {
			t.Errorf("response header %s = %q, want %q", key, got, want)
		}
	}
	if response.Header.Get("Set-Cookie") != "" {
		t.Fatal("unsafe response header was preserved")
	}
	for _, key := range []string{"Content-Type", "Accept-Encoding", "X-Trace-Id", "X-Space-Id"} {
		if gotHeader.Get(key) != headers.Get(key) {
			t.Errorf("request header %s = %q", key, gotHeader.Get(key))
		}
	}
	for _, key := range []string{"Authorization", "X-Moox-Service-Auth", "X-User-Id", "User-Agent"} {
		if gotHeader.Get(key) != "" {
			t.Errorf("unsafe request header %s forwarded", key)
		}
	}
}

func TestForwardRejectsInvalidMethodAndOversizedBodyBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	route := routeForServer(t, server)
	route.MaxBodyBytes = 3
	for _, test := range []struct {
		method string
		body   []byte
	}{
		{method: "../Status", body: nil},
		{method: "Get/Status", body: nil},
		{method: "GetStatus", body: []byte("four")},
	} {
		if _, err := Forward(context.Background(), nil, route, test.method, test.body, nil); err == nil {
			t.Fatalf("Forward(%q, %q) succeeded", test.method, test.body)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("made %d upstream requests", requests.Load())
	}
}

func TestForwardRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "12345") }))
	defer server.Close()
	route := routeForServer(t, server)
	route.MaxBodyBytes = 4
	if _, err := Forward(context.Background(), nil, route, "GetStatus", nil, nil); err == nil || !strings.Contains(err.Error(), "response body") {
		t.Fatalf("error = %v", err)
	}
}

func TestForwardTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, "late")
	}))
	defer server.Close()
	route := routeForServer(t, server)
	route.TimeoutMS = 10
	_, err := Forward(context.Background(), nil, route, "GetStatus", nil, nil)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestForwardReturnsUpstreamTransportErrors(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	route := Route{ServiceID: "admin", Address: address, ServicePath: "trpc.moox.Admin", TimeoutMS: 100, MaxBodyBytes: 1024}
	_, err = Forward(context.Background(), nil, route, "GetStatus", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "send upstream request") {
		t.Fatalf("error = %v", err)
	}
}

func TestForwardDoesNotInjectCookiesFromSuppliedClientJar(t *testing.T) {
	var gotCookie string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		gotCookie = request.Header.Get("Cookie")
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(serverURL, []*http.Cookie{{Name: "session", Value: "secret"}})

	if _, err := Forward(context.Background(), &http.Client{Jar: jar}, routeForServer(t, server), "GetStatus", nil, nil); err != nil {
		t.Fatal(err)
	}
	if gotCookie != "" {
		t.Fatalf("upstream Cookie = %q, want empty", gotCookie)
	}
}

func TestForwardIgnoresSuppliedClientTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "direct")
	}))
	defer server.Close()
	var customTransportCalls atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		customTransportCalls.Add(1)
		return nil, errors.New("custom transport must not run")
	})}

	response, err := Forward(context.Background(), client, routeForServer(t, server), "GetStatus", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Body) != "direct" {
		t.Fatalf("body = %q, want direct", response.Body)
	}
	if customTransportCalls.Load() != 0 {
		t.Fatalf("custom transport called %d times", customTransportCalls.Load())
	}
}

func TestForwardReturnsRedirectWithoutFollowingIt(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	response, err := Forward(context.Background(), nil, routeForServer(t, redirect), "GetStatus", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusTemporaryRedirect)
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target received %d requests", targetRequests.Load())
	}
}

func TestForwardReusesSharedHardenedTransportConnections(t *testing.T) {
	var newConnections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "ok")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	route := routeForServer(t, server)

	for range 2 {
		if _, err := Forward(context.Background(), nil, route, "GetStatus", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if newConnections.Load() != 1 {
		t.Fatalf("new connections = %d, want 1", newConnections.Load())
	}
}

func TestValidatedLoopbackAddressesAreForwardable(t *testing.T) {
	for _, test := range []struct {
		name    string
		network string
		address string
	}{
		{name: "IPv4", network: "tcp4", address: "127.0.0.1:0"},
		{name: "IPv6", network: "tcp6", address: "[::1]:0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen(test.network, test.address)
			if err != nil {
				if test.network == "tcp6" {
					t.Skipf("IPv6 loopback unavailable: %v", err)
				}
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(writer, "ok")
			}))
			server.Listener = listener
			server.Start()
			defer server.Close()
			route := routeForServer(t, server)
			if err := ValidateRoute(route); err != nil {
				t.Fatalf("ValidateRoute: %v", err)
			}
			response, err := Forward(context.Background(), nil, route, "GetStatus", nil, nil)
			if err != nil {
				t.Fatalf("Forward: %v", err)
			}
			if string(response.Body) != "ok" {
				t.Fatalf("body = %q, want ok", response.Body)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
