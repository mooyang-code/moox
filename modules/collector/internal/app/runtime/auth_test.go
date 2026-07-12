package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAuthConfig_AppliesDefaults(t *testing.T) {
	cfg := normalizeAuthConfig(AuthConfig{AccessKey: "ak", SecretKey: "sk"})
	assert.Equal(t, defaultAuthVersion, cfg.Version)
	assert.Equal(t, defaultExpireSec, cfg.ExpireSec)
	assert.Greater(t, cfg.NowUnix, int64(0))
}

func TestGenerateAuthHeader_IsDeterministic(t *testing.T) {
	cfg := AuthConfig{
		Version:   defaultAuthVersion,
		AccessKey: "ak",
		SecretKey: "sk",
		NowUnix:   1700000000,
		ExpireSec: 1800,
	}
	got := GenerateAuthHeader(cfg, `{"k":"v"}`)
	assert.Contains(t, got, "moox-auth-v1/ak/1700000000/1800/")
	assert.Equal(t, got, GenerateAuthHeader(cfg, `{"k":"v"}`))
}

func TestNewSignedRequestWithContext_SetsAuthHeader(t *testing.T) {
	req, err := NewSignedRequestWithContext(context.Background(), "POST", "http://127.0.0.1:8080/api", []byte(`{}`), AuthConfig{
		AccessKey: "ak", SecretKey: "sk", NowUnix: time.Now().Unix(), ExpireSec: 60,
	})
	require.NoError(t, err)
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.NotEmpty(t, req.Header.Get("Auth"))
}

func TestNewSignedRequestWithContext_RequiresCredentials(t *testing.T) {
	_, err := NewSignedRequestWithContext(context.Background(), "POST", "http://127.0.0.1:8080", nil, AuthConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestDefaultAuthConfig_UsesGlobalConfig(t *testing.T) {
	t.Setenv("MOOX_SERVICE_AUTH_ACCESS_KEY", "env-ak")
	t.Setenv("MOOX_SERVICE_AUTH_SECRET_KEY", "env-sk")
	cfg := DefaultAuthConfig()
	assert.Equal(t, "env-ak", cfg.AccessKey)
	assert.Equal(t, "env-sk", cfg.SecretKey)
}
