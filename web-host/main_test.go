package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControlGatewayRewrite(t *testing.T) {
	cases := []struct {
		path        string
		wantPath    string
		wantService string
		wantMethod  string
	}{
		{"/api/control/space/ListSpaces", "/api/control/space/ListSpaces", "space", "ListSpaces"},
		{"/api/control/cloudnode/ListNodes", "/api/control/cloudnode/ListNodes", "cloudnode", "ListNodes"},
	}
	for _, tc := range cases {
		got, ok := resolveControlGatewayTarget(tc.path)
		if !ok {
			t.Fatalf("%s was not recognized as control gateway path", tc.path)
		}
		if got.Path != tc.wantPath || got.Service != tc.wantService || got.Method != tc.wantMethod {
			t.Fatalf("%s => %+v, want path=%s service=%s method=%s", tc.path, got, tc.wantPath, tc.wantService, tc.wantMethod)
		}
	}
}

func TestControlGatewayProxyKeepsAPIControlPath(t *testing.T) {
	var gotRequestURI string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequestURI = r.URL.RequestURI()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	proxy, err := newGatewayProxy(gatewayConfig{
		ControlURL:  upstream.URL,
		MetadataURL: "http://127.0.0.1:19101",
		AccessURL:   "http://127.0.0.1:19104",
		ViewURL:     "http://127.0.0.1:19105",
	})
	if err != nil {
		t.Fatalf("newGatewayProxy returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/control/foo/Bar?trace=1", nil)
	target, ok := resolveControlGatewayTarget(req.URL.Path)
	if !ok {
		t.Fatalf("%s was not recognized as control gateway path", req.URL.Path)
	}
	rec := httptest.NewRecorder()

	proxy.ServeHTTP(rec, req, target)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("proxy returned status %d, want %d", rec.Code, http.StatusNoContent)
	}
	if gotRequestURI != "/api/control/foo/Bar?trace=1" {
		t.Fatalf("upstream received %q, want /api/control/foo/Bar?trace=1", gotRequestURI)
	}
}

func TestStorageGatewayRewrite(t *testing.T) {
	cases := []struct {
		path       string
		wantBase   string
		wantRPC    string
		wantMethod string
	}{
		{"/api/storage/metadata/ListDatasets", "metadata", "/trpc.storage.metadata.MetadataService/ListDatasets", "ListDatasets"},
		{"/api/storage/access/ReadTimeSeriesRows", "access", "/trpc.storage.access.AccessService/ReadTimeSeriesRows", "ReadTimeSeriesRows"},
		{"/api/storage/view/QueryTimeSeriesRows", "view", "/trpc.storage.view.ViewService/QueryTimeSeriesRows", "QueryTimeSeriesRows"},
	}
	for _, tc := range cases {
		got, ok := resolveStorageGatewayTarget(tc.path)
		if !ok {
			t.Fatalf("%s was not recognized as storage gateway path", tc.path)
		}
		if got.Base != tc.wantBase || got.Path != tc.wantRPC || got.Method != tc.wantMethod {
			t.Fatalf("%s => %+v, want base=%s path=%s method=%s", tc.path, got, tc.wantBase, tc.wantRPC, tc.wantMethod)
		}
	}
}

func TestGatewayRewriteRejectsInvalidPaths(t *testing.T) {
	invalidControl := []string{"/api/control", "/api/control/space", "/api/control/space/List/Extra", "/gateway/space/ListSpaces"}
	for _, path := range invalidControl {
		if got, ok := resolveControlGatewayTarget(path); ok {
			t.Fatalf("%s unexpectedly resolved to %+v", path, got)
		}
	}

	invalidStorage := []string{"/api/storage", "/api/storage/device/ListDevices", "/api/storage/access", "/api/storage/view/Query/Extra"}
	for _, path := range invalidStorage {
		if got, ok := resolveStorageGatewayTarget(path); ok {
			t.Fatalf("%s unexpectedly resolved to %+v", path, got)
		}
	}
}

func TestNewGatewayProxyPrebuildsReverseProxies(t *testing.T) {
	proxy, err := newGatewayProxy(gatewayConfig{
		ControlURL:  "http://127.0.0.1:20103",
		MetadataURL: "http://127.0.0.1:19101",
		AccessURL:   "http://127.0.0.1:19104",
		ViewURL:     "http://127.0.0.1:19105",
	})
	if err != nil {
		t.Fatalf("newGatewayProxy returned error: %v", err)
	}

	for _, base := range []string{"control", "metadata", "access", "view"} {
		if proxy.proxies[base] == nil {
			t.Fatalf("proxy for %s was not prebuilt", base)
		}
	}
}
