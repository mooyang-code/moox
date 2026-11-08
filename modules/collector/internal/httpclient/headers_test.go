package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

// X-MBX-APIKEY 只对 api.binance.com 经出口代理放行：别的域名、别的头仍然被丢弃。
func TestEgressTransportAllowsAPIKeyHeaderOnlyForBinanceSpotAPI(t *testing.T) {
	for host, want := range map[string]bool{"api.binance.com": true, "fapi.binance.com": false, "www.okx.com": false} {
		var sent map[string]string
		gateway := egressInvoker(func(_ context.Context, _, _ string, request, response any) error {
			sent = request.(*egresspb.DoReq).Headers
			*response.(*egresspb.DoRsp) = egresspb.DoRsp{RetInfo: &commonpb.RetInfo{}, Status: 200, Body: []byte("{}")}
			return nil
		})
		transport, err := NewEgressTransport(gateway, []string{"*.binance.com", "www.okx.com"}, directTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("proxy request reached direct transport")
			return nil, nil
		}))
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodGet, "https://"+host+"/sapi/v1/spot/delist-schedule", nil)
		require.NoError(t, err)
		req.Header.Set("X-MBX-APIKEY", "public-key-id")
		req.Header.Set("Authorization", "must-not-leave")
		req.Header.Set("X-Other", "must-not-leave")
		rsp, err := transport.RoundTrip(req)
		require.NoError(t, err)
		_ = rsp.Body.Close()
		if want {
			require.Equal(t, map[string]string{"X-Mbx-Apikey": "public-key-id"}, sent, host)
		} else {
			require.Empty(t, sent, host)
		}
	}
}

func TestWithRequestHeadersAreSentAndOnlyOnThatContext(t *testing.T) {
	var got []http.Header
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Clone())
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client := NewHTTPClient(server.Client())
	domain := server.Listener.Addr().String()

	ctx := WithRequestHeaders(context.Background(), http.Header{"X-Mbx-Apikey": {"public-key-id"}})
	require.NoError(t, client.Get(ctx, domain, "/with", nil, nil))
	require.NoError(t, client.Get(context.Background(), domain, "/without", nil, nil))
	require.Len(t, got, 2)
	require.Equal(t, "public-key-id", got[0].Get("X-MBX-APIKEY"))
	require.Equal(t, "moox-collector/1.0", got[0].Get("User-Agent"), "额外头不覆盖默认头")
	require.Empty(t, got[1].Get("X-MBX-APIKEY"), "不带头的请求不受影响")
}
