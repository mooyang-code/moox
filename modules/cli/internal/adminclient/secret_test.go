package adminclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetSecretValueCallsSecretMgr(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/trpc.moox.ops.SecretMgr/GetSecretValue", r.URL.Path)
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"secret":{"secret_id":"secret-1","category":"cloud","provider":"tencent","status":"active","key_id":"sid","secret_value":"skey"}}`))
	}))
	defer server.Close()
	secret, err := newTestClient(server.URL).GetSecretValue(context.Background(), "secret-1")
	require.NoError(t, err)
	require.Equal(t, "sid", secret.KeyID)
}
