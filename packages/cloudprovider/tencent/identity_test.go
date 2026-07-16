package tencent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityValidatorReturnsSanitizedIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Response":{"Arn":"qcs::cam::uin/10001:uin/10001","AccountId":"10001","UserId":"10001","PrincipalId":"10001","Type":"CAMUser","RequestId":"request-1"}}`))
	}))
	defer server.Close()

	validator, err := NewIdentityValidator(IdentityOptions{
		Credentials: Credentials{SecretID: "secret-id", SecretKey: "secret-key"},
		Endpoint:    server.URL,
		HTTPClient:  server.Client(),
	})
	require.NoError(t, err)
	identity, err := validator.GetCallerIdentity(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tencent", identity.Provider)
	assert.Equal(t, "10001", identity.AccountID)
	assert.Equal(t, "request-1", identity.RequestID)
}

func TestIdentityValidatorRedactsAuthenticationErrors(t *testing.T) {
	secretID := "recognizable-secret-id"
	secretKey := "recognizable-secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Response":{"Error":{"Code":"AuthFailure.AccessKeyIllegal","Message":"recognizable-secret-key is invalid"},"RequestId":"request-auth"}}`))
	}))
	defer server.Close()

	validator, err := NewIdentityValidator(IdentityOptions{
		Credentials: Credentials{SecretID: secretID, SecretKey: secretKey},
		Endpoint:    server.URL,
		HTTPClient:  server.Client(),
	})
	require.NoError(t, err)
	_, err = validator.GetCallerIdentity(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAuthentication)
	assert.Contains(t, err.Error(), "request-auth")
	assert.NotContains(t, err.Error(), secretID)
	assert.NotContains(t, err.Error(), secretKey)
}

func TestNewIdentityValidatorRejectsIncompleteCredentials(t *testing.T) {
	_, err := NewIdentityValidator(IdentityOptions{Credentials: Credentials{SecretID: "secret-id"}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialsInvalid)
	assert.NotContains(t, err.Error(), "secret-id")
}
