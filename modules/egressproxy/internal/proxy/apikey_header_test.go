package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// X-MBX-APIKEY 只对 api.binance.com 放行；其他域名、其他头一律丢弃（代理侧这一层与 Collector 侧各自把关）。
func TestFilterRequestHeadersAllowsAPIKeyOnlyForBinanceSpotAPI(t *testing.T) {
	values := map[string]string{
		"x-mbx-apikey": "public-key-id", "Accept": "application/json", "Authorization": "must-not-pass", "X-Other": "must-not-pass",
	}
	headers, err := filterRequestHeaders("api.binance.com", values)
	require.NoError(t, err)
	require.Equal(t, "public-key-id", headers.Get("X-MBX-APIKEY"))
	require.Equal(t, "application/json", headers.Get("Accept"))
	require.Empty(t, headers.Get("Authorization"))
	require.Empty(t, headers.Get("X-Other"))

	for _, host := range []string{"fapi.binance.com", "www.okx.com", "api.binance.com.evil.example", ""} {
		headers, err := filterRequestHeaders(host, values)
		require.NoError(t, err)
		require.Empty(t, headers.Get("X-MBX-APIKEY"), host)
		require.Equal(t, "application/json", headers.Get("Accept"), host)
	}

	// 同一个头出现两次（大小写不同）仍然按重复拒绝。
	_, err = filterRequestHeaders("api.binance.com", map[string]string{"X-MBX-APIKEY": "a", "x-mbx-apikey": "b"})
	require.Error(t, err)
	_, err = filterRequestHeaders("api.binance.com", map[string]string{"X-MBX-APIKEY": "a\r\nInjected: 1"})
	require.Error(t, err)
}

// 经过代理真正发出的请求里带有该头（只对 api.binance.com），其他域名即使请求里带了也不会发出。
func TestDoForwardsAPIKeyHeaderToBinanceSpotAPI(t *testing.T) {
	captured := make(chan string, 2)
	p, _ := tlsFixture(t, func(w http.ResponseWriter, r *http.Request) {
		captured <- r.Header.Get("X-MBX-APIKEY")
		_, _ = w.Write([]byte(`{}`))
	})
	for host, want := range map[string]string{"api.binance.com": "public-key-id", "fapi.binance.com": ""} {
		req := request()
		req.Host = host
		req.Path = "/sapi/v1/spot/delist-schedule"
		req.Query = ""
		req.Headers = map[string]string{"X-MBX-APIKEY": "public-key-id"}
		rsp, err := p.Do(context.Background(), req)
		require.NoError(t, err)
		require.EqualValues(t, 200, rsp.Status, host)
		require.Equal(t, want, <-captured, host)
	}
}
