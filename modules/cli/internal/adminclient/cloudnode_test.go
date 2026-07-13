package adminclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListCloudAccounts_ParsesSuccessResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/cloudnode/ListCloudAccounts", r.URL.Path)
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"},"accounts":[{"account_id":"a1","provider":"tencent"}]}`))
	}))
	defer server.Close()

	client := New(server.URL)
	accounts, err := client.ListCloudAccounts(context.Background(), "tencent")
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	assert.Equal(t, "a1", accounts[0].AccountID)
}

func TestResolvePackageType_MapsKnownAliases(t *testing.T) {
	assert.Equal(t, 1, ResolvePackageType("collector"))
	assert.Equal(t, 2, ResolvePackageType("factor"))
	assert.Equal(t, 3, ResolvePackageType("custom"))
	assert.Equal(t, 1, ResolvePackageType("unknown"))
}

func TestGetCOSAccountInfoUsesSignedServiceRouteAndReveal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/service/cloudnode/GetCOSAccountInfo", r.URL.Path)
		require.NotEmpty(t, r.Header.Get("Auth"))
		var body struct {
			AccountID string `json:"account_id"`
			Reveal    bool   `json:"reveal"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "acct-1", body.AccountID)
		require.True(t, body.Reveal)
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"secret":{"account_id":"acct-1","provider":"tencent","secret_id":"sid","secret_key":"skey"}}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.HTTPClient = server.Client()
	client.ServiceAuth = &ServiceAuthConfig{Version: "moox-auth-v2", AccessKey: "ak", SecretKey: "sk", ExpireSecs: 60}
	secret, err := client.GetCOSAccountInfo(context.Background(), "acct-1")
	require.NoError(t, err)
	require.Equal(t, "sid", secret.SecretID)
}

func TestGetCOSAccountInfoRejectsUnsignedReveal(t *testing.T) {
	client := New("http://127.0.0.1:1")
	_, err := client.GetCOSAccountInfo(context.Background(), "acct-1")
	require.ErrorContains(t, err, "service authentication is required")
}
