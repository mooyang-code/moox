package exchangelist

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// hostRouter 是假的网络：把请求按主机名转给 push2 或 push2test 的假服务，并按主机、市场计数。
// 全程不碰真实网络；push2Down 为真时对 push2 的请求立即返回连接错误（模拟被断开）。
type hostRouter struct {
	mu        sync.Mutex
	push2     *httptest.Server
	push2test *httptest.Server
	push2Down bool
	// perHost 是 {主机: {市场 fs: 请求次数}}，每次请求（含失败的）都计入。
	perHost map[string]map[string]int
}

func newHostRouter(push2, push2test *httptest.Server) *hostRouter {
	return &hostRouter{push2: push2, push2test: push2test, perHost: map[string]map[string]int{}}
}

func (r *hostRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	r.mu.Lock()
	if r.perHost[host] == nil {
		r.perHost[host] = map[string]int{}
	}
	r.perHost[host][req.URL.Query().Get("fs")]++
	down := r.push2Down && host == "push2.eastmoney.com"
	r.mu.Unlock()
	if down {
		return nil, errors.New("假网络：连接被对端断开")
	}
	var target *httptest.Server
	switch host {
	case "push2.eastmoney.com":
		target = r.push2
	case "push2test.eastmoney.com":
		target = r.push2test
	default:
		return nil, errors.New("假网络：不认识的主机 " + host)
	}
	parsed, err := url.Parse(target.URL)
	if err != nil {
		return nil, err
	}
	rewritten := req.Clone(req.Context())
	rewritten.URL.Scheme = parsed.Scheme
	rewritten.URL.Host = parsed.Host
	return http.DefaultTransport.RoundTrip(rewritten)
}

func (r *hostRouter) requests(host, fs string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.perHost[host][fs]
}

func (r *hostRouter) total(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	sum := 0
	for _, n := range r.perHost[host] {
		sum += n
	}
	return sum
}

func newRoutedConfig(t *testing.T, push2Down bool) (Config, *hostRouter, *fakeClist) {
	t.Helper()
	lists, _ := sampleLists(t)
	fake := newFakeClist(lists)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	// 两个域名由同一个假服务提供（push2test 是备用域名，内容相同）。
	router := newHostRouter(server, server)
	router.push2Down = push2Down
	cfg := testConfig("") // BaseURL 留空：默认主地址 push2
	cfg.HTTPClient = &http.Client{Transport: router}
	return cfg, router, fake
}

// push2 立即断开：第一个市场换到 push2test 之后，后两个市场的第 1 页不再请求 push2。
func TestLookupCarriesFallbackBaseToLaterMarkets(t *testing.T) {
	cfg, router, fake := newRoutedConfig(t, true)
	exchanges, err := Lookup(context.Background(), cfg, sp500Codes(t))
	require.NoError(t, err)
	require.Len(t, exchanges, 503)

	require.Equal(t, 1, router.requests("push2.eastmoney.com", "m:105"), "第一个市场只在 push2 上失败一次，之后换到 push2test")
	require.Zero(t, router.requests("push2.eastmoney.com", "m:106"), "第二个市场不再请求 push2")
	require.Zero(t, router.requests("push2.eastmoney.com", "m:107"), "第三个市场不再请求 push2")
	require.Equal(t, 1, router.total("push2.eastmoney.com"), "push2 合计只被请求一次")
	require.Equal(t, fake.pages("m:105")+fake.pages("m:106")+fake.pages("m:107"), router.total("push2test.eastmoney.com"),
		"所有页都由 push2test 提供，一页一次")
	require.Equal(t, router.total("push2test.eastmoney.com"), fake.count(), "假服务只收到 push2test 的请求")
}

// push2 正常：三个市场都用 push2，没有任何请求发给 push2test。
func TestLookupKeepsPrimaryBaseWhenHealthy(t *testing.T) {
	cfg, router, fake := newRoutedConfig(t, false)
	_, err := Lookup(context.Background(), cfg, sp500Codes(t))
	require.NoError(t, err)
	for _, fs := range []string{"m:105", "m:106", "m:107"} {
		require.Equal(t, fake.pages(fs), router.requests("push2.eastmoney.com", fs), fs)
	}
	require.Zero(t, router.total("push2test.eastmoney.com"), "push2 正常时不请求备用域名")
	require.Equal(t, fake.pages("m:105")+fake.pages("m:106")+fake.pages("m:107"), fake.count())
}

// 请求总数的最坏值不变：push2 一直断开且 push2test 一直 5xx 时，第一个市场第 1 页只在两个地址各试 PageRetries+1 次，
// 整次失败，后面的市场一页都不读。
func TestLookupFailoverKeepsRequestBound(t *testing.T) {
	lists, _ := sampleLists(t)
	fake := newFakeClist(lists)
	fake.fail = func(string, int, int) int { return http.StatusBadGateway }
	server := httptest.NewServer(fake)
	defer server.Close()
	router := newHostRouter(server, server)
	router.push2Down = true
	cfg := testConfig("")
	cfg.HTTPClient = &http.Client{Transport: router}
	_, err := Lookup(context.Background(), cfg, []string{"AAPL"})
	require.Error(t, err)
	require.Equal(t, cfg.withDefaults().PageRetries+1, router.total("push2.eastmoney.com"))
	require.Equal(t, cfg.withDefaults().PageRetries+1, router.total("push2test.eastmoney.com"))
	for _, request := range fake.requests {
		require.True(t, strings.HasPrefix(request, "m:105#1"), request)
	}
}
