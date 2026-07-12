package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayContainsWildcardOrigin_HasWildcard_ShouldReturnTrue(t *testing.T) {
	assert.True(t, gatewayContainsWildcardOrigin([]string{"https://a.com", "*"}))
}

func TestGatewayContainsWildcardOrigin_NoWildcard_ShouldReturnFalse(t *testing.T) {
	assert.False(t, gatewayContainsWildcardOrigin([]string{"https://a.com", "https://b.com"}))
}

func TestValidateGatewayCORS_NilConfig_ShouldError(t *testing.T) {
	err := validateGatewayCORS(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestValidateGatewayCORS_ProdEmptyOrigins_ShouldError(t *testing.T) {
	cfg := &gateway.Config{Gateway: gateway.GatewayConfig{Debug: false}}
	err := validateGatewayCORS(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cors.allowed_origins")
}

func TestValidateGatewayCORS_DebugEmptyOrigins_ShouldPass(t *testing.T) {
	cfg := &gateway.Config{Gateway: gateway.GatewayConfig{Debug: true}}
	require.NoError(t, validateGatewayCORS(cfg))
}

func TestValidateGatewayCORS_WithOrigins_ShouldPass(t *testing.T) {
	cfg := &gateway.Config{
		Gateway: gateway.GatewayConfig{Debug: false},
		CORS:    gateway.CORSConfig{AllowedOrigins: []string{"https://admin.example.com"}},
	}
	require.NoError(t, validateGatewayCORS(cfg))
}

func TestLoadEncryptionKey_FromEnv_ShouldPass(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", "")
	require.NoError(t, loadEncryptionKey())
}

func TestLoadEncryptionKey_MissingFileEnv_ShouldError(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "")
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", "")
	err := loadEncryptionKey()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MOOX_ADMIN_ENCRYPTION_KEY_FILE is required")
}

func TestLoadEncryptionKey_InvalidFileMode_ShouldError(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "")
	keyPath := filepath.Join(t.TempDir(), "bad-key")
	require.NoError(t, os.WriteFile(keyPath, []byte("secret-key-value-32bytes-long!!"), 0o644))
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", keyPath)

	err := loadEncryptionKey()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "regular 0600 file")
}

func TestLoadEncryptionKey_ValidFile_ShouldSetEnv(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "")
	keyPath := filepath.Join(t.TempDir(), "good-key")
	require.NoError(t, os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcdef"), 0o600))
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", keyPath)

	require.NoError(t, loadEncryptionKey())
	assert.Equal(t, "0123456789abcdef0123456789abcdef", os.Getenv("MOOX_ADMIN_ENCRYPTION_KEY"))
}
