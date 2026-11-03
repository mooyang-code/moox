package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func TestDoPausesHostAfterRateLimitWithoutSending(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	first := New(server.URL)
	_, err := first.Do(context.Background(), &Request{Path: "/"})
	require.True(t, exchange.IsKind(err, exchange.ErrorRateLimited), "%v", err)
	require.True(t, exchange.NotAccepted(err))
	// 同一域名的另一个账户也不能再发请求。
	_, err = New(server.URL).Do(context.Background(), &Request{Path: "/"})
	require.True(t, exchange.IsKind(err, exchange.ErrorNotSent), "%v", err)
	require.EqualValues(t, 1, calls.Load())
	require.False(t, first.Status().PausedUntil.IsZero())
}

func TestDoBanPausesHost(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()
	client := New(server.URL)
	_, err := client.Do(context.Background(), &Request{Path: "/"})
	require.True(t, exchange.IsKind(err, exchange.ErrorBanned), "%v", err)
	_, err = client.Do(context.Background(), &Request{Path: "/", Order: true})
	require.True(t, exchange.IsKind(err, exchange.ErrorNotSent), "%v", err)
	require.EqualValues(t, 1, calls.Load())
	status := client.Status()
	require.Equal(t, exchange.ErrorBanned, status.PauseReason)
	require.True(t, status.PausedUntil.After(time.Now().Add(time.Minute)))
}

// 下单限流只停这个账户下单，查询照常。
func TestDoOrderRateLimitOnlyBlocksOrders(t *testing.T) {
	var orders, queries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			orders.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":-1015,"msg":"Too many new orders"}`))
			return
		}
		queries.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client := New(server.URL)
	client.Classify = func(_ int, raw []byte) exchange.ErrorKind {
		if strings.Contains(string(raw), "-1015") {
			return exchange.ErrorOrderRateLimited
		}
		return ""
	}
	_, err := client.Do(context.Background(), &Request{Method: http.MethodPost, Path: "/order", Order: true})
	require.True(t, exchange.IsKind(err, exchange.ErrorOrderRateLimited), "%v", err)
	_, err = client.Do(context.Background(), &Request{Method: http.MethodPost, Path: "/order", Order: true})
	require.True(t, exchange.IsKind(err, exchange.ErrorNotSent), "%v", err)
	_, err = client.Do(context.Background(), &Request{Path: "/order"})
	require.NoError(t, err)
	require.EqualValues(t, 1, orders.Load())
	require.EqualValues(t, 1, queries.Load())
	// 另一个账户的下单不受影响。
	other := New(server.URL)
	_, err = other.Do(context.Background(), &Request{Method: http.MethodPost, Path: "/order", Order: true})
	require.True(t, exchange.IsKind(err, exchange.ErrorRateLimited) || exchange.IsKind(err, exchange.ErrorOrderRateLimited), "%v", err)
}

func TestDoRequestTimeoutIsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer server.Close()
	_, err := New(server.URL).Do(context.Background(), &Request{Path: "/"})
	require.True(t, exchange.IsKind(err, exchange.ErrorTransportUnknown), "%v", err)
	require.False(t, exchange.NotAccepted(err))
}

func TestStatusTracksWeightWithinMinute(t *testing.T) {
	weights := []string{"120", "80", "300"}
	var index atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-MBX-USED-WEIGHT-1M", weights[index.Add(1)-1])
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client := New(server.URL)
	client.SetWeightLimit(2400)
	for range weights {
		_, err := client.Do(context.Background(), &Request{Path: "/"})
		require.NoError(t, err)
	}
	status := client.Status()
	if status.UsedWeight != 0 { // 跨分钟时读数作废
		require.Equal(t, 300, status.UsedWeight)
	}
	require.Equal(t, 2400, status.WeightLimit)
	host := hostOf(server.URL)
	used, _, _, _ := host.snapshot(time.Now().Add(2 * time.Minute))
	require.Zero(t, used)
}

func TestOnPauseReportsHostAndReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	var host string
	var reason exchange.ErrorKind
	OnPause = func(h string, r exchange.ErrorKind, _ time.Time) { host, reason = h, r }
	defer func() { OnPause = nil }()
	_, _ = New(server.URL).Do(context.Background(), &Request{Path: "/"})
	require.Equal(t, hostKey(server.URL), host)
	require.Equal(t, exchange.ErrorRateLimited, reason)
}
