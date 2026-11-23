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

// 东方财富把新建连接断开时（EOF），下一次尝试换到 push2his 的编号子域名，之后的请求留在新域名上；
// 新域名也断开就再换下一个，三个用完回到 push2his。
func TestFetchKlinesRotatesHostsOnNetworkErrors(t *testing.T) {
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

	_, err := provider.FetchKlines(context.Background(), targetRequest())
	require.ErrorIs(t, err, marketdata.ErrTimeout, "网络错误仍按超时类报出，流水线照常重试")
	rows, err := provider.FetchKlines(context.Background(), targetRequest())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	_, err = provider.FetchKlines(context.Background(), targetRequest())
	require.NoError(t, err)
	require.Equal(t, []string{"push2his.eastmoney.com", "33.push2his.eastmoney.com", "33.push2his.eastmoney.com"}, hosts)

	down["33.push2his.eastmoney.com"] = true
	hosts = nil
	_, err = provider.FetchKlines(context.Background(), targetRequest())
	require.Error(t, err)
	_, err = provider.FetchKlines(context.Background(), targetRequest())
	require.NoError(t, err)
	down = map[string]bool{"72.push2his.eastmoney.com": true}
	_, err = provider.FetchKlines(context.Background(), targetRequest())
	require.Error(t, err)
	_, err = provider.FetchKlines(context.Background(), targetRequest())
	require.NoError(t, err)
	require.Equal(t, []string{"33.push2his.eastmoney.com", "72.push2his.eastmoney.com", "72.push2his.eastmoney.com", "push2his.eastmoney.com"}, hosts)
}

// 调用方主动取消（备援请求另一路先成功）不是域名的问题，不换；HTTP 状态码错误说明连接是通的，也不换。
func TestFetchKlinesKeepsHostOnCancelAndHTTPStatus(t *testing.T) {
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
	_, err := provider.FetchKlines(ctx, targetRequest())
	require.True(t, errors.Is(err, marketdata.ErrTimeout))
	status = http.StatusServiceUnavailable
	_, err = provider.FetchKlines(context.Background(), targetRequest())
	require.ErrorIs(t, err, marketdata.ErrHTTPStatus)
	status = 0
	_, err = provider.FetchKlines(context.Background(), targetRequest())
	require.NoError(t, err)
	require.Equal(t, []string{"push2his.eastmoney.com", "push2his.eastmoney.com", "push2his.eastmoney.com"}, hosts)
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
	require.Equal(t, "33.push2his.eastmoney.com", snap.LastDomain)
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
