package gateway

import (
	"encoding/json"
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

func TestGatewayHealthzRoutesUseSharedPayload(t *testing.T) {
	router := NewHTTPRouter(NewGatewayHandle()).buildRouter()

	for _, path := range []string{"/api/admin/health", "/healthz"} {
		t.Run(path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))

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
				Time   string `json:"time"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &rsp); err != nil {
				t.Fatalf("decode health response: %v", err)
			}
			if rsp.Module != "admin" {
				t.Fatalf("module = %q, want admin", rsp.Module)
			}
			if !rsp.Ready {
				t.Fatal("ready = false, want true")
			}
			if rsp.Status != "ok" {
				t.Fatalf("status = %q, want ok", rsp.Status)
			}
			if rsp.Time == "" {
				t.Fatal("time is empty")
			}
		})
	}
}
