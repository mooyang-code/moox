package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type unavailableFS struct{}

func (unavailableFS) Open(name string) (http.File, error) {
	return nil, errors.New("static fs should not be touched for healthz")
}

type recordingFS struct {
	opened string
}

func (f *recordingFS) Open(name string) (http.File, error) {
	f.opened = name
	return nil, errors.New("fixture asset unavailable")
}

func TestWebHostHealthzBypassesStaticFallback(t *testing.T) {
	handler := newHealthHandler()

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var rsp struct {
		Module string `json:"module"`
		Ready  bool   `json:"ready"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &rsp); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if rsp.Module != "web-host" {
		t.Fatalf("module = %q, want web-host", rsp.Module)
	}
	if !rsp.Ready {
		t.Fatal("ready = false, want true")
	}
	if rsp.Status != "ok" {
		t.Fatalf("status = %q, want ok", rsp.Status)
	}
}

func TestPublicHandlerDoesNotExposeDiagnostics(t *testing.T) {
	handler := newStaticHandler(unavailableFS{})
	for _, path := range []string{"/healthz", "/readyz", "/metrics", "/api/admin/auth/Login", "/api/service/cloudnode/PollJobItems"} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, rr.Code, http.StatusNotFound)
		}
	}
}

func TestPublicHandlerRoutesStaticAssetPathsToStaticFilesystem(t *testing.T) {
	staticFS := &recordingFS{}
	handler := newStaticHandler(staticFS)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/static/app.js", nil))

	if staticFS.opened != "/static/app.js" {
		t.Fatalf("opened = %q, want /static/app.js", staticFS.opened)
	}
}

func TestGatewayConfigDefaultsToLoopbackListeners(t *testing.T) {
	t.Setenv("MOOX_WEB_HOST_ADDR", "")
	t.Setenv("MOOX_WEB_HOST_HEALTH_ADDR", "")
	cfg := loadGatewayConfig()
	if cfg.ListenAddr != "127.0.0.1:9528" {
		t.Fatalf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.HealthAddr != "127.0.0.1:19527" {
		t.Fatalf("HealthAddr = %q", cfg.HealthAddr)
	}
}
