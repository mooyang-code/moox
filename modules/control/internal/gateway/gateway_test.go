package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type stubServiceHandler struct {
	serviceID string
	method    string
	body      string
}

func (h *stubServiceHandler) ServiceID() string {
	return h.serviceID
}

func (h *stubServiceHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	h.method = method
	h.body = string(body)
	return []byte(`{"code":200,"message":"ok","data":[]}`), nil
}

func TestHTTPRouterServesAPIControlRoute(t *testing.T) {
	stub := &stubServiceHandler{serviceID: "auth"}
	gw := NewGatewayHandle()
	gw.Register(stub)
	router := NewHTTPRouter(gw).buildRouter()

	req := httptest.NewRequest(http.MethodPost, "/api/control/auth/Login", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if stub.method != "Login" {
		t.Fatalf("forwarded method = %q, want Login", stub.method)
	}
}

func TestHTTPRouterDoesNotExposeLegacyGatewayRoute(t *testing.T) {
	stub := &stubServiceHandler{serviceID: "auth"}
	gw := NewGatewayHandle()
	gw.Register(stub)
	router := NewHTTPRouter(gw).buildRouter()

	req := httptest.NewRequest(http.MethodPost, "/gateway/auth/Login", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHTTPRouterServesAPIServiceRouteWithServiceAuth(t *testing.T) {
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
		},
	})
	t.Cleanup(func() { SetConfig(nil) })

	stub := &stubServiceHandler{serviceID: "cloudnode"}
	gw := NewGatewayHandle()
	gw.Register(stub)
	router := NewHTTPRouter(gw).buildRouter()

	body := `{"node_id":"scf-event"}`
	req := httptest.NewRequest(http.MethodPost, "/api/service/cloudnode/ReportHeartbeatInner", strings.NewReader(body))
	req.Header.Set("Auth", GenerateServiceAuthHeaderForTest("moox-auth-v1", "collector", "collector-secret", body, time.Now().Unix(), 1800))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if stub.method != "ReportHeartbeatInner" {
		t.Fatalf("forwarded method = %q, want ReportHeartbeatInner", stub.method)
	}
	if stub.body != body {
		t.Fatalf("forwarded body = %q, want %q", stub.body, body)
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

	stub := &stubServiceHandler{serviceID: "cloudnode"}
	gw := NewGatewayHandle()
	gw.Register(stub)
	router := NewHTTPRouter(gw).buildRouter()

	req := httptest.NewRequest(http.MethodPost, "/api/service/cloudnode/ReportHeartbeatInner", strings.NewReader(`{"node_id":"scf-event"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	if stub.method != "" {
		t.Fatalf("handler should not be called, got method %q", stub.method)
	}
}

func TestIsControlAPIPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "api control endpoint", path: "/api/control/auth/Login", want: true},
		{name: "api control health", path: "/api/control/health", want: true},
		{name: "api service endpoint", path: "/api/service/cloudnode/ReportHeartbeatInner", want: false},
		{name: "legacy gateway endpoint", path: "/gateway/auth/Login", want: false},
		{name: "bare api control", path: "/api/control", want: false},
		{name: "other api", path: "/api/other/auth/Login", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsControlAPIPath(tc.path); got != tc.want {
				t.Fatalf("IsControlAPIPath(%q) = %v, want %v", tc.path, got, tc.want)
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
		{name: "api control endpoint", path: "/api/control/cloudnode/ReportHeartbeatInner", want: false},
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
		"storage_metadata": "trpc.storage.metadata.MetadataService",
		"storage_access":   "trpc.storage.access.AccessService",
		"storage_view":     "trpc.storage.view.ViewService",
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
