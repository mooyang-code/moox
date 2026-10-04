package adminclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetSecretValueUsesSignedServiceRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/service/secret/GetSecretValue", r.URL.Path)
		require.NotEmpty(t, r.Header.Get("X-Moox-Signature"))
		require.Equal(t, "moox-cli", r.Header.Get("X-Moox-Caller"))
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"secret":{"secret_id":"secret-1","category":"cloud","provider":"tencent","status":"active","key_id":"sid","secret_value":"skey"}}`))
	}))
	defer server.Close()
	client := New(server.URL)
	client.ServiceAuth = &ServiceAuthConfig{
		AccessKey: "ak", SecretKey: "sk", Caller: "moox-cli", TargetNode: "gateway-1", ExpireSecs: 60,
	}
	secret, err := client.GetSecretValue(context.Background(), "secret-1")
	require.NoError(t, err)
	require.Equal(t, "sid", secret.KeyID)
}
