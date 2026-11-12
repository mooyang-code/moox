package tencent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestFetchKlinesParsesFixture(t *testing.T) {
	fixture := filepath.Join("testdata", "kline.js")
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/appstock/app/kline/mkline", r.URL.Path)
		body, err := os.ReadFile(fixture)
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID:      "600000.XSHG",
		ProviderSymbol: "sh600000",
		Frequency:      "1m",
		Limit:          2,
		RequestID:      "req-tencent",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC), rows[0].BarStart)
	require.Equal(t, 200.0, rows[0].Volume)
	require.InDelta(t, 2040.0, rows[0].Amount, 0.000001)
	require.True(t, rows[0].AmountEstimated)
}

func TestFetchKlinesRequestsOnlyTheBarsSinceStart(t *testing.T) {
	var requested []string
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Query().Get("param"))
		body, err := os.ReadFile(filepath.Join("testdata", "kline_5m_600519_20261104.txt"))
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))
	now := time.Date(2026, 11, 4, 2, 7, 30, 0, time.UTC) // 10:07:30 Asia/Shanghai
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	for _, start := range []time.Time{
		time.Date(2026, 11, 4, 2, 0, 0, 0, time.UTC), // 实时周期提交：目标 10:00 那根
		time.Date(2026, 11, 3, 5, 0, 0, 0, time.UTC), // 补采前一天下午起
		time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC), // 两个月前：以单页上限封顶
	} {
		_, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
			SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "5m", Limit: 1,
			StartTime: start, EndTime: start.Add(5 * time.Minute), RequestID: "req-limit",
		})
		require.NoError(t, err)
	}
	// 10:00 起到最新已收盘的 10:00 那根是 1 根，多取 2 根；前一天 13:00 起是 31 根，多取 2 根。
	require.Equal(t, []string{"sh600519,m5,,3", "sh600519,m5,,33", "sh600519,m5,,320"}, requested)
}

func TestKlineSpecDoesNotAdvertiseUnverifiedBSE(t *testing.T) {
	spec := (&Provider{}).KlineSpec()
	require.Equal(t, []string{"XSHG", "XSHE"}, spec.Exchanges)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newFixtureClient(t *testing.T, handler http.Handler) *http.Client {
	t.Helper()
	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			return recorder.Result(), nil
		}),
	}
}

var shanghai = time.FixedZone("CST", 8*3600)

func cst(day, clock string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04", day+" "+clock, shanghai)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

// testdata/kline_5m_600519_20261104.txt 是 2026-11-04 16:42 实际请求
// ifzq.gtimg.cn/appstock/app/kline/mkline?_var=m5_today&param=sh600519,m5,,60 的原始返回。
func TestFetchKlines5mParsesRecordedResponse(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "kline_5m_600519_20261104.txt"))
	require.NoError(t, err)
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "sh600519,m5,,60", r.URL.Query().Get("param"))
		_, _ = w.Write(body)
	}))
	now := cst("2026-11-04", "16:42")
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "5m", Limit: 60, RequestID: "tx-5m"})
	require.NoError(t, err)
	require.Len(t, rows, 60)
	// 标签是收盘时刻：首根 202611031405 是 14:00 那根，末根 202611041500 是 14:55 那根。
	require.Equal(t, cst("2026-11-03", "14:00"), rows[0].BarStart)
	require.Equal(t, cst("2026-11-04", "14:55"), rows[59].BarStart)
	var first marketdata.NormalizedKline
	for _, row := range rows {
		require.Equal(t, "5m", row.Frequency)
		require.True(t, row.AmountEstimated)
		require.InDelta(t, row.Close*row.Volume, row.Amount, 1e-6, "成交额按 收盘价 × 成交量 估算")
		if row.BarStart.Equal(cst("2026-11-04", "09:30")) {
			first = row
		}
	}
	// 首根含 09:25 集合竞价；成交量单位是手：6735 手 = 673500 股，与东方财富同一根一致。
	require.Equal(t, 673500.0, first.Volume)
	require.Equal(t, 1470.0, first.Open)
	require.Equal(t, 1474.0, first.Close)
}
