package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/gateway/internal/bootstrap"
	"github.com/mooyang-code/moox/modules/gateway/internal/controlplane"
	"github.com/mooyang-code/moox/modules/gateway/internal/health"
	"github.com/mooyang-code/moox/modules/gateway/internal/router"
	"github.com/mooyang-code/moox/modules/gateway/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

const (
	testNodeID        = "gateway-test-a"
	testControlSecret = "gateway-control-e2e-secret"
	testServiceSecret = "gateway-service-e2e-secret"
)

func TestGatewayControlAndForwardingLifecycle(t *testing.T) {
	firstUpstream := newTRPCUpstream(t, "first")
	defer firstUpstream.Close()
	secondUpstream := newTRPCUpstream(t, "second")
	defer secondUpstream.Close()

	control := newFakeAdmin(t, snapshotFor(t, testNodeID, firstUpstream.URL, false))
	defer control.Close()
	cacheDirectory := t.TempDir()

	gateway := startGateway(t, control.URL, cacheDirectory)
	status, body := callGateway(t, gateway.URL, testNodeID)
	if status != http.StatusOK || body != `{"upstream":"first"}` {
		t.Fatalf("initial forward = HTTP %d %s", status, body)
	}
	if control.pullCount() == 0 || control.reportCount() == 0 {
		t.Fatalf("control calls: pulls=%d reports=%d", control.pullCount(), control.reportCount())
	}

	t.Run("wrong target node is rejected", func(t *testing.T) {
		status, _ := callGateway(t, gateway.URL, "gateway-test-b")
		if status != http.StatusUnauthorized {
			t.Fatalf("wrong-node request = HTTP %d, want 401", status)
		}
	})

	t.Run("invalid replacement is atomic", func(t *testing.T) {
		invalid := snapshotFor(t, testNodeID, secondUpstream.URL, false)
		invalid.RouteHash = strings.Repeat("0", 64)
		control.setSnapshot(invalid)
		if err := gateway.runtime.Refresh(context.Background()); err == nil {
			t.Fatal("Refresh accepted a hash-mismatched snapshot")
		}
		status, body := callGateway(t, gateway.URL, testNodeID)
		if status != http.StatusOK || body != `{"upstream":"first"}` {
			t.Fatalf("failed refresh changed route: HTTP %d %s", status, body)
		}
	})

	t.Run("valid replacement is applied as one snapshot", func(t *testing.T) {
		control.setSnapshot(snapshotFor(t, testNodeID, secondUpstream.URL, false))
		if err := gateway.runtime.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		status, body := callGateway(t, gateway.URL, testNodeID)
		if status != http.StatusOK || body != `{"upstream":"second"}` {
			t.Fatalf("replacement route = HTTP %d %s", status, body)
		}
	})

	t.Run("admin outage retains live route", func(t *testing.T) {
		control.setOutage(true)
		if err := gateway.runtime.Refresh(context.Background()); err == nil {
			t.Fatal("Refresh during outage returned nil")
		}
		status, body := callGateway(t, gateway.URL, testNodeID)
		if status != http.StatusOK || body != `{"upstream":"second"}` {
			t.Fatalf("outage forward = HTTP %d %s", status, body)
		}
	})

	gateway.Close()
	t.Run("cached restart survives admin outage", func(t *testing.T) {
		gateway = startGateway(t, control.URL, cacheDirectory)
		status, body := callGateway(t, gateway.URL, testNodeID)
		if status != http.StatusOK || body != `{"upstream":"second"}` {
			t.Fatalf("cached restart forward = HTTP %d %s", status, body)
		}
	})
	defer gateway.Close()

	t.Run("disabled node rejects service traffic", func(t *testing.T) {
		control.setOutage(false)
		control.setSnapshot(snapshotFor(t, testNodeID, secondUpstream.URL, true))
		if err := gateway.runtime.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh disabled snapshot: %v", err)
		}
		status, _ := callGateway(t, gateway.URL, testNodeID)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("disabled gateway = HTTP %d, want 503", status)
		}
	})
}

type gatewayHarness struct {
	URL     string
	runtime *bootstrap.Runtime
	server  *httptest.Server
	nonces  *store.Nonces
}

func (gateway *gatewayHarness) Close() {
	if gateway == nil {
		return
	}
	gateway.server.Close()
	_ = gateway.nonces.Close()
}

func startGateway(t *testing.T, controlURL, cacheDirectory string) *gatewayHarness {
	t.Helper()
	controlKey := filepath.Join(cacheDirectory, "control.key")
	if _, err := os.Stat(controlKey); os.IsNotExist(err) {
		if err := os.WriteFile(controlKey, []byte(testControlSecret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	client, err := controlplane.New(controlplane.Options{NodeID: testNodeID, BaseURL: controlURL, HMACKeyFile: controlKey})
	if err != nil {
		t.Fatalf("new control client: %v", err)
	}
	state := health.NewState()
	routes := store.NewRoutes(filepath.Join(cacheDirectory, "routes"))
	runtime := bootstrap.New(bootstrap.Options{NodeID: testNodeID, Routes: routes, Control: client, Health: state})
	if err := runtime.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize gateway: %v", err)
	}
	nonces, err := store.OpenNonces(filepath.Join(cacheDirectory, "nonces"))
	if err != nil {
		t.Fatalf("open nonce store: %v", err)
	}
	handler := router.New(router.Options{
		NodeID:       testNodeID,
		Credentials:  gatewayauth.Credentials{KeyID: "moox-gateway-service", Secret: testServiceSecret},
		MaxBodyBytes: 4 << 20,
		Table:        runtime.Table(),
		Nonces:       nonces,
		Disabled:     state.Disabled,
	})
	server := httptest.NewServer(handler)
	return &gatewayHarness{URL: server.URL, runtime: runtime, server: server, nonces: nonces}
}

func callGateway(t *testing.T, baseURL, targetNode string) (int, string) {
	t.Helper()
	body := []byte(`{"message":"hello"}`)
	endpoint := baseURL + "/api/service/echo/Echo"
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	headers, err := gatewayauth.Sign(
		gatewayauth.Credentials{KeyID: "moox-gateway-service", Secret: testServiceSecret},
		gatewayauth.Request{Method: http.MethodPost, Path: request.URL.EscapedPath(), TargetNode: targetNode, Body: body},
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header = headers
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, strings.TrimSpace(string(raw))
}

func snapshotFor(t *testing.T, nodeID, upstreamURL string, disabled bool) gatewayproxy.Snapshot {
	t.Helper()
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := gatewayproxy.NormalizeAndHashState(nodeID, disabled, []gatewayproxy.Route{{
		ServiceID: "echo", Address: parsed.Host, ServicePath: "trpc.test.Echo", AllowedMethods: []string{"Echo"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func newTRPCUpstream(t *testing.T, name string) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/trpc.test.Echo/Echo" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"upstream":"`+name+`"}`)
	}))
	server.Start()
	return server
}

type fakeAdmin struct {
	*httptest.Server
	mu       sync.Mutex
	snapshot gatewayproxy.Snapshot
	outage   bool
	pulls    int
	reports  int
}

func newFakeAdmin(t *testing.T, initial gatewayproxy.Snapshot) *fakeAdmin {
	t.Helper()
	admin := &fakeAdmin{snapshot: initial}
	admin.Server = httptest.NewServer(http.HandlerFunc(admin.serveHTTP))
	return admin
}

func (admin *fakeAdmin) serveHTTP(response http.ResponseWriter, request *http.Request) {
	admin.mu.Lock()
	defer admin.mu.Unlock()
	if admin.outage {
		http.Error(response, "admin unavailable", http.StatusServiceUnavailable)
		return
	}
	switch request.URL.Path {
	case "/api/gateway-control/routes":
		target := request.URL.Query().Get("node_id")
		if target != admin.snapshot.NodeID || !verifyControlRequest(request, target, nil) {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		admin.pulls++
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(admin.snapshot)
	case "/api/gateway-control/status":
		body, _ := io.ReadAll(request.Body)
		var report struct {
			NodeID string `json:"node_id"`
		}
		if json.Unmarshal(body, &report) != nil || !verifyControlRequest(request, report.NodeID, body) {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		admin.reports++
		response.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(response, request)
	}
}

func verifyControlRequest(request *http.Request, nodeID string, body []byte) bool {
	_, err := gatewayauth.Verify(
		gatewayauth.Credentials{KeyID: controlplane.DefaultControlKeyID, Secret: testControlSecret},
		gatewayauth.Request{Method: request.Method, Path: request.URL.EscapedPath(), TargetNode: nodeID, Body: body},
		request.Header,
		time.Now(),
	)
	return err == nil
}

func (admin *fakeAdmin) setSnapshot(snapshot gatewayproxy.Snapshot) {
	admin.mu.Lock()
	defer admin.mu.Unlock()
	admin.snapshot = snapshot
}

func (admin *fakeAdmin) setOutage(outage bool) {
	admin.mu.Lock()
	defer admin.mu.Unlock()
	admin.outage = outage
}

func (admin *fakeAdmin) pullCount() int {
	admin.mu.Lock()
	defer admin.mu.Unlock()
	return admin.pulls
}

func (admin *fakeAdmin) reportCount() int {
	admin.mu.Lock()
	defer admin.mu.Unlock()
	return admin.reports
}
