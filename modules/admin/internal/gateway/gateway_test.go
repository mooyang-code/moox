package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestBackend 启动一个模拟底层有协议 http 服务的 httptest.Server，返回其 host:port 与清理函数。
func newTestBackend(t *testing.T, handler http.HandlerFunc) (string, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), srv.Close
}

func TestHTTPRouterForwardsControlToBackend(t *testing.T) {
	var gotPath, gotBody string
	addr, _ := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ret_info":{"code":0},"ok":true}`))
	})

	SetConfig(&Config{Gateway: GatewayConfig{Services: map[string]ServiceDetail{
		"auth": {Address: addr, Path: "trpc.test.AuthService"},
	}}})
	t.Cleanup(func() { SetConfig(nil) })

	gw := NewGatewayHandle()
	router := NewHTTPRouter(gw).buildRouter()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth/Login", strings.NewReader(`{"x":1}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/trpc.test.AuthService/Login" {
		t.Fatalf("forwarded path = %q, want /trpc.test.AuthService/Login", gotPath)
	}
	if gotBody != `{"x":1}` {
		t.Fatalf("forwarded body = %q, want {\"x\":1}", gotBody)
	}
	if !strings.Contains(rec.Body.String(), `"code":0`) {
		t.Fatalf("response body should contain ret_info code 0, got %s", rec.Body.String())
	}
}

func TestHTTPRouterDoesNotExposeLegacyGatewayRoute(t *testing.T) {
	gw := NewGatewayHandle()
	router := NewHTTPRouter(gw).buildRouter()

	req := httptest.NewRequest(http.MethodPost, "/gateway/auth/Login", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHTTPRouterForwardsServiceRouteWithServiceAuth(t *testing.T) {
	var gotPath, gotBody string
	addr, _ := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Write([]byte(`{"ret_info":{"code":0}}`))
	})

	SetConfig(&Config{
		Gateway: GatewayConfig{
			ServiceAuth: ServiceAuthConfig{
				Enabled:       true,
				Version:       "moox-auth-v1",
				AccessKey:     "collector",
				SecretKey:     "collector-secret",
				MaxExpireSecs: 1800,
				ClockSkewSecs: 60,
			},
			Services: map[string]ServiceDetail{
				"cloudnode": {Address: addr, Path: "trpc.test.CloudNodeService"},
			},
		},
	})
	t.Cleanup(func() { SetConfig(nil) })

	gw := NewGatewayHandle()
	router := NewHTTPRouter(gw).buildRouter()

	body := `{"node_id":"scf-event"}`
	req := httptest.NewRequest(http.MethodPost, "/api/service/cloudnode/ReportHeartbeatInner", strings.NewReader(body))
	req.Header.Set("Auth", GenerateServiceAuthHeaderForTest("moox-auth-v1", "collector", "collector-secret", body, time.Now().Unix(), 1800))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/trpc.test.CloudNodeService/ReportHeartbeat" {
		t.Fatalf("forwarded path = %q, want /trpc.test.CloudNodeService/ReportHeartbeat", gotPath)
	}
	if gotBody != body {
		t.Fatalf("forwarded body = %q, want %q", gotBody, body)
	}
}

func TestHTTPRouterRejectsAPIServiceRouteWithoutServiceAuth(t *testing.T) {
	SetConfig(&Config{
		Gateway: GatewayConfig{
			ServiceAuth: ServiceAuthConfig{
				Enabled:   true,
				AccessKey: "collector",
				SecretKey: "collector-secret",
			},
		},
	})
	t.Cleanup(func() { SetConfig(nil) })

	gw := NewGatewayHandle()
	router := NewHTTPRouter(gw).buildRouter()

	req := httptest.NewRequest(http.MethodPost, "/api/service/cloudnode/ReportHeartbeatInner", strings.NewReader(`{"node_id":"scf-event"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

// unused but kept to silence context import if needed by future tests
var _ context.Context = context.Background()

func TestIsAdminAPIPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "api control endpoint", path: "/api/admin/auth/Login", want: true},
		{name: "api control health", path: "/api/admin/health", want: true},
		{name: "api service endpoint", path: "/api/service/cloudnode/ReportHeartbeatInner", want: false},
		{name: "legacy gateway endpoint", path: "/gateway/auth/Login", want: false},
		{name: "bare api control", path: "/api/admin", want: false},
		{name: "other api", path: "/api/other/auth/Login", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAdminAPIPath(tc.path); got != tc.want {
				t.Fatalf("IsAdminAPIPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsServiceAPIPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "api service endpoint", path: "/api/service/cloudnode/ReportHeartbeatInner", want: true},
		{name: "api control endpoint", path: "/api/admin/cloudnode/ReportHeartbeatInner", want: false},
		{name: "bare api service", path: "/api/service", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsServiceAPIPath(tc.path); got != tc.want {
				t.Fatalf("IsServiceAPIPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestControlGatewayConfigIncludesStorageFacadeServices(t *testing.T) {
	gatewayConfigPath := filepath.Join("..", "..", "config", "gateway.yaml")
	trpcConfigPath := filepath.Join("..", "..", "config", "trpc_go.yaml")

	gatewayConfig, err := os.ReadFile(gatewayConfigPath)
	if err != nil {
		t.Fatalf("read gateway config: %v", err)
	}
	trpcConfig, err := os.ReadFile(trpcConfigPath)
	if err != nil {
		t.Fatalf("read trpc config: %v", err)
	}

	required := map[string]string{
		"storage_metadata": "trpc.moox.storage.Metadata",
		"storage_access":   "trpc.moox.storage.Access",
		"storage_view":     "trpc.moox.storage.DataView",
	}
	for serviceID, servicePath := range required {
		if !strings.Contains(string(gatewayConfig), serviceID+":") {
			t.Fatalf("gateway config must include service id %q", serviceID)
		}
		if !strings.Contains(string(gatewayConfig), servicePath) {
			t.Fatalf("gateway config must map %q to %q", serviceID, servicePath)
		}
		if !strings.Contains(string(trpcConfig), "name: "+serviceID) {
			t.Fatalf("trpc client config must include service id %q", serviceID)
		}
	}
}
