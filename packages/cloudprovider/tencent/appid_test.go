package tencent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetUserAppIDReadsAppIDNotUIN(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GetUserAppId", r.Header.Get("X-TC-Action"))
		require.True(t, strings.Contains(r.Header.Get("Authorization"), "secret-id"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Response":{"Uin":100012345678,"OwnerUin":100012345678,"AppId":1250000001,"RequestId":"r1"}}`))
	}))
	defer server.Close()
	appID, err := GetUserAppID(context.Background(), IdentityOptions{
		Credentials: Credentials{SecretID: "secret-id", SecretKey: "secret-key"}, Endpoint: server.URL, HTTPClient: server.Client(),
	})
	require.NoError(t, err)
	require.Equal(t, "1250000001", appID)
}

func TestGetUserAppIDRejectsErrorsAndEmptyAppID(t *testing.T) {
	for _, body := range []string{
		`{"Response":{"Error":{"Code":"AuthFailure.SecretIdNotFound","Message":"secret-key leaked?"},"RequestId":"r2"}}`,
		`{"Response":{"Uin":1,"RequestId":"r3"}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		_, err := GetUserAppID(context.Background(), IdentityOptions{
			Credentials: Credentials{SecretID: "secret-id", SecretKey: "secret-key"}, Endpoint: server.URL, HTTPClient: server.Client(),
		})
		server.Close()
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret-key")
	}
}
