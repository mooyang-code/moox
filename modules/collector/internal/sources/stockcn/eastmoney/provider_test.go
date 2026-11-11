package eastmoney

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

var shanghai = time.FixedZone("CST", 8*3600)

func cst(day, clock string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04", day+" "+clock, shanghai)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

// testdata/kline_5m_600519_20261104.json 是 2026-11-04 12:51（午休）实际请求
// push2his.eastmoney.com/api/qt/stock/kline/get?secid=1.600519&klt=5&fqt=0&end=20500101&lmt=60 的原始返回。
func TestFetchKlines5mParsesRecordedResponse(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "kline_5m_600519_20261104.json"))
	require.NoError(t, err)
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "push2his.eastmoney.com", r.URL.Host, "5m 只在 push2his 有数据")
		require.Equal(t, "/api/qt/stock/kline/get", r.URL.Path)
		require.Equal(t, "1.600519", r.URL.Query().Get("secid"))
		require.Equal(t, "5", r.URL.Query().Get("klt"))
		require.Equal(t, "0", r.URL.Query().Get("fqt"))
		require.False(t, r.URL.Query().Has("beg"), "实时请求不带 beg，否则上游忽略 lmt 返回整段历史")
		require.Equal(t, "60", r.URL.Query().Get("lmt"))
		_, _ = w.Write(body)
	}))
	now := cst("2026-11-04", "12:51")
	provider := New(Config{HTTPClient: client, Now: func() time.Time { return now }})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "5m", Limit: 60, Now: now, RequestID: "req-5m",
	})
	require.NoError(t, err)
	require.Len(t, rows, 60)
	// 标签是收盘时刻：首根 2026-11-03 10:35 是 10:30 开盘那根，末根 11:30 是 11:25 那根。
	require.Equal(t, cst("2026-11-03", "10:30"), rows[0].BarStart)
	require.Equal(t, cst("2026-11-04", "11:25"), rows[59].BarStart)
	require.Equal(t, cst("2026-11-04", "11:30"), rows[59].BarEnd)
	byStart := map[time.Time]marketdata.NormalizedKline{}
	for _, row := range rows {
		require.Equal(t, "5m", row.Frequency)
		require.Equal(t, 5*time.Minute, row.BarEnd.Sub(row.BarStart))
		byStart[row.BarStart] = row
	}
	_, lunch := byStart[cst("2026-11-03", "11:30")]
	require.False(t, lunch, "午休没有桶")
	require.Contains(t, byStart, cst("2026-11-03", "13:00"), "下午首根标签 13:05")
	require.Contains(t, byStart, cst("2026-11-03", "14:55"), "末根标签 15:00")
	// 首根含 09:25 开盘集合竞价；成交量单位是手，换成股：6735 手 = 673500 股。
	first := byStart[cst("2026-11-04", "09:30")]
	require.Equal(t, 1470.0, first.Open)
	require.Equal(t, 1480.6, first.High)
	require.Equal(t, 1458.0, first.Low)
	require.Equal(t, 1474.0, first.Close)
	require.Equal(t, 673500.0, first.VolumeShares)
	require.Equal(t, 991534727.0, first.AmountCNY)
}

// testdata/kline_1m_600519_20261104.json 是同一时刻 klt=1 的原始返回：1m 同样是收盘时刻标签，首根 09:31。
func TestFetchKlines1mUsesCloseLabels(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "kline_1m_600519_20261104.json"))
	require.NoError(t, err)
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "1", r.URL.Query().Get("klt"))
		require.Equal(t, "20261104", r.URL.Query().Get("beg"))
		require.Equal(t, "20261104", r.URL.Query().Get("end"))
		require.Equal(t, "1205", r.URL.Query().Get("lmt"), "历史请求取整页，由公共写入边界按区间过滤")
		_, _ = w.Write(body)
	}))
	now := cst("2026-11-04", "12:51")
	provider := New(Config{BaseURL: "http://fixture.test", KlineBaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "1m", Limit: 2,
		StartTime: cst("2026-11-04", "09:30"), EndTime: cst("2026-11-04", "09:32"), Now: now, RequestID: "req-1m",
	})
	require.NoError(t, err)
	require.Len(t, rows, 120)
	require.Equal(t, cst("2026-11-04", "09:30"), rows[0].BarStart)
	require.Equal(t, cst("2026-11-04", "11:29"), rows[119].BarStart)
	// 09:31 那根 3668 手 = 竞价 1215 手（分时 09:26）+ 连续竞价 2453 手（分时 09:31）。
	require.Equal(t, 366800.0, rows[0].VolumeShares)
}

func TestFetchKlinesRejectsUndeclaredFrequency(t *testing.T) {
	provider := New(Config{HTTPClient: newFixtureClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("不应发请求") }))})
	_, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "15m", Limit: 1, RequestID: "req-15m"})
	require.ErrorIs(t, err, marketdata.ErrUnsupportedFrequency)
}

func TestKlineSpecDeclaresHistoryPerFrequency(t *testing.T) {
	spec := New(Config{}).KlineSpec()
	require.Equal(t, []string{"1m", "5m", "1d"}, spec.Frequencies)
	require.Equal(t, marketdata.TimestampModeClose, spec.TimestampMode)
	require.Equal(t, 24*time.Hour, spec.HistoryFor("1m").MaxLookback)
	require.Equal(t, 30*24*time.Hour, spec.HistoryFor("5m").MaxLookback)
	require.True(t, spec.HistoryFor("1d").SupportsArbitraryRange, "日线按日期区间取，能取到上市首日")
	require.NoError(t, spec.HistoryFor("1d").ValidateStart(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)))
}

// testdata/kline_1d_600519_20251103_20251115.json 是 2026-11-11 实际请求
// push2his.eastmoney.com/api/qt/stock/kline/get?secid=1.600519&klt=101&fqt=0&beg=20251103&end=20251115&lmt=1205 的原始返回。
func TestFetchKlines1dParsesRecordedResponse(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "kline_1d_600519_20251103_20251115.json"))
	require.NoError(t, err)
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "push2his.eastmoney.com", r.URL.Host)
		require.Equal(t, "101", r.URL.Query().Get("klt"))
		require.Equal(t, "0", r.URL.Query().Get("fqt"), "不复权")
		require.Equal(t, "20251103", r.URL.Query().Get("beg"))
		require.Equal(t, "20251115", r.URL.Query().Get("end"))
		_, _ = w.Write(body)
	}))
	now := cst("2026-11-11", "18:00")
	provider := New(Config{HTTPClient: client, Now: func() time.Time { return now }})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "1d", Limit: 12, Now: now, RequestID: "req-1d",
		StartTime: time.Date(2025, 11, 3, 0, 0, 0, 0, time.UTC), EndTime: time.Date(2025, 11, 15, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Len(t, rows, 10, "区间内 10 个交易日，周末没有行")
	for _, row := range rows {
		require.Equal(t, "1d", row.Frequency)
		require.Equal(t, "stockcn_http", row.SourceID)
		require.NoError(t, marketdata.ValidateNormalizedKline(row))
	}
	// 日期标签 2025-11-03 就是交易日期：键是它的 UTC 零点，bar_end 是键加 24 小时。
	first := rows[0]
	require.Equal(t, time.Date(2025, 11, 3, 0, 0, 0, 0, time.UTC), first.BarStart)
	require.Equal(t, time.Date(2025, 11, 4, 0, 0, 0, 0, time.UTC), first.BarEnd)
	require.Equal(t, cst("2025-11-03", "15:00"), first.ProviderTimestamp)
	require.Equal(t, time.Date(2025, 11, 10, 0, 0, 0, 0, time.UTC), rows[5].BarStart, "11-07 周五之后是 11-10 周一")
	require.Equal(t, time.Date(2025, 11, 14, 0, 0, 0, 0, time.UTC), rows[9].BarStart)
	// 列顺序是 日期,开,收,高,低,成交量(手),成交额；成交量换成股：34548 手 = 3454800 股。
	require.Equal(t, 1431.0, first.Open)
	require.Equal(t, 1448.0, first.High)
	require.Equal(t, 1420.1, first.Low)
	require.Equal(t, 1435.0, first.Close)
	require.Equal(t, 3454800.0, first.VolumeShares)
	require.Equal(t, 4945310830.0, first.AmountCNY)
}

// 当天的日线在键加 24 小时之前不算收盘：不返回。
func TestFetchKlines1dDropsTheUnfinishedDay(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.False(t, r.URL.Query().Has("beg"))
		require.Equal(t, "2", r.URL.Query().Get("lmt"))
		_, _ = w.Write([]byte(`{"data":{"klines":["2026-11-10,1438.23,1439.00,1444.50,1431.26,23038,3316078849.00","2026-11-11,1445.00,1402.30,1445.62,1402.00,39848,5633865699.00"]}}`))
	}))
	now := cst("2026-11-11", "18:00")
	provider := New(Config{HTTPClient: client, Now: func() time.Time { return now }})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "1d", Limit: 2, Now: now, RequestID: "req-1d"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), rows[0].BarStart)

	bad := New(Config{HTTPClient: newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"klines":["2026-11-10 15:00,1,1,1,1,1,1"]}}`))
	})), Now: func() time.Time { return now }})
	_, err = bad.FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "1d", Limit: 2, Now: now, RequestID: "req-1d"})
	require.ErrorIs(t, err, marketdata.ErrProtocol, "日线的标签必须是日期")
}

func TestFetchInstrumentSnapshotPaginatesSuccessfullyAndCountsExchanges(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/qt/clist/get", r.URL.Path)
		switch r.URL.Query().Get("pn") {
		case "1":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"total": 3,
					"diff": [
						{"f12": "600000", "f13": 1, "f14": "Pudong Bank"},
						{"f12": "000001", "f13": 0, "f14": "Ping An Bank"}
					]
				}
			}`))
		case "2":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"total": 3,
					"diff": [
						{"f12": "920000", "f13": 2, "f14": "Beijing Stock"}
					]
				}
			}`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("pn"))
		}
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{
		MarketID:   "stockcn",
		SnapshotAt: now,
		RequestID:  "req-eastmoney-instrument",
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
		requests = append(requests, r.URL.Query().Get("pn"))
		switch r.URL.Query().Get("pn") {
		case "1":
			_, _ = w.Write([]byte(`{"data":{"pagecount":2,"total":2,"diff":[{"f12":"600000","f13":1,"f14":"Pudong Bank"}]}}`))
		case "2":
			http.Error(w, "truncated", http.StatusBadGateway)
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("pn"))
		}
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, RateLimit: marketdata.RateLimitPolicy{RequestsPerSecond: 100, Burst: 1, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: time.Second}})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "short-page"})

	require.ErrorIs(t, err, marketdata.ErrHTTPStatus)
	require.Equal(t, []string{"1", "2"}, requests)
}

func TestFetchInstrumentSnapshotRejectsDeclaredTotalMismatch(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("pn") {
		case "1":
			_, _ = w.Write([]byte(`{"data":{"pagecount":2,"total":3,"diff":[{"f12":"600000","f13":1,"f14":"Pudong Bank"}]}}`))
		case "2":
			_, _ = w.Write([]byte(`{"data":{"pagecount":2,"total":3,"diff":[{"f12":"000001","f13":0,"f14":"Ping An Bank"}]}}`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("pn"))
		}
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "declared-total"})

	require.ErrorContains(t, err, "does not match")
}

func TestFetchInstrumentSnapshotAcceptsObjectDiffWithDeclaredTotal(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"total":1,"diff":{"0":{"f12":"600000","f13":1,"f14":"Pudong Bank"}}}}`))
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "object-diff"})

	require.NoError(t, err)
	require.Len(t, snapshot.Instruments, 1)
}

func TestFetchInstrumentSnapshotRejectsShortFinalPageWithoutCount(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pn") == "1" {
			_, _ = w.Write([]byte(`{"data":{"diff":[{"f12":"600000","f13":1,"f14":"Pudong Bank"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"diff":[]}}`))
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "short-empty"})

	require.ErrorContains(t, err, "unverified short page")
}

func TestFetchInstrumentSnapshotFailsWhenLaterPageReturnsHTTPError(t *testing.T) {
	requests := make([]string, 0, 2)
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Query().Get("pn"))
		switch r.URL.Query().Get("pn") {
		case "1":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"total": 2,
					"diff": [
						{"f12": "600000", "f13": 1, "f14": "Pudong Bank"}
					]
				}
			}`))
		case "2":
			http.Error(w, "boom", http.StatusBadGateway)
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("pn"))
		}
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{
		MarketID:   "stockcn",
		SnapshotAt: now,
		RequestID:  "req-eastmoney-instrument",
	})
	require.ErrorIs(t, err, marketdata.ErrHTTPStatus)
	require.Equal(t, []string{"1", "2"}, requests)
}

func TestFetchInstrumentSnapshotFallsBackWhenPrimaryHostIsUnavailable(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		if req.URL.Host == "push2.eastmoney.com" {
			http.Error(recorder, "blocked", http.StatusBadGateway)
			return recorder.Result(), nil
		}
		require.Equal(t, "push2test.eastmoney.com", req.URL.Host)
		require.Equal(t, "/api/qt/clist/get", req.URL.Path)
		_, _ = recorder.WriteString(`{"data":{"total":1,"diff":[{"f12":"600000","f13":1,"f14":"Pudong Bank"}]}}`)
		return recorder.Result(), nil
	})}
	provider := New(Config{BaseURL: "https://push2.eastmoney.com", HTTPClient: client, Now: func() time.Time { return time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC) }})
	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "fallback"})
	require.NoError(t, err)
	require.True(t, snapshot.Complete)
	require.Equal(t, "600000.XSHG", snapshot.Instruments[0].SubjectID)
}

func TestFetchInstrumentSnapshotFallsBackWhenPrimaryTransportFails(t *testing.T) {
	var hosts []string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hosts = append(hosts, req.URL.Host)
		if req.URL.Host == "push2.eastmoney.com" {
			return nil, errors.New("connection reset")
		}
		require.Equal(t, "push2test.eastmoney.com", req.URL.Host)
		recorder := httptest.NewRecorder()
		_, _ = recorder.WriteString(`{"data":{"total":1,"diff":[{"f12":"600000","f13":1,"f14":"Pudong Bank"}]}}`)
		return recorder.Result(), nil
	})}
	provider := New(Config{BaseURL: "https://push2.eastmoney.com", HTTPClient: client, Now: time.Now})
	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "transport"})
	require.NoError(t, err)
	require.Equal(t, "600000.XSHG", snapshot.Instruments[0].SubjectID)
	require.Equal(t, []string{"push2.eastmoney.com", "push2test.eastmoney.com"}, hosts)
}

func TestFetchInstrumentSnapshotDoesNotSwitchHostAfterPrimaryPageSucceeds(t *testing.T) {
	var hosts []string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hosts = append(hosts, req.URL.Host)
		recorder := httptest.NewRecorder()
		if req.URL.Query().Get("pn") == "2" {
			http.Error(recorder, "boom", http.StatusBadGateway)
			return recorder.Result(), nil
		}
		_, _ = recorder.WriteString(`{"data":{"total":2,"diff":[{"f12":"600000","f13":1,"f14":"Pudong Bank"}]}}`)
		return recorder.Result(), nil
	})}
	provider := New(Config{BaseURL: "https://push2.eastmoney.com", HTTPClient: client, Now: time.Now})
	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "pinned"})
	require.ErrorIs(t, err, marketdata.ErrHTTPStatus)
	require.Equal(t, []string{"push2.eastmoney.com", "push2.eastmoney.com"}, hosts)
}

func TestFetchInstrumentSnapshotContinuesWhenCappedPageIsShortOfDeclaredTotal(t *testing.T) {
	var pages []string
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("pn"))
		switch r.URL.Query().Get("pn") {
		case "1":
			_, _ = w.Write([]byte(`{"data":{"total":3,"diff":[{"f12":"600000","f13":1,"f14":"Pudong Bank"}]}}`))
		case "2":
			_, _ = w.Write([]byte(`{"data":{"total":3,"diff":[{"f12":"000001","f13":0,"f14":"Ping An Bank"},{"f12":"920000","f13":2,"f14":"Beijing Stock"}]}}`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("pn"))
		}
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: time.Now})
	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "capped"})
	require.NoError(t, err)
	require.True(t, snapshot.Complete)
	require.Equal(t, []string{"1", "2"}, pages)
	require.Len(t, snapshot.Instruments, 3)
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

// testdata/hs300_bk0500_p{1,2,3}.json 是 2026-11-04 实际请求
// push2.eastmoney.com/api/qt/clist/get?pn=N&pz=100&fs=b:BK0500 的原始返回（total=300，每页 100 条）。
func hs300Pages(t *testing.T) map[string][]byte {
	pages := map[string][]byte{}
	for _, page := range []string{"1", "2", "3"} {
		body, err := os.ReadFile(filepath.Join("testdata", "hs300_bk0500_p"+page+".json"))
		require.NoError(t, err)
		pages[page] = body
	}
	return pages
}

func TestFetchIndexConstituentsReadsHS300Board(t *testing.T) {
	pages := hs300Pages(t)
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/qt/clist/get", r.URL.Path)
		require.Equal(t, "b:BK0500", r.URL.Query().Get("fs"))
		require.Equal(t, "100", r.URL.Query().Get("pz"))
		body, ok := pages[r.URL.Query().Get("pn")]
		require.True(t, ok, "翻到总数就停，不请求第 4 页")
		_, _ = w.Write(body)
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})
	snapshot, err := provider.FetchIndexConstituents(context.Background(), "000300")
	require.NoError(t, err)
	require.Len(t, snapshot.Instruments, 300)
	require.True(t, snapshot.Complete)
	subjects := map[string]bool{}
	for _, item := range snapshot.Instruments {
		subjects[item.SubjectID] = true
	}
	// 标的 ID 与 cn_a_share 同一个生成函数：沪市 XSHG、深市 XSHE，含科创板。
	require.True(t, subjects["600519.XSHG"])
	require.True(t, subjects["000001.XSHE"])
	require.True(t, subjects["688008.XSHG"])
}

func TestFetchIndexConstituentsRejectsIncompletePages(t *testing.T) {
	pages := hs300Pages(t)
	// 第 3 页只回一半：条数与 total 不一致，报错，不应用快照。
	var third map[string]any
	require.NoError(t, json.Unmarshal(pages["3"], &third))
	diff := third["data"].(map[string]any)["diff"].(map[string]any)
	for key := range diff {
		if len(diff) <= 50 {
			break
		}
		delete(diff, key)
	}
	truncated, err := json.Marshal(third)
	require.NoError(t, err)
	pages["3"] = truncated
	pages["4"] = []byte(`{"rc":102,"data":null}`)
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pages[r.URL.Query().Get("pn")])
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})
	_, err = provider.FetchIndexConstituents(context.Background(), "000300")
	require.Error(t, err)

	_, err = provider.FetchIndexConstituents(context.Background(), "399001")
	require.ErrorIs(t, err, marketdata.ErrInvalidRequest, "未登记的指数直接拒绝")
}
