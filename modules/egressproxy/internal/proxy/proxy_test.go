package proxy

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/egressproxy/internal/resolver"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

// testProxy 启动一个 HTTPS 目标，返回把所有域名都连到这个目标的出口代理。
func testProxy(t *testing.T, maxBody int64, handler http.HandlerFunc) (*Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(backend.Close)
	transport := backend.Client().Transport.(*http.Transport).Clone()
	address := backend.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	// httptest 的证书签给 example.com，用它校验目标证书。
	transport.TLSClientConfig.ServerName = "example.com"
	server, err := New(Config{
		Domains: []string{"*.binance.com", "data-api.binance.vision"}, MaxResponseBytes: maxBody, Transport: transport,
	}, nil, nil)
	require.NoError(t, err)
	return server, &calls
}

func TestDomainListMatchesExactAndWildcard(t *testing.T) {
	list, err := NewDomainList([]string{"*.binance.com", "Data-API.binance.vision"})
	require.NoError(t, err)
	for host, want := range map[string]bool{
		"fapi.binance.com": true, "a.b.binance.com": true, "data-api.binance.vision": true,
		"binance.com": false, "evilbinance.com": false, "binance.com.evil.io": false, "api.binance.vision": false,
	} {
		require.Equal(t, want, list.Allows(host), host)
	}
	for _, entries := range [][]string{nil, {"*."}, {"1.2.3.4"}, {"https://fapi.binance.com"}, {"fapi.binance.com:443"}} {
		_, err := NewDomainList(entries)
		require.Error(t, err, "%v", entries)
	}
}

func TestDoForwardsAllowedRequestAndFiltersHeaders(t *testing.T) {
	server, calls := testProxy(t, 0, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/fapi/v1/exchangeInfo", r.URL.Path)
		require.Equal(t, "symbol=BTCUSDT&limit=2", r.URL.RawQuery)
		require.Equal(t, "fapi.binance.com", r.Host, "请求发往调用方指定的域名")
		require.Equal(t, "moox-collector", r.Header.Get("User-Agent"))
		require.Empty(t, r.Header.Get("Authorization"), "白名单之外的请求头不转发")
		require.Empty(t, r.Header.Get("Cookie"))
		w.Header().Set("X-Mbx-Used-Weight-1m", "12")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"symbols":[]}`)
	})
	rsp, err := server.Do(context.Background(), &egresspb.DoReq{
		Method: "get", Host: "FAPI.binance.com", Path: "/fapi/v1/exchangeInfo", Query: "symbol=BTCUSDT&limit=2",
		Headers: map[string]string{"user-agent": "moox-collector", "Authorization": "secret", "Cookie": "c=1"},
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.EqualValues(t, http.StatusOK, rsp.GetStatus())
	require.Equal(t, `{"symbols":[]}`, string(rsp.GetBody()))
	require.Equal(t, "12", rsp.GetHeaders()["X-Mbx-Used-Weight-1m"])
	require.EqualValues(t, 1, calls.Load())
}

func TestDoRejectsDomainOutsideAllowlist(t *testing.T) {
	server, calls := testProxy(t, 0, func(http.ResponseWriter, *http.Request) {})
	for _, host := range []string{"evil.example.com", "binance.com", "fapi.binance.com.evil.io"} {
		rsp, err := server.Do(context.Background(), &egresspb.DoReq{Method: "GET", Host: host, Path: "/"})
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_NO_PERMISSION, rsp.GetRetInfo().GetCode(), host)
	}
	require.Zero(t, calls.Load(), "白名单之外的域名不能发出请求")
}

func TestDoPassesThroughUpstreamErrorStatus(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusInternalServerError} {
		server, _ := testProxy(t, 0, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"code":-1003}`)
		})
		rsp, err := server.Do(context.Background(), &egresspb.DoReq{Method: "GET", Host: "api.binance.com", Path: "/api/v3/klines"})
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), "目标返回的 HTTP 错误不是代理错误")
		require.EqualValues(t, status, rsp.GetStatus())
		require.Equal(t, "3", rsp.GetHeaders()["Retry-After"])
		require.Equal(t, `{"code":-1003}`, string(rsp.GetBody()))
	}
}

func TestDoDecompressesAndBoundsResponse(t *testing.T) {
	payload := strings.Repeat("k", 4096)
	server, _ := testProxy(t, 8192, func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.Header.Get("Accept-Encoding"), "gzip", "出口代理自己请求压缩")
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = io.WriteString(writer, payload)
		_ = writer.Close()
	})
	rsp, err := server.Do(context.Background(), &egresspb.DoReq{Method: "GET", Host: "api.binance.com", Path: "/api/v3/exchangeInfo"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, payload, string(rsp.GetBody()), "返回解压后的响应")
	require.NotContains(t, rsp.GetHeaders(), "Content-Encoding")

	large, _ := testProxy(t, 1024, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 2048))
	})
	rsp, err = large.Do(context.Background(), &egresspb.DoReq{Method: "GET", Host: "api.binance.com", Path: "/big"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "超过 1024 字节")
	require.Empty(t, rsp.GetBody())
}

func TestDoDoesNotFollowRedirects(t *testing.T) {
	server, calls := testProxy(t, 0, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.com/steal", http.StatusFound)
	})
	rsp, err := server.Do(context.Background(), &egresspb.DoReq{Method: "GET", Host: "api.binance.com", Path: "/moved"})
	require.NoError(t, err)
	require.EqualValues(t, http.StatusFound, rsp.GetStatus(), "重定向原样返回给调用方")
	require.Equal(t, "https://evil.example.com/steal", rsp.GetHeaders()["Location"])
	require.EqualValues(t, 1, calls.Load())
}

func TestDoPostsBodyAndRejectsInvalidRequests(t *testing.T) {
	server, calls := testProxy(t, 0, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		_, _ = w.Write(body)
	})
	rsp, err := server.Do(context.Background(), &egresspb.DoReq{
		Method: "POST", Host: "api.binance.com", Path: "/echo", Body: []byte(`{"a":1}`), Headers: map[string]string{"Content-Type": "application/json"},
	})
	require.NoError(t, err)
	require.Equal(t, `{"a":1}`, string(rsp.GetBody()))
	require.EqualValues(t, 1, calls.Load())

	for name, req := range map[string]*egresspb.DoReq{
		"nil":            nil,
		"method":         {Method: "PUT", Host: "api.binance.com", Path: "/"},
		"port":           {Method: "GET", Host: "api.binance.com:443", Path: "/"},
		"ip":             {Method: "GET", Host: "1.2.3.4", Path: "/"},
		"relative path":  {Method: "GET", Host: "api.binance.com", Path: "api/v3"},
		"path query":     {Method: "GET", Host: "api.binance.com", Path: "/a?b=1"},
		"crlf query":     {Method: "GET", Host: "api.binance.com", Path: "/a", Query: "a=1\r\nX: y"},
		"timeout":        {Method: "GET", Host: "api.binance.com", Path: "/", TimeoutMs: 60001},
		"get with body":  {Method: "GET", Host: "api.binance.com", Path: "/", Body: []byte("x")},
		"bad path utf-8": {Method: "GET", Host: "api.binance.com", Path: "/%zz"},
	} {
		rsp, err := server.Do(context.Background(), req)
		require.NoError(t, err, name)
		require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), name)
	}
	require.EqualValues(t, 1, calls.Load(), "无效请求不能发出")
}

func TestDoReportsTransportFailureAndTimeout(t *testing.T) {
	server, err := New(Config{Domains: []string{"api.binance.com"}, Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}, nil, nil)
	require.NoError(t, err)
	rsp, err := server.Do(context.Background(), &egresspb.DoReq{Method: "GET", Host: "api.binance.com", Path: "/"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "connection refused")
	require.Zero(t, rsp.GetStatus())

	slow, _ := testProxy(t, 0, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	rsp, err = slow.Do(context.Background(), &egresspb.DoReq{Method: "GET", Host: "api.binance.com", Path: "/slow", TimeoutMs: 100})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode(), "超时是代理错误")
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	_, err := New(Config{Domains: []string{"api.binance.com"}, Headers: []string{"Accept-Encoding"}}, nil, nil)
	require.Error(t, err, "压缩由出口代理处理，不能转发调用方的 Accept-Encoding")
	_, err = New(Config{Domains: []string{"api.binance.com"}, DefaultTimeout: 2 * time.Minute}, nil, nil)
	require.Error(t, err)
	_, err = New(Config{}, nil, nil)
	require.Error(t, err)
}

// 以下与交易服务原 DNSResolverServer 的测试一致，确认迁移后结果不变。

func TestResolveDomainsMapsResultsAndUnresolvedDomains(t *testing.T) {
	service := dnsServer(t, resolver.New(resolver.Config{
		Domains: []string{"good.example.com", "bad.example.com"},
		LookupHost: func(_ context.Context, domain string) ([]string, error) {
			if domain == "bad.example.com" {
				return nil, errors.New("lookup failed")
			}
			return []string{"8.8.8.8"}, nil
		},
		DialContext: func(context.Context, string, string) (net.Conn, error) { return &fakeConn{}, nil },
	}))
	rsp, err := service.ResolveDomains(context.Background(), &egresspb.ResolveDomainsReq{
		Domains: []string{"good.example.com", "bad.example.com"}, MaxIpsPerDomain: 1,
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetResolutions(), 1)
	require.Equal(t, "good.example.com", rsp.GetResolutions()[0].GetDomain())
	require.Equal(t, "8.8.8.8", rsp.GetResolutions()[0].GetIps()[0].GetIp())
	require.GreaterOrEqual(t, rsp.GetResolutions()[0].GetIps()[0].GetTcpConnectLatencyMs(), uint32(1))
	require.Equal(t, []string{"bad.example.com"}, rsp.GetUnresolvedDomains())
}

func TestResolveDomainsMapsInvalidAndDisabled(t *testing.T) {
	service := dnsServer(t, resolver.New(resolver.Config{Domains: []string{"good.example.com"}}))
	rsp, err := service.ResolveDomains(context.Background(), &egresspb.ResolveDomainsReq{Domains: []string{"https://good.example.com"}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	rsp, err = service.ResolveDomains(context.Background(), &egresspb.ResolveDomainsReq{Domains: []string{"good.example.com"}, MaxIpsPerDomain: 5})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "每个域名最多 4 个地址")

	disabled := dnsServer(t, nil)
	rsp, err = disabled.ResolveDomains(context.Background(), &egresspb.ResolveDomainsReq{Domains: []string{"good.example.com"}})
	require.NoError(t, err)
	require.NotEqual(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	rsp, err = service.ResolveDomains(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func dnsServer(t *testing.T, dns *resolver.Resolver) *Server {
	t.Helper()
	server, err := New(Config{Domains: []string{"api.binance.com"}}, dns, nil)
	require.NoError(t, err)
	return server
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeConn struct{}

func (*fakeConn) Read([]byte) (int, error)         { return 0, errors.New("not implemented") }
func (*fakeConn) Write([]byte) (int, error)        { return 0, errors.New("not implemented") }
func (*fakeConn) Close() error                     { return nil }
func (*fakeConn) LocalAddr() net.Addr              { return fakeAddr("local") }
func (*fakeConn) RemoteAddr() net.Addr             { return fakeAddr("remote") }
func (*fakeConn) SetDeadline(time.Time) error      { return nil }
func (*fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (*fakeConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }
