package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/serviceauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeServiceAuthConfig_EmptyFields_ShouldApplyDefaults(t *testing.T) {
	t.Setenv("MOOX_SERVICE_AUTH_ACCESS_KEY", "env-ak")
	t.Setenv("MOOX_SERVICE_AUTH_SECRET_KEY", "env-sk")

	cfg := normalizeServiceAuthConfig(ServiceAuthConfig{})
	assert.Equal(t, defaultServiceAuthVersion, cfg.Version)
	assert.Equal(t, "env-ak", cfg.AccessKey)
	assert.Equal(t, "env-sk", cfg.SecretKey)
	assert.Equal(t, defaultServiceAuthExpireSeconds, cfg.MaxExpireSecs)
	assert.Equal(t, defaultServiceAuthClockSkewSecs, cfg.ClockSkewSecs)
}

func TestCurrentServiceAuthConfig_Disabled_ShouldError(t *testing.T) {
	SetConfig(&Config{Gateway: GatewayConfig{ServiceAuth: ServiceAuthConfig{Enabled: false}}})
	_, err := currentServiceAuthConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")
}

func TestCurrentServiceAuthConfig_MissingKeys_ShouldError(t *testing.T) {
	SetConfig(&Config{Gateway: GatewayConfig{ServiceAuth: ServiceAuthConfig{
		Enabled: true, AccessKey: "", SecretKey: "",
	}}})
	_, err := currentServiceAuthConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func buildValidServiceAuthHeader(t *testing.T, cfg ServiceAuthConfig, body string, now time.Time) string {
	t.Helper()
	cfg = normalizeServiceAuthConfig(cfg)
	header, err := serviceauth.BuildHeader(serviceauth.Config{AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, ExpireSeconds: 300}, serviceauth.Request{Method: http.MethodPost, Path: "/api/service/x/Do", Body: []byte(body)}, now)
	require.NoError(t, err)
	return header
}

func TestValidateServiceAuthHeader_ValidSignature_ShouldPass(t *testing.T) {
	cfg := ServiceAuthConfig{
		Enabled: true, Version: "moox-auth-v2",
		AccessKey: "ak", SecretKey: "sk",
		MaxExpireSecs: 1800, ClockSkewSecs: 300,
	}
	now := time.Unix(1_700_000_000, 0)
	body := `{"ok":true}`
	auth := buildValidServiceAuthHeader(t, cfg, body, now)
	require.NoError(t, validateServiceAuthHeader(auth, http.MethodPost, "/api/service/x/Do", []byte(body), now, cfg))
	require.Error(t, validateServiceAuthHeader(auth, http.MethodPost, "/api/service/x/Do", []byte(body), now, cfg))
}

func TestValidateServiceAuthHeader_InvalidFormat_ShouldError(t *testing.T) {
	cfg := ServiceAuthConfig{Enabled: true, Version: "moox-auth-v2", AccessKey: "ak", SecretKey: "sk"}
	err := validateServiceAuthHeader("bad-format", http.MethodPost, "/api/service/x/Do", nil, time.Now(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid auth format")
}

func TestValidateServiceAuthHeader_InvalidVersion_ShouldError(t *testing.T) {
	cfg := ServiceAuthConfig{Enabled: true, Version: "moox-auth-v2", AccessKey: "ak", SecretKey: "sk"}
	auth := "other-v1/ak/1/300/sig"
	err := validateServiceAuthHeader(auth, http.MethodPost, "/api/service/x/Do", nil, time.Now(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid auth version")
}

func TestValidateServiceAuthHeader_ExpiredSignature_ShouldError(t *testing.T) {
	cfg := ServiceAuthConfig{
		Enabled: true, Version: "moox-auth-v2",
		AccessKey: "ak", SecretKey: "sk",
		MaxExpireSecs: 300, ClockSkewSecs: 0,
	}
	past := time.Unix(1_600_000_000, 0)
	body := `{}`
	auth := buildValidServiceAuthHeader(t, cfg, body, past)
	err := validateServiceAuthHeader(auth, http.MethodPost, "/api/service/x/Do", []byte(body), time.Unix(1_600_001_000, 0), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestHTTPRequestHandler_ValidateServiceAuth_InvalidHeader_ShouldError(t *testing.T) {
	SetConfig(&Config{Gateway: GatewayConfig{ServiceAuth: ServiceAuthConfig{
		Enabled: true, Version: "moox-auth-v2",
		AccessKey: "ak", SecretKey: "sk",
	}}})
	h := NewHTTPRequestHandler()
	req, _ := http.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Auth", "invalid")
	err := h.validateServiceAuth(req, []byte(`{}`))
	require.Error(t, err)
}
