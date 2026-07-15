package gateway

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSetConfig_GetConfig_RoundTrip_ShouldWork(t *testing.T) {
	cfg := &Config{
		JWT:  JWTConfig{SecretKey: "secret"},
		CORS: CORSConfig{AllowedOrigins: []string{"https://app.example.com"}},
	}
	SetConfig(cfg)
	got := GetConfig()
	assert.Equal(t, cfg.JWT.SecretKey, got.JWT.SecretKey)
	assert.Equal(t, cfg.CORS.AllowedOrigins, got.CORS.AllowedOrigins)
}

func TestApplyCORSHeaders_EmptyOriginWithWildcard_ShouldSetStar(t *testing.T) {
	SetConfig(&Config{CORS: CORSConfig{AllowedOrigins: []string{"*"}}})
	rr := httptest.NewRecorder()
	applyCORSHeaders(rr, "")
	assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestLoadConfig_ValidYAML_ShouldParse(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	content := `jwt:
  secret_key: test-secret
gateway:
  debug: true
  service_auth:
    enabled: true
    access_key: ak
    secret_key: sk
cors:
  allowed_origins:
    - "*"
rate_limit:
  default_qps: 100
  default_burst: 200
`
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "gateway.yaml"), []byte(content), 0o644))

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "test-secret", cfg.JWT.SecretKey)
	assert.True(t, cfg.Gateway.Debug)
	assert.Equal(t, 100, cfg.RateLimit.DefaultQPS)
}

func TestLoadConfig_MissingFile_ShouldError(t *testing.T) {
	dir := t.TempDir()
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	_, err = LoadConfig()
	require.Error(t, err)
}

func TestGatewayRoutersIsolateControlServiceAndDiagnostics(t *testing.T) {
	hr := NewHTTPRouter(NewGatewayHandle(), nil)
	for name, router := range map[string]http.Handler{"control": hr.buildControlRouter(), "service": hr.buildServiceRouter()} {
		for _, path := range []string{"/healthz", "/readyz", "/metrics", "/api/admin/health"} {
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
			if rr.Code != http.StatusNotFound {
				t.Fatalf("%s router exposed %s: %d", name, path, rr.Code)
			}
		}
	}

	serviceRR := httptest.NewRecorder()
	hr.buildControlRouter().ServeHTTP(serviceRR, httptest.NewRequest(http.MethodPost, "/api/service/cloudnode/PollJobItems", nil))
	if serviceRR.Code != http.StatusNotFound {
		t.Fatalf("control router service status=%d", serviceRR.Code)
	}
	adminRR := httptest.NewRecorder()
	hr.buildServiceRouter().ServeHTTP(adminRR, httptest.NewRequest(http.MethodPost, "/api/admin/auth/Login", nil))
	if adminRR.Code != http.StatusNotFound {
		t.Fatalf("service router admin status=%d", adminRR.Code)
	}
}
