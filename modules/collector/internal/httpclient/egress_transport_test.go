package httpclient

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

// fakeEgress 记录收到的 DoReq，并按 respond 返回结果。
type fakeEgress struct {
	mu       sync.Mutex
	requests []*egresspb.DoReq
	deadline time.Time
	respond  func(*egresspb.DoReq) (*egresspb.DoRsp, error)
}

func (f *fakeEgress) Do(ctx context.Context, req *egresspb.DoReq, _ ...client.Option) (*egresspb.DoRsp, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.deadline, _ = ctx.Deadline()
	f.mu.Unlock()
	return f.respond(req)
}

func (f *fakeEgress) last() *egresspb.DoReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return nil
	}
	return f.requests[len(f.requests)-1]
}

func binanceWhitelist(t *testing.T) egresspb.DomainList {
	t.Helper()
	domains, err := egresspb.ParseDomainList([]string{"*.binance.com", "data-api.binance.vision"})
	require.NoError(t, err)
	return domains
}

func okResponse(status int32, body string, headers map[string]string) *egresspb.DoRsp {
	return &egresspb.DoRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Status: status, Body: []byte(body), Headers: headers}
}

func TestEgressHTTPClientSendsWhitelistedRequestsThroughProxy(t *testing.T) {
	egress := &fakeEgress{respond: func(*egresspb.DoReq) (*egresspb.DoRsp, error) {
		return okResponse(http.StatusOK, `{"symbols":[{"symbol":"BTCUSDT"}]}`, map[string]string{
			"Content-Type": "application/json", "Content-Encoding": "gzip", "Content-Length": "999", "X-Mbx-Used-Weight-1m": "20",
		}), nil
	}}
	httpClient := NewEgressHTTPClient(binanceWhitelist(t), egress)
	var result struct {
		Symbols []struct{ Symbol string } `json:"symbols"`
	}
	query := url.Values{"symbol": []string{"BTCUSDT"}, "limit": []string{"2"}}
	require.NoError(t, httpClient.Get(context.Background(), "FAPI.binance.com", "/fapi/v1/exchangeInfo", query, &result))
	require.Equal(t, "BTCUSDT", result.Symbols[0].Symbol, "响应体按原样解码；解压由出口代理完成，客户端不能再按 gzip 解码")

	req := egress.last()
	require.NotNil(t, req)
	require.Equal(t, http.MethodGet, req.GetMethod())
	require.Equal(t, "fapi.binance.com", req.GetHost())
	require.Equal(t, "/fapi/v1/exchangeInfo", req.GetPath())
	require.Equal(t, "limit=2&symbol=BTCUSDT", req.GetQuery())
	require.Equal(t, "moox-collector/1.0", req.GetHeaders()["User-Agent"])
	require.Empty(t, req.GetBody())
	// 客户端整体超时 60 秒，给出口代理的超时要留出回程的余量，且不超过出口代理的上限。
	require.Greater(t, req.GetTimeoutMs(), int32(55_000))
	require.LessOrEqual(t, req.GetTimeoutMs(), int32(60_000))
	require.Less(t, time.Duration(req.GetTimeoutMs())*time.Millisecond, time.Until(egress.deadline))
}

func TestEgressHTTPClientPassesUpstreamStatusToRetryLogic(t *testing.T) {
	for _, status := range []int32{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusFound} {
		egress := &fakeEgress{respond: func(*egresspb.DoReq) (*egresspb.DoRsp, error) {
			return okResponse(status, "busy", map[string]string{"Location": "https://evil.example.com/"}), nil
		}}
		err := NewEgressHTTPClient(binanceWhitelist(t), egress).Get(context.Background(), "api.binance.com", "/api/v3/exchangeInfo", nil, nil)
		var statusErr *StatusError
		require.ErrorAs(t, err, &statusErr)
		require.Equal(t, int(status), statusErr.StatusCode)
		require.Len(t, egress.requests, 1, "重定向不跟随，避免把请求带到白名单之外直连")
	}
}

func TestEgressHTTPClientReportsProxyFailures(t *testing.T) {
	for name, respond := range map[string]func(*egresspb.DoReq) (*egresspb.DoRsp, error){
		"调用失败": func(*egresspb.DoReq) (*egresspb.DoRsp, error) { return nil, errors.New("connection refused") },
		"代理拒绝": func(*egresspb.DoReq) (*egresspb.DoRsp, error) {
			return &egresspb.DoRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NO_PERMISSION, Msg: "域名不在白名单内"}}, nil
		},
		"空响应":   func(*egresspb.DoReq) (*egresspb.DoRsp, error) { return &egresspb.DoRsp{}, nil },
		"状态码无效": func(*egresspb.DoReq) (*egresspb.DoRsp, error) { return okResponse(0, "", nil), nil },
	} {
		egress := &fakeEgress{respond: respond}
		err := NewEgressHTTPClient(binanceWhitelist(t), egress).Get(context.Background(), "api.binance.com", "/api/v3/ping", nil, nil)
		require.ErrorContains(t, err, "经出口代理请求 api.binance.com 失败", name)
		var statusErr *StatusError
		require.False(t, errors.As(err, &statusErr), "%s：代理自身的错误不能当成目标的状态码", name)
	}
}

func TestEgressTransportSendsOtherDomainsDirectly(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"direct"}`)
	}))
	defer server.Close()
	egress := &fakeEgress{respond: func(*egresspb.DoReq) (*egresspb.DoRsp, error) {
		t.Fatal("白名单之外的域名不应经过出口代理")
		return nil, nil
	}}
	direct := server.Client()
	direct.Transport = NewEgressTransport(binanceWhitelist(t), egress, direct.Transport)
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	var result map[string]string
	require.NoError(t, NewHTTPClient(direct).Get(context.Background(), parsed.Host, "/", nil, &result))
	require.Equal(t, "direct", result["status"])
}

func TestEgressTransportRejectsRequestsTheProxyCannotSend(t *testing.T) {
	egress := &fakeEgress{respond: func(*egresspb.DoReq) (*egresspb.DoRsp, error) {
		t.Fatal("不应发出请求")
		return nil, nil
	}}
	transport := NewEgressTransport(binanceWhitelist(t), egress, http.DefaultTransport)
	for rawURL, want := range map[string]string{
		"http://api.binance.com/api/v3/ping":       "只发 HTTPS",
		"https://api.binance.com:8443/api/v3/ping": "只访问 443 端口",
	} {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		require.NoError(t, err)
		_, err = transport.RoundTrip(req)
		require.ErrorContains(t, err, want, rawURL)
	}
	req, err := http.NewRequest(http.MethodPost, "https://api.binance.com/api/v3/order", strings.NewReader(strings.Repeat("x", maxEgressRequestBody+1)))
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.ErrorContains(t, err, "请求体超过")
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	req, err = http.NewRequestWithContext(expired, http.MethodGet, "https://api.binance.com/api/v3/ping", nil)
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestEgressTransportForwardsPostBodyAndOmitsTimeoutWithoutDeadline(t *testing.T) {
	egress := &fakeEgress{respond: func(*egresspb.DoReq) (*egresspb.DoRsp, error) {
		return okResponse(http.StatusOK, "{}", nil), nil
	}}
	transport := NewEgressTransport(binanceWhitelist(t), egress, http.DefaultTransport)
	req, err := http.NewRequest(http.MethodPost, "https://data-api.binance.vision/api/v3/query?x=1", strings.NewReader(`{"a":1}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	rsp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	body, err := io.ReadAll(rsp.Body)
	require.NoError(t, err)
	require.Equal(t, "{}", string(body))
	require.EqualValues(t, 2, rsp.ContentLength)
	got := egress.last()
	require.Equal(t, `{"a":1}`, string(got.GetBody()))
	require.Equal(t, "application/json", got.GetHeaders()["Content-Type"])
	require.Equal(t, "x=1", got.GetQuery())
	require.Zero(t, got.GetTimeoutMs(), "请求没有截止时间时由出口代理使用默认超时")
}

func TestEgressTransportInflatesCompressedBodyWithinLimit(t *testing.T) {
	gzipped := func(plain string) []byte {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		_, _ = io.WriteString(writer, plain)
		require.NoError(t, writer.Close())
		return buffer.Bytes()
	}
	req := httptest.NewRequest(http.MethodGet, "https://api.binance.com/api/v3/exchangeInfo", nil)

	rsp, err := egressResponse(req, &egresspb.DoRsp{
		Status: http.StatusOK, Body: gzipped(`{"symbols":[]}`),
		Headers: map[string]string{egressBodyEncodingHeader: "gzip", "Content-Type": "application/json"},
	})
	require.NoError(t, err)
	body, err := io.ReadAll(rsp.Body)
	require.NoError(t, err)
	require.Equal(t, `{"symbols":[]}`, string(body))
	require.Equal(t, int64(len(body)), rsp.ContentLength)
	require.Empty(t, rsp.Header.Get(egressBodyEncodingHeader))

	_, err = egressResponse(req, &egresspb.DoRsp{Status: http.StatusOK, Body: []byte("不是 gzip"), Headers: map[string]string{egressBodyEncodingHeader: "gzip"}})
	require.ErrorContains(t, err, "解压失败")

	_, err = gunzipLimited(gzipped(strings.Repeat("x", 2048)), 1024)
	require.ErrorContains(t, err, "超过 1024 字节")
}
