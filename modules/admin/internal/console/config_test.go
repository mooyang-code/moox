package console

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func loadConsoleConfigFrom(t *testing.T, content string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "config"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "console.yaml"), []byte(content), 0o644))
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	return LoadConfig("./config/console.yaml")
}

func TestLoadConfig_ValidYAML_ShouldParse(t *testing.T) {
	cfg, err := loadConsoleConfigFrom(t, `console:
  allowed_origins:
    - "http://localhost:9527"
rate_limit:
  method_limits:
    "/api/admin/auth/Login":
      qps: 1
      burst: 2
`)
	require.NoError(t, err)
	assert.Equal(t, []string{"http://localhost:9527"}, cfg.Console.AllowedOrigins)
	assert.Equal(t, 2, cfg.RateLimit.MethodLimits["/api/admin/auth/Login"].Burst)
}

// 仓库里的 console.yaml 生产不放开任何别的来源。
func TestShippedConsoleConfigHasNoExtraOrigins(t *testing.T) {
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(filepath.Join("..", "..")))
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	cfg, err := LoadConfig("./config/console.yaml")
	require.NoError(t, err)
	assert.Empty(t, cfg.Console.AllowedOrigins)
	assert.Contains(t, cfg.RateLimit.MethodLimits, "/api/admin/auth/Login")
}

func TestLoadConfig_RejectsInvalidOrigins(t *testing.T) {
	for _, origin := range []string{"*", "https://*.example.com", "localhost:9527", "https://a.example/path", "ftp://a.example"} {
		_, err := loadConsoleConfigFrom(t, "console:\n  allowed_origins:\n    - \""+origin+"\"\n")
		require.ErrorContains(t, err, "console.allowed_origins", origin)
	}
}

func TestLoadConfig_MissingFile_ShouldError(t *testing.T) {
	dir := t.TempDir()
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	_, err = LoadConfig("./config/console.yaml")
	require.Error(t, err)
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
