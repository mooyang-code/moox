package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

func TestPullSignsRequestAndValidatesSnapshot(t *testing.T) {
	const nodeID = "gateway-test"
	const secret = "control-secret"
	snapshot, _ := gatewayproxy.NormalizeAndHash(nodeID, []gatewayproxy.Route{{ServiceID: "monitor", Address: "127.0.0.1:11410", ServicePath: "trpc.moox.monitor.MonitorMgr"}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/gateway-control/routes" || r.URL.Query().Get("node_id") != nodeID || r.URL.Query().Get("current_hash") != "old" {
			t.Errorf("request URL = %s", r.URL.String())
		}
		if _, err := gatewayauth.Verify(gatewayauth.Credentials{KeyID: DefaultControlKeyID, Secret: secret}, gatewayauth.Request{Method: http.MethodGet, Path: r.URL.EscapedPath(), TargetNode: nodeID}, r.Header, time.Now()); err != nil {
			t.Errorf("Verify() = %v", err)
		}
		_ = json.NewEncoder(w).Encode(snapshot)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, secret)
	got, err := client.Pull(context.Background(), "old")
	if err != nil {
		t.Fatalf("Pull() = %v", err)
	}
	if got.RouteHash != snapshot.RouteHash {
		t.Fatalf("hash = %q", got.RouteHash)
	}
}

func TestPullFailureReturnsNoSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusBadGateway) }))
	defer server.Close()
	client := newTestClient(t, server.URL, "control-secret")
	got, err := client.Pull(context.Background(), "old")
	if err == nil || got.RouteHash != "" || len(got.Routes) != 0 {
		t.Fatalf("Pull() = %+v, %v", got, err)
	}
}

func TestNewRejectsNonLoopbackPlaintextControlPlane(t *testing.T) {
	key := filepath.Join(t.TempDir(), "control.key")
	if err := os.WriteFile(key, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{NodeID: "gateway-test", BaseURL: "http://admin.example", HMACKeyFile: key}); err == nil {
		t.Fatal("New() accepted non-loopback plaintext Admin URL")
	}
}

func TestPullRejectsWrongNodeAndInvalidRouteHash(t *testing.T) {
	for name, mutate := range map[string]func(*gatewayproxy.Snapshot){
		"wrong node":   func(snapshot *gatewayproxy.Snapshot) { snapshot.NodeID = "other-node" },
		"invalid hash": func(snapshot *gatewayproxy.Snapshot) { snapshot.RouteHash = "invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			snapshot, _ := gatewayproxy.NormalizeAndHash("gateway-test", nil)
			mutate(&snapshot)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(snapshot) }))
			defer server.Close()
			client := newTestClient(t, server.URL, "control-secret")
			got, err := client.Pull(context.Background(), "old")
			if err == nil || got.RouteHash != "" {
				t.Fatalf("Pull() = %+v, %v", got, err)
			}
		})
	}
}

func TestReportSignsExactJSONBody(t *testing.T) {
	const secret = "control-secret"
	seen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NodeID           string `json:"node_id"`
			AppliedRouteHash string `json:"applied_route_hash"`
			RouteCount       int32  `json:"route_count"`
			LastError        string `json:"last_error"`
		}
		var raw json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&raw)
		if _, err := gatewayauth.Verify(gatewayauth.Credentials{KeyID: DefaultControlKeyID, Secret: secret}, gatewayauth.Request{Method: http.MethodPost, Path: r.URL.EscapedPath(), TargetNode: "gateway-test", Body: raw}, r.Header, time.Now()); err != nil {
			t.Errorf("Verify() = %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		if body.NodeID != "gateway-test" || body.AppliedRouteHash != "hash" || body.RouteCount != 2 || body.LastError != "oops" {
			t.Errorf("body = %+v", body)
		}
		seen = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, secret)
	if err := client.Report(context.Background(), "hash", 2, "oops"); err != nil {
		t.Fatalf("Report() = %v", err)
	}
	if !seen {
		t.Fatal("request not seen")
	}
}

func newTestClient(t *testing.T, baseURL, secret string) *Client {
	t.Helper()
	key := filepath.Join(t.TempDir(), "control.key")
	if err := os.WriteFile(key, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Options{NodeID: "gateway-test", BaseURL: baseURL, HMACKeyFile: key})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
