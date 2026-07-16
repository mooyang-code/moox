package runtime

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAuthConfig_AppliesDefaults(t *testing.T) {
	cfg := normalizeAuthConfig(AuthConfig{AccessKey: "ak", SecretKey: "sk"})
	assert.Equal(t, defaultExpireSec, cfg.ExpireSec)
	assert.Greater(t, cfg.NowUnix, int64(0))
}

func TestGenerateAuthHeader_IsDeterministic(t *testing.T) {
	cfg := AuthConfig{
		AccessKey:  "ak",
		SecretKey:  "sk",
		TargetNode: "gateway-gz-122",
		NowUnix:    1700000000,
		ExpireSec:  1800,
	}
	got, err := GenerateAuthHeader(cfg, "POST", "/api/service/x/Do", []byte(`{"k":"v"}`))
	require.NoError(t, err)
	assert.Equal(t, "gateway-gz-122", got.Get("X-Moox-Target-Node"))
}

func TestNewSignedRequestWithContext_SetsAuthHeader(t *testing.T) {
	req, err := NewSignedRequestWithContext(context.Background(), "POST", "http://127.0.0.1:8080/api", []byte(`{}`), AuthConfig{
		AccessKey: "ak", SecretKey: "sk", TargetNode: "gateway-gz-122", NowUnix: time.Now().Unix(), ExpireSec: 60,
	})
	require.NoError(t, err)
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.NotEmpty(t, req.Header.Get("X-Moox-Signature"))
	assert.Equal(t, "gateway-gz-122", req.Header.Get("X-Moox-Target-Node"))
}

func TestNewSignedRequestWithContext_RequiresCredentials(t *testing.T) {
	_, err := NewSignedRequestWithContext(context.Background(), "POST", "http://127.0.0.1:8080", nil, AuthConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestNewSignedRequestWithContextAndHeaders_PreservesSpaceHeader(t *testing.T) {
	now := time.Now()
	req, err := NewSignedRequestWithContextAndHeaders(
		context.Background(), http.MethodPost, "http://127.0.0.1:8080/api/service/x/Do", []byte(`{}`),
		map[string]string{"X-Space-Id": "space-1"},
		AuthConfig{AccessKey: "ak", SecretKey: "sk", TargetNode: "gateway-gz-122", NowUnix: now.Unix(), ExpireSec: 60},
	)
	require.NoError(t, err)
	_, err = gatewayauth.Verify(gatewayauth.Credentials{KeyID: "ak", Secret: "sk", Expire: 60 * time.Second}, gatewayauth.Request{
		Method: http.MethodPost, Path: req.URL.EscapedPath(), Body: []byte(`{}`), TargetNode: "gateway-gz-122",
	}, req.Header, now)
	require.NoError(t, err)
}

func TestDefaultAuthConfig_UsesGlobalConfig(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "env-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "env-sk")
	cfg := DefaultAuthConfig()
	assert.Equal(t, "env-ak", cfg.AccessKey)
	assert.Equal(t, "env-sk", cfg.SecretKey)
	assert.Equal(t, "gateway-gz-122", cfg.TargetNode)
}
