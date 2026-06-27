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
		{"/api/admin/space/ListSpaces", "/api/admin/space/ListSpaces", "space", "ListSpaces"},
		{"/api/admin/cloudnode/ListNodes", "/api/admin/cloudnode/ListNodes", "cloudnode", "ListNodes"},
	}
	for _, tc := range cases {
		got, ok := resolveAdminGatewayTarget(tc.path)
		if !ok {
			t.Fatalf("%s was not recognized as admin gateway path", tc.path)
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
		AdminURL:  upstream.URL,
		MetadataURL: "http://127.0.0.1:20200",
		AccessURL:   "http://127.0.0.1:20201",
		ViewURL:     "http://127.0.0.1:20202",
	})
	if err != nil {
		t.Fatalf("newGatewayProxy returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/foo/Bar?trace=1", nil)
	target, ok := resolveAdminGatewayTarget(req.URL.Path)
	if !ok {
		t.Fatalf("%s was not recognized as admin gateway path", req.URL.Path)
	}
	rec := httptest.NewRecorder()

	proxy.ServeHTTP(rec, req, target)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("proxy returned status %d, want %d", rec.Code, http.StatusNoContent)
	}
	if gotRequestURI != "/api/admin/foo/Bar?trace=1" {
		t.Fatalf("upstream received %q, want /api/admin/foo/Bar?trace=1", gotRequestURI)
	}
}

func TestStorageGatewayRewrite(t *testing.T) {
	cases := []struct {
		path       string
		wantBase   string
		wantRPC    string
		wantMethod string
	}{
		{"/api/storage/metadata/ListDatasets", "metadata", "/trpc.storage.metadata.Metadata/ListDatasets", "ListDatasets"},
		{"/api/storage/access/ReadTimeSeriesRows", "access", "/trpc.storage.access.Access/ReadTimeSeriesRows", "ReadTimeSeriesRows"},
		{"/api/storage/view/QueryTimeSeriesRows", "view", "/trpc.storage.view.DataView/QueryTimeSeriesRows", "QueryTimeSeriesRows"},
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
	invalidControl := []string{"/api/admin", "/api/admin/space", "/api/admin/space/List/Extra", "/gateway/space/ListSpaces"}
	for _, path := range invalidControl {
		if got, ok := resolveAdminGatewayTarget(path); ok {
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
		AdminURL:  "http://127.0.0.1:11000",
		MetadataURL: "http://127.0.0.1:20200",
		AccessURL:   "http://127.0.0.1:20201",
		ViewURL:     "http://127.0.0.1:20202",
	})
	if err != nil {
		t.Fatalf("newGatewayProxy returned error: %v", err)
	}

	for _, base := range []string{"admin", "metadata", "access", "view"} {
		if proxy.proxies[base] == nil {
			t.Fatalf("proxy for %s was not prebuilt", base)
		}
	}
}
