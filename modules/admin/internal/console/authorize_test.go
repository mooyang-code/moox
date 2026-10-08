package console

import (
	"context"
	"net/http"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldSkipAuth_ConfiguredMethod_ShouldReturnTrue(t *testing.T) {
	SetConfig(&Config{Gateway: GatewayConfig{NoAuthMethods: []string{"/api/admin/auth/login"}}})
	assert.True(t, ShouldSkipAuth("/api/admin/auth/login"))
	assert.False(t, ShouldSkipAuth("/api/admin/auth/get_user_info"))
}

func TestShouldSkipAuth_NilConfig_ShouldReturnFalse(t *testing.T) {
	SetConfig(nil)
	assert.False(t, ShouldSkipAuth("/api/admin/auth/login"))
}

func TestValidateAccessToken_ValidToken_ShouldReturnClaims(t *testing.T) {
	secret := "test-secret-key-for-gateway"
	SetConfig(&Config{JWT: JWTConfig{SecretKey: secret}})
	token, err := mooxsecurity.SignToken(map[string]any{
		"user_id":    "user-1",
		"username":   "admin",
		"role":       int32(pb.UserRole_USER_ROLE_ADMIN),
		"token_type": "access",
		"sid":        "session-1",
	}, secret, "moox-admin", time.Hour)
	require.NoError(t, err)

	claims, ok := validateAccessToken(context.Background(), token)
	assert.True(t, ok)
	require.NotNil(t, claims)
	assert.Equal(t, "user-1", claims.UserID)
}

func TestValidateAccessToken_WrongIssuer_ShouldReject(t *testing.T) {
	secret := "test-secret-key-for-gateway"
	SetConfig(&Config{JWT: JWTConfig{SecretKey: secret}})
	token, err := mooxsecurity.SignToken(map[string]any{
		"user_id":    "user-1",
		"username":   "admin",
		"role":       int32(pb.UserRole_USER_ROLE_ADMIN),
		"token_type": "access",
	}, secret, "other-service", time.Hour)
	require.NoError(t, err)

	_, ok := validateAccessToken(context.Background(), token)
	assert.False(t, ok)
}

func TestValidateAccessToken_EmptySecret_ShouldReturnFalse(t *testing.T) {
	SetConfig(&Config{JWT: JWTConfig{SecretKey: ""}})
	claims, ok := validateAccessToken(context.Background(), "any-token")
	assert.False(t, ok)
	assert.Nil(t, claims)
}

func TestGetJWTSecretKey_NilConfig_ShouldReturnEmpty(t *testing.T) {
	SetConfig(nil)
	assert.Empty(t, getJWTSecretKey())
}

func TestAccessTokenFromHTTPPrefersBearerAuthorization(t *testing.T) {
	r := &http.Request{Header: http.Header{"Authorization": []string{"Bearer bearer-token"}, "X-Access-Token": []string{"other"}}}
	assert.Equal(t, "bearer-token", accessTokenFromHTTP(r))
	r = &http.Request{Header: http.Header{"X-Access-Token": []string{"access-token"}}}
	assert.Equal(t, "access-token", accessTokenFromHTTP(r))
}
