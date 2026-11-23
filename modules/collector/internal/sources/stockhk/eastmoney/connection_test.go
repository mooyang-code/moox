package eastmoney

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/eastmoneykline"
	"github.com/stretchr/testify/require"
)

// targetRequest 是 14:45 那根的定时取数请求，now 在边界后 5 秒。
func targetRequest() marketdata.KlineRequest {
	return marketdata.KlineRequest{
		MarketID: "stockhk", ExchangeID: "XHKG", InstrumentType: marketdata.InstrumentEquity, SubjectID: "00836.XHKG", ProviderSymbol: "00836",
		Frequency: "5m", Limit: 1, StartTime: hkt("2026-11-12", "14:45:00"), EndTime: hkt("2026-11-12", "14:50:00"),
		Now: hkt("2026-11-12", "14:50:05"), RequestID: "req-target",
	}
}

const targetLine = "2026-11-12 14:50,20.460,20.420,20.460,20.420,24000,490280.000"

// 当前域名连续 6 次网络错误（中间没有成功）才换到下一个编号子域名，之后的请求留在新域名上；
// 新域名也整个不通就再换，三个用完回到 push2his。
func TestFetchKlinesRotatesHostsAfterConsecutiveNetworkErrors(t *testing.T) {
	var hosts []string
	down := map[string]bool{"push2his.eastmoney.com": true}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hosts = append(hosts, req.URL.Host)
		if down[req.URL.Host] {
			return nil, io.EOF
		}
		recorder := httptest.NewRecorder()
		_, _ = recorder.Write(klinesBody(targetLine))
		return recorder.Result(), nil
	})}
	provider := New(Config{HTTPClient: client, Now: func() time.Time { return hkt("2026-11-12", "14:50:05") }})
	fetch := func() error {
		_, err := provider.FetchKlines(context.Background(), targetRequest())
		return err
	}
	failTimes := func(n int) {
		for range n {
			require.ErrorIs(t, fetch(), marketdata.ErrTimeout, "网络错误仍按超时类报出，流水线照常重试")
		}
	}
	repeat := func(host string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = host
		}
		return out
	}

	failTimes(6)
	require.NoError(t, fetch())
	require.NoError(t, fetch())
	require.Equal(t, append(repeat("push2his.eastmoney.com", 6), "33.push2his.eastmoney.com", "33.push2his.eastmoney.com"), hosts)

	down = map[string]bool{"33.push2his.eastmoney.com": true, "72.push2his.eastmoney.com": true}
	hosts = nil
	failTimes(12)
	require.NoError(t, fetch())
	require.Equal(t, append(append(repeat("33.push2his.eastmoney.com", 6), repeat("72.push2his.eastmoney.com", 6)...), "push2his.eastmoney.com"), hosts)
}

// 部分请求被断开、成功与失败交替时不换域名：换了就要在新域名上重新建连接，而被断开的正是新建连接。
func TestFetchKlinesKeepsHostWhileSomeRequestsSucceed(t *testing.T) {
	var hosts []string
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hosts = append(hosts, req.URL.Host)
		calls++
		if calls%4 != 0 {
			return nil, io.EOF
		}
		recorder := httptest.NewRecorder()
		_, _ = recorder.Write(klinesBody(targetLine))
		return recorder.Result(), nil
	})}
	provider := New(Config{HTTPClient: client, Now: func() time.Time { return hkt("2026-11-12", "14:50:05") }})
	for range 20 {
		_, _ = provider.FetchKlines(context.Background(), targetRequest())
	}
	for _, host := range hosts {
		require.Equal(t, "push2his.eastmoney.com", host, "每 4 次有 1 次成功，连续失败到不了 6 次")
	}
}

// 调用方取消不是域名的问题，不计入连续失败；HTTP 状态码错误说明连接是通的，清零。Trace 里状态码错误记成 status_<码>。
func TestFetchKlinesIgnoresCancelAndHTTPStatusForRotation(t *testing.T) {
	var hosts []string
	status := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hosts = append(hosts, req.URL.Host)
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		recorder := httptest.NewRecorder()
		if status != 0 {
			recorder.WriteHeader(status)
		}
		_, _ = recorder.Write(klinesBody(targetLine))
		return recorder.Result(), nil
	})}
	provider := New(Config{HTTPClient: client, Now: func() time.Time { return hkt("2026-11-12", "14:50:05") }})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 10 {
		_, err := provider.FetchKlines(ctx, targetRequest())
		require.True(t, errors.Is(err, marketdata.ErrTimeout))
	}
	trace := &httpclient.Trace{}
	status = http.StatusServiceUnavailable
	_, err := provider.FetchKlines(httpclient.WithTrace(context.Background(), trace), targetRequest())
	require.ErrorIs(t, err, marketdata.ErrHTTPStatus)
	require.Equal(t, "status_503", trace.Snapshot().Attempts[0].Kind)
	status = http.StatusTooManyRequests
	_, err = provider.FetchKlines(context.Background(), targetRequest())
	require.ErrorIs(t, err, marketdata.ErrRateLimited)
	status = 0
	_, err = provider.FetchKlines(context.Background(), targetRequest())
	require.NoError(t, err)
	for _, host := range hosts {
		require.Equal(t, "push2his.eastmoney.com", host)
	}
	require.Len(t, hosts, 13)
}

// 东方财富的请求记进流水线挂的 Trace：CLS 的逐项上报才有阶段、连接与对端地址；失败的那次记下 eof。
func TestFetchKlinesRecordsRequestsInTrace(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, io.EOF
		}
		recorder := httptest.NewRecorder()
		_, _ = recorder.Write(klinesBody(targetLine))
		return recorder.Result(), nil
	})}
	provider := New(Config{HTTPClient: client, Now: func() time.Time { return hkt("2026-11-12", "14:50:05") }})
	trace := &httpclient.Trace{}
	ctx := httpclient.WithTrace(context.Background(), trace)
	_, err := provider.FetchKlines(ctx, targetRequest())
	require.Error(t, err)
	_, err = provider.FetchKlines(ctx, targetRequest())
	require.NoError(t, err)

	snap := trace.Snapshot()
	require.Equal(t, 2, snap.Requests)
	require.Equal(t, http.StatusOK, snap.LastStatus)
	require.Equal(t, "push2his.eastmoney.com", snap.LastDomain, "一次失败不换域名")
	require.Len(t, snap.Attempts, 2)
	require.Equal(t, "push2his.eastmoney.com", snap.Attempts[0].Domain)
	require.Equal(t, "eof", snap.Attempts[0].Kind)
	require.Equal(t, "done", snap.Attempts[1].Phase)
}

// 默认客户端的空闲连接数不小于各市场的并发上限，一波并发请求结束后连接留着给后面的请求复用。
func TestDefaultHTTPClientKeepsIdleConnectionsForConcurrentRequests(t *testing.T) {
	client := eastmoneykline.NewHTTPClient(time.Second)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.GreaterOrEqual(t, transport.MaxIdleConnsPerHost, 16)
	require.Equal(t, time.Second, client.Timeout)
	require.NotSame(t, http.DefaultTransport, client.Transport, "不改进程共用的默认连接池")
}
