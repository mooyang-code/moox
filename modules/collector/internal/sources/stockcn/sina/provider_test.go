package sina

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/eastmoney"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/tencent"
	"github.com/stretchr/testify/require"
)

func TestFetchKlinesParsesFixture(t *testing.T) {
	fixture := filepath.Join("testdata", "kline.json")
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/cn/api/jsonp_v2.php/var moox_kline=/CN_MarketDataService.getKLineData", r.URL.Path)
		body, err := os.ReadFile(fixture)
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID:      "000001.XSHE",
		ProviderSymbol: "sz000001",
		Frequency:      "1m",
		Limit:          2,
		RequestID:      "req-sina",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 300.0, rows[0].Volume)
	require.Equal(t, time.Date(2026, 8, 29, 1, 29, 0, 0, time.UTC), rows[0].BarStart)
	require.Equal(t, time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC), rows[0].BarEnd)
	require.Equal(t, marketdata.TimestampModeClose, provider.KlineSpec().TimestampMode)
}

func TestFetchKlinesRequestsOnlyTheBarsSinceStart(t *testing.T) {
	var requested []string
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Query().Get("datalen"))
		body, err := os.ReadFile(filepath.Join("testdata", "kline.json"))
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
			SubjectID: "000001.XSHE", ProviderSymbol: "sz000001", Frequency: "5m", Limit: 1,
			StartTime: start, EndTime: start.Add(5 * time.Minute), RequestID: "req-limit",
		})
		require.NoError(t, err)
	}
	// 10:00 起到最新已收盘的 10:00 那根是 1 根，多取 2 根；前一天 13:00 起是 31 根，多取 2 根。
	require.Equal(t, []string{"3", "33", "1023"}, requested)
}

func TestFetchKlinesClassifiesBodyReadErrorForFallback(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: readErrorBody{}, Header: make(http.Header), Request: req}, nil
	})}
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	_, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: "600000.XSHG", ProviderSymbol: "sh600000", Frequency: "1m", Limit: 2, RequestID: "body-error"})
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.True(t, marketdata.CanFallback(context.Background(), err), "truncated upstream bodies are retryable provider failures")
}

func TestFetchInstrumentSnapshotAcceptsEmptyTerminalPageAfterExactMultiple(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getHQNodeStockCount") {
			_, _ = w.Write([]byte(`100`))
			return
		}
		page := r.URL.Query().Get("page")
		if page == "2" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		items := make([]string, 100)
		for index := range items {
			items[index] = fmt.Sprintf(`{"symbol":"sh%06d","name":"stock"}`, 600000+index)
		}
		_, _ = fmt.Fprintf(w, "[%s]", strings.Join(items, ","))
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", SnapshotAt: now, RequestID: "req"})

	require.NoError(t, err)
	require.True(t, snapshot.Complete)
	require.Equal(t, 2, snapshot.PageCount)
	require.Len(t, snapshot.Instruments, 100)
}

func TestFetchInstrumentSnapshotRejectsHTTP200ErrorObjectAsTerminalPage(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"error":"temporary"}`))
			return
		}
		items := make([]string, 100)
		for index := range items {
			items[index] = fmt.Sprintf(`{"symbol":"sh%06d","name":"stock"}`, 600000+index)
		}
		_, _ = fmt.Fprintf(w, "[%s]", strings.Join(items, ","))
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "req"})

	require.ErrorContains(t, err, "no recognized item list")
}

func TestFetchInstrumentSnapshotRejectsChangingDeclaredPageCount(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCount := 3
		if r.URL.Query().Get("page") == "2" {
			pageCount = 2
		}
		_, _ = fmt.Fprintf(w, `{"data":{"pagecount":%d,"total":3,"list":[{"symbol":"sh60000%s","name":"stock"}]}}`, pageCount, r.URL.Query().Get("page"))
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "req"})

	require.ErrorContains(t, err, "page count changed")
}

func TestFetchInstrumentSnapshotResetsGuardTimeoutForEveryPage(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Millisecond)
		_, _ = fmt.Fprintf(w, `{"data":{"pagecount":2,"total":2,"list":[{"symbol":"sh60000%s","name":"stock"}]}}`, r.URL.Query().Get("page"))
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, InstrumentRequestTimeout: 20 * time.Millisecond})

	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "req"})

	require.NoError(t, err)
	require.Equal(t, 2, snapshot.PageCount)
}

func TestFetchInstrumentSnapshotPaginatesSuccessfullyAndCountsExchanges(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/quotes_service/api/json_v2.php/Market_Center.getHQNodeData", r.URL.Path)
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"total": 3,
					"list": [
						{"symbol": "sh600000", "name": "Pudong Bank"},
						{"symbol": "sz000001", "name": "Ping An Bank"}
					]
				}
			}`))
		case "2":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"total": 3,
					"list": [
						{"symbol": "bj920000", "name": "Beijing Stock"}
					]
				}
			}`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{
		MarketID:   "stockcn",
		SnapshotAt: now,
		RequestID:  "req-sina-instrument",
	})
	require.NoError(t, err)
	require.True(t, snapshot.Complete)
	require.Equal(t, 2, snapshot.PageCount)
	require.Equal(t, map[string]int{"XBSE": 1, "XSHG": 1, "XSHE": 1}, snapshot.ExchangeCounts)
	require.Len(t, snapshot.Instruments, 3)
	require.Equal(t, "sh600000", snapshot.Instruments[0].ProviderSymbol)
	require.Equal(t, "sz000001", snapshot.Instruments[1].ProviderSymbol)
	require.Equal(t, "bj920000", snapshot.Instruments[2].ProviderSymbol)
}

func TestFetchInstrumentSnapshotDoesNotTreatShortPageAsCompleteWithoutTerminalEvidence(t *testing.T) {
	requests := make([]string, 0, 2)
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Query().Get("page"))
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`[{"symbol":"sh600000","name":"Pudong Bank"}]`))
		case "2":
			http.Error(w, "truncated", http.StatusBadGateway)
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, RateLimit: marketdata.RateLimitPolicy{RequestsPerSecond: 100, Burst: 1, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: time.Second}})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "short-page"})

	require.ErrorIs(t, err, marketdata.ErrHTTPStatus)
	require.Equal(t, []string{"1", "2"}, requests)
}

func TestFetchInstrumentSnapshotRejectsDeclaredTotalMismatch(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{"data":{"pagecount":2,"total":3,"list":[{"symbol":"sh600000","name":"Pudong Bank"}]}}`))
		case "2":
			_, _ = w.Write([]byte(`{"data":{"pagecount":2,"total":3,"list":[{"symbol":"sz000001","name":"Ping An Bank"}]}}`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "declared-total"})

	require.ErrorContains(t, err, "does not match")
}

func TestFetchInstrumentSnapshotRejectsShortFinalPageWhenCountDisagrees(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getHQNodeStockCount") {
			_, _ = w.Write([]byte(`2`))
			return
		}
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(`[{"symbol":"sh600000","name":"Pudong Bank"}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "short-empty"})

	require.ErrorContains(t, err, "does not match")
}

func TestFetchInstrumentSnapshotFailsWhenLaterPageReturnsHTTPError(t *testing.T) {
	requests := make([]string, 0, 2)
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Query().Get("page"))
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"total": 2,
					"list": [
						{"symbol": "sh600000", "name": "Pudong Bank"}
					]
				}
			}`))
		case "2":
			http.Error(w, "boom", http.StatusBadGateway)
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{
		MarketID:   "stockcn",
		SnapshotAt: now,
		RequestID:  "req-sina-instrument",
	})
	require.ErrorIs(t, err, marketdata.ErrHTTPStatus)
	require.Equal(t, []string{"1", "2"}, requests)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

type readErrorBody struct{}

func (readErrorBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (readErrorBody) Close() error             { return nil }

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

func recordedKlines(t *testing.T, fetch func(*http.Client, time.Time) ([]marketdata.NormalizedKline, error), path string) []marketdata.NormalizedKline {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
	})}
	rows, err := fetch(client, cst("2026-11-04", "16:42"))
	require.NoError(t, err)
	return rows
}

// testdata/kline_5m_600519_20261104.txt 是 2026-11-04 16:42 实际请求
// quotes.sina.cn/cn/api/jsonp_v2.php/var%20moox_kline=/CN_MarketDataService.getKLineData?symbol=sh600519&scale=5&ma=no&datalen=60 的原始返回。
func TestFetchKlines5mParsesRecordedResponse(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "kline_5m_600519_20261104.txt"))
	require.NoError(t, err)
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "5", r.URL.Query().Get("scale"))
		require.Equal(t, "60", r.URL.Query().Get("datalen"))
		_, _ = w.Write(body)
	}))
	now := cst("2026-11-04", "16:42")
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "5m", Limit: 60, RequestID: "sina-5m"})
	require.NoError(t, err)
	require.Len(t, rows, 60)
	// 标签是收盘时刻：首根 14:05:00 是 14:00 那根；下午首根 13:05 是 13:00 那根。
	require.Equal(t, cst("2026-11-03", "14:00"), rows[0].BarStart)
	byStart := map[time.Time]marketdata.NormalizedKline{}
	for _, row := range rows {
		require.Equal(t, "5m", row.Frequency)
		require.False(t, row.AmountEstimated)
		byStart[row.BarStart] = row
	}
	require.Contains(t, byStart, cst("2026-11-04", "13:00"))
	_, lunch := byStart[cst("2026-11-04", "11:30")]
	require.False(t, lunch)
	// 首根含 09:25 集合竞价；成交量单位是股，有成交额。
	first := byStart[cst("2026-11-04", "09:30")]
	require.Equal(t, 674643.0, first.Volume)
	require.Equal(t, 993156836.1729, first.Amount)
	require.Equal(t, 1470.0, first.Open)
}

// 同一时段新浪、腾讯、东方财富 5m 时间逐根对齐，整段成交量合计一致；贴近 5 分钟边界的个别成交归属不同，单根量价可有小差异。
func TestRecorded5mAlignsWithTencentAndEastMoney(t *testing.T) {
	sinaRows := recordedKlines(t, func(client *http.Client, now time.Time) ([]marketdata.NormalizedKline, error) {
		return New(Config{HTTPClient: client, Now: func() time.Time { return now }}).FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "5m", Limit: 60, RequestID: "sina"})
	}, filepath.Join("testdata", "kline_5m_600519_20261104.txt"))
	tencentRows := recordedKlines(t, func(client *http.Client, now time.Time) ([]marketdata.NormalizedKline, error) {
		return tencent.New(tencent.Config{HTTPClient: client, Now: func() time.Time { return now }}).FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "5m", Limit: 60, RequestID: "tencent"})
	}, filepath.Join("..", "tencent", "testdata", "kline_5m_600519_20261104.txt"))
	// 东方财富样例在 12:51 录制，覆盖 2026-11-03 10:30～11-04 11:25。
	eastRows := recordedKlines(t, func(client *http.Client, now time.Time) ([]marketdata.NormalizedKline, error) {
		return eastmoney.New(eastmoney.Config{HTTPClient: client, Now: func() time.Time { return now }}).FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "5m", Limit: 60, RequestID: "east"})
	}, filepath.Join("..", "eastmoney", "testdata", "kline_5m_600519_20261104.json"))

	require.Len(t, tencentRows, len(sinaRows))
	for i := range sinaRows {
		require.Equal(t, sinaRows[i].BarStart, tencentRows[i].BarStart, "第 %d 根", i)
		require.Equal(t, sinaRows[i].BarEnd, tencentRows[i].BarEnd, "第 %d 根", i)
	}
	sinaByStart := map[time.Time]marketdata.NormalizedKline{}
	tencentByStart := map[time.Time]marketdata.NormalizedKline{}
	for i := range sinaRows {
		sinaByStart[sinaRows[i].BarStart], tencentByStart[tencentRows[i].BarStart] = sinaRows[i], tencentRows[i]
	}
	var eastVolume, sinaVolume, tencentVolume float64
	overlap := 0
	for _, east := range eastRows {
		sina, ok := sinaByStart[east.BarStart]
		if !ok {
			continue
		}
		overlap++
		eastVolume += east.Volume
		sinaVolume += sina.Volume
		tencentVolume += tencentByStart[east.BarStart].Volume
	}
	require.Equal(t, 36, overlap, "11-03 14:00～14:55 与 11-04 09:30～11:25")
	require.InEpsilon(t, eastVolume, sinaVolume, 2e-3)
	require.InEpsilon(t, eastVolume, tencentVolume, 2e-3)
}
