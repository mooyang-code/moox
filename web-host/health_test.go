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

func TestWebHostHealthzBypassesStaticFallback(t *testing.T) {
	handler := newHTTPHandler(unavailableFS{})

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
