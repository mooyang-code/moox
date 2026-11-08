package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/stretchr/testify/require"
)

// 币安行情数据类接口只要 API Key 头、不签名：带头的入口会发出 X-MBX-APIKEY，普通入口不带。
func TestGetDirectWithHeadersSendsAPIKeyHeader(t *testing.T) {
	var gotKey, gotPath, gotSignature string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath, gotSignature = r.Header.Get("X-MBX-APIKEY"), r.URL.Path, r.URL.Query().Get("signature")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client := NewClient(httpclient.NewHTTPClient(server.Client()))
	domain := server.Listener.Addr().String()

	var result map[string]bool
	require.NoError(t, client.GetDirectWithHeaders(context.Background(), domain, "/sapi/v1/spot/delist-schedule", nil, http.Header{"X-Mbx-Apikey": {"public-key-id"}}, &result))
	require.True(t, result["ok"])
	require.Equal(t, "public-key-id", gotKey)
	require.Equal(t, "/sapi/v1/spot/delist-schedule", gotPath)
	require.Empty(t, gotSignature, "行情数据类接口不签名")

	require.NoError(t, client.GetDirect(context.Background(), domain, "/api/v3/time", nil, &result))
	require.Empty(t, gotKey, "普通入口不带 API Key 头")
}
