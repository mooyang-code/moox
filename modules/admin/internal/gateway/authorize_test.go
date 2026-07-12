package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	admincrypto "github.com/mooyang-code/moox/modules/admin/internal/common/crypto"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	thttp "trpc.group/trpc-go/trpc-go/http"
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

func TestCreateAuthFailResponse_ShouldReturnNoAuth(t *testing.T) {
	resp := createAuthFailResponse().(*middlewareResp)
	require.NotNil(t, resp.RetInfo)
	assert.Equal(t, pb.ErrorCode_NO_AUTH, resp.RetInfo.Code)
	assert.Contains(t, resp.RetInfo.Msg, "访问令牌无效")
}

func TestGetTokenFromHeader_ExistingHeader_ShouldReturnValue(t *testing.T) {
	header := &thttp.Header{Request: &http.Request{Header: http.Header{"Authorization": []string{"token-abc"}}}}
	assert.Equal(t, "token-abc", getTokenFromHeader(header, "Authorization"))
	assert.Empty(t, getTokenFromHeader(header, "X-Access-Token"))
}

func TestValidateAccessToken_ValidToken_ShouldReturnClaims(t *testing.T) {
	secret := "test-secret-key-for-gateway"
	SetConfig(&Config{JWT: JWTConfig{SecretKey: secret}})
	token, err := admincrypto.GenerateAccessToken("user-1", "admin", int32(pb.UserRole_USER_ROLE_ADMIN), secret, time.Hour)
	require.NoError(t, err)

	claims, ok := validateAccessToken(context.Background(), token)
	assert.True(t, ok)
	require.NotNil(t, claims)
	assert.Equal(t, "user-1", claims.UserID)
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

func TestGetAccessTokenFromRequest_AuthorizationHeader_ShouldReturnToken(t *testing.T) {
	header := &thttp.Header{Request: &http.Request{Header: http.Header{
		"Authorization": []string{"bearer-token"},
	}}}
	token := getAccessTokenFromRequest(context.Background(), header, nil)
	assert.Equal(t, "bearer-token", token)
}

func TestGetAccessTokenFromRequest_XAccessTokenHeader_ShouldReturnToken(t *testing.T) {
	header := &thttp.Header{Request: &http.Request{Header: http.Header{
		"X-Access-Token": []string{"access-token"},
	}}}
	token := getAccessTokenFromRequest(context.Background(), header, nil)
	assert.Equal(t, "access-token", token)
}
