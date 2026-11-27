package console

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetConfig_GetConfig_RoundTrip_ShouldWork(t *testing.T) {
	cfg := &Config{Console: ConsoleConfig{AllowedOrigins: []string{"http://localhost:9527"}}}
	SetConfig(cfg)
	assert.Equal(t, cfg.Console.AllowedOrigins, GetConfig().Console.AllowedOrigins)
}

func TestAdminRouterKeepsBrowserRoutesAndRejectsMachineSurfaces(t *testing.T) {
	SetConfig(&Config{})
	router := newTestRouter(t, nil, nil, nil).buildControlRouter()
	for _, path := range []string{"/healthz", "/readyz", "/metrics", "/api/admin/health", "/api/gateway-control/routes", "/api/gateway-control/status"} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("admin router exposed %s: %d", path, rr.Code)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if !router.Match(httptest.NewRequest(method, "/api/admin/auth/Login", nil), &mux.RouteMatch{}) {
			t.Fatalf("admin router did not register %s", method)
		}
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodOptions} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(method, "/api/admin/auth/Login", nil))
		require.Equal(t, http.StatusNotFound, rr.Code, "没有跨域预检，也不收别的方法：%s", method)
	}

	machineRR := httptest.NewRecorder()
	router.ServeHTTP(machineRR, httptest.NewRequest(http.MethodPost, "/api/service/monitor/GetPeerSnapshot", nil))
	if machineRR.Code != http.StatusNotFound {
		t.Fatalf("admin router machine service status=%d, want 404", machineRR.Code)
	}
}
