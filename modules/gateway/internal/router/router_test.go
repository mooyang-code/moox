package router

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/gateway/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

const (
	testNode   = "gateway-test"
	testKeyID  = "moox-gateway-service"
	testSecret = "service-secret"
)

func TestServiceRouterProxiesAuthenticatedRequestAndPreservesHeaders(t *testing.T) {
	upstream := newUpstream(t)
	handler, closeHandler := newHandler(t, upstream, false, 1024)
	defer closeHandler()

	req := signedRequest(t, http.MethodPost, "/api/service/monitor/GetSnapshot", []byte(`{"ok":true}`), testNode, testSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Trace-Id", "trace-123")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusCreated || recorder.Header().Get("trpc-ret") != "0" || recorder.Header().Get("X-Trace-Id") != "trace-123" {
		t.Fatalf("response = %d headers=%v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestServiceRouterRejectsInvalidMethodAndPath(t *testing.T) {
	handler, closeHandler := newHandler(t, nil, false, 1024)
	defer closeHandler()
	for name, req := range map[string]*http.Request{
		"HTTP method":  signedRequest(t, http.MethodGet, "/api/service/monitor/GetSnapshot", nil, testNode, testSecret),
		"extra path":   signedRequest(t, http.MethodPost, "/api/service/monitor/GetSnapshot/extra", nil, testNode, testSecret),
		"empty method": signedRequest(t, http.MethodPost, "/api/service/monitor/", nil, testNode, testSecret),
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusMethodNotAllowed && recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d", recorder.Code)
			}
		})
	}
}

func TestServiceRouterAuthenticationAndReplay(t *testing.T) {
	handler, closeHandler := newHandler(t, nil, false, 1024)
	defer closeHandler()
	valid := signedRequest(t, http.MethodPost, "/api/service/missing/Call", nil, testNode, testSecret)
	for name, mutate := range map[string]func(*http.Request){
		"bad HMAC":   func(req *http.Request) { req.Header.Set("X-Moox-Signature", strings.Repeat("0", 64)) },
		"wrong node": func(req *http.Request) { req.Header.Set("X-Moox-Target-Node", "other-node") },
	} {
		t.Run(name, func(t *testing.T) {
			req := valid.Clone(context.Background())
			req.Header = valid.Header.Clone()
			mutate(req)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", recorder.Code)
			}
		})
	}
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, valid)
	if first.Code != http.StatusNotFound {
		t.Fatalf("first status = %d", first.Code)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, valid.Clone(context.Background()))
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d", second.Code)
	}
}

func TestServiceRouterStatusMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		upstream *httptest.Server
		disabled bool
		maxBody  int64
		path     string
		body     []byte
		want     int
	}{
		"body too large":       {maxBody: 3, path: "/api/service/monitor/GetSnapshot", body: []byte("four"), want: http.StatusRequestEntityTooLarge},
		"missing route":        {maxBody: 1024, path: "/api/service/missing/Call", want: http.StatusNotFound},
		"disabled node":        {maxBody: 1024, path: "/api/service/monitor/GetSnapshot", disabled: true, want: http.StatusServiceUnavailable},
		"unavailable upstream": {maxBody: 1024, path: "/api/service/monitor/GetSnapshot", want: http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			handler, closeHandler := newHandler(t, tc.upstream, tc.disabled, tc.maxBody)
			defer closeHandler()
			req := signedRequest(t, http.MethodPost, tc.path, tc.body, testNode, testSecret)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d body=%q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func newHandler(t *testing.T, upstream *httptest.Server, disabled bool, maxBody int64) (http.Handler, func()) {
	t.Helper()
	address := "127.0.0.1:1"
	if upstream != nil {
		address = upstream.Listener.Addr().String()
	}
	snapshot, err := gatewayproxy.NormalizeAndHashState(testNode, disabled, []gatewayproxy.Route{{ServiceID: "monitor", Address: address, ServicePath: "trpc.moox.monitor.MonitorMgr", MaxBodyBytes: 1024}})
	if err != nil {
		t.Fatal(err)
	}
	var table gatewayproxy.Table
	if err := table.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
	nonces, err := store.OpenNonces(filepath.Join(t.TempDir(), "nonces"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(Options{NodeID: testNode, Credentials: gatewayauth.Credentials{KeyID: testKeyID, Secret: testSecret}, MaxBodyBytes: maxBody, Table: &table, Nonces: nonces, Disabled: func() bool { return disabled }})
	return handler, func() { _ = nonces.Close() }
}

func signedRequest(t *testing.T, method, path string, body []byte, nodeID, secret string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	headers, err := gatewayauth.Sign(gatewayauth.Credentials{KeyID: testKeyID, Secret: secret}, gatewayauth.Request{Method: method, Path: path, TargetNode: nodeID, Body: body}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return req
}

func newUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/trpc.moox.monitor.MonitorMgr/GetSnapshot" || r.Header.Get("X-Trace-Id") != "trace-123" {
			t.Errorf("upstream request = %s headers=%v", r.URL.Path, r.Header)
		}
		w.Header().Set("trpc-ret", "0")
		w.Header().Set("X-Trace-Id", r.Header.Get("X-Trace-Id"))
		w.WriteHeader(http.StatusCreated)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}
