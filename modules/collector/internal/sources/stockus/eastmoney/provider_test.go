package eastmoney

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

// cst 把北京时间换成 UTC。
func cst(day, clock string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", day+" "+clock, time.FixedZone("CST", 8*3600))
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// fixtureProvider 返回一个不连网的数据源：请求交给 handler，时钟固定在 now。
func fixtureProvider(now time.Time, handler http.HandlerFunc) *Provider {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler(recorder, req)
		return recorder.Result(), nil
	})}
	return New(Config{HTTPClient: client, Now: func() time.Time { return now }})
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return body
}

// klinesBody 把若干行拼成接口的返回。
func klinesBody(lines ...string) []byte {
	return []byte(`{"data":{"klines":["` + strings.Join(lines, `","`) + `"]}}`)
}

// aaplRequest 是 AAPL 的 5m 请求：起止是 [StartTime, EndTime)，按 K 线的开始时刻，与流水线给的一致。
func aaplRequest(start, end, now time.Time, limit int) marketdata.KlineRequest {
	return marketdata.KlineRequest{
		MarketID: "stockus", ExchangeID: "XNAS", InstrumentType: marketdata.InstrumentEquity, SubjectID: "AAPL.XNAS", ProviderSymbol: "AAPL",
		Frequency: "5m", Limit: limit, StartTime: start, EndTime: end, Now: now, RequestID: "req-aapl",
	}
}

// inRequest 复现流水线的裁剪（kline_pipeline.go）：只留开始时刻在 [start, end) 内的根。
func inRequest(rows []marketdata.NormalizedKline, start, end time.Time) []marketdata.NormalizedKline {
	kept := make([]marketdata.NormalizedKline, 0, len(rows))
	for _, row := range rows {
		if !start.IsZero() && row.BarStart.Before(start) {
			continue
		}
		if !end.IsZero() && !row.BarStart.Before(end) {
			continue
		}
		kept = append(kept, row)
	}
	return kept
}

// 一整个冬令时交易日。testdata/kline_5m_AAPL_20261111_20261113.json 是 2026-11-12 23:06（北京时间）从南京的云函数请求
// push2his.eastmoney.com/api/qt/stock/kline/get?secid=105.AAPL&klt=5&fqt=0&beg=20261111&end=20261113 取到的 86 根
// （docs/计划/美股实测数据/out7.json 的“区间 20261111~20261113”）：纽约 11-11 的 78 根，加上 11-12 晚上开盘后的 8 根，
// 最后一根（23:10）还在进行中。请求的区间是纽约 11-11 整天，所以后面多带的 8 根在区间之外。
func TestFetchKlinesParsesRecordedWinterDay(t *testing.T) {
	body := readTestdata(t, "kline_5m_AAPL_20261111_20261113.json")
	now := cst("2026-11-12", "23:06:00")
	start, end := cst("2026-11-11", "22:30:00"), cst("2026-11-12", "05:00:00")
	provider := fixtureProvider(now, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "push2his.eastmoney.com", r.URL.Host)
		require.Equal(t, "/api/qt/stock/kline/get", r.URL.Path)
		query := r.URL.Query()
		require.Equal(t, "105.AAPL", query.Get("secid"))
		require.Equal(t, "5", query.Get("klt"))
		require.Equal(t, "0", query.Get("fqt"), "不复权")
		require.Equal(t, "20261111", query.Get("beg"), "首根标签 22:35 减 22:30 是 11-11 零点过后，beg 是 11-11")
		require.Equal(t, "20261113", query.Get("end"), "末根标签 11-12 05:00，end 是 11-13")
		require.Equal(t, "1205", query.Get("lmt"))
		require.Equal(t, "Mozilla/5.0", r.Header.Get("User-Agent"))
		require.Equal(t, "https://quote.eastmoney.com/", r.Header.Get("Referer"))
		_, _ = w.Write(body)
	})
	rows, err := provider.FetchKlines(context.Background(), aaplRequest(start, end, now, 100))
	require.NoError(t, err)
	require.Equal(t, 85, len(rows), "78 根加区间之后多带的 7 根；86 根里最后一根（23:10）还在进行中，不返回")
	for _, row := range rows {
		require.NoError(t, marketdata.ValidateNormalizedKline(row))
		require.Equal(t, "5m", row.Frequency)
		require.Equal(t, 5*time.Minute, row.BarEnd.Sub(row.BarStart))
		require.Equal(t, row.BarEnd, row.ProviderTimestamp)
		require.Equal(t, "eastmoney", row.ProviderID)
		require.Equal(t, "stockus_http", row.SourceID)
		require.Equal(t, "AAPL", row.ProviderSymbol)
	}

	// 区间内正好是一个交易日的 78 根：开始时刻从 14:30 UTC（纽约 09:30）起每 5 分钟一根，没有缺口。
	kept := inRequest(rows, start, end)
	require.Len(t, kept, 78)
	byStart := map[time.Time]marketdata.NormalizedKline{}
	for index, row := range kept {
		require.Equal(t, cst("2026-11-11", "22:30:00").Add(time.Duration(index)*5*time.Minute), row.BarStart)
		byStart[row.BarStart] = row
	}
	require.Equal(t, time.Date(2026, 11, 11, 21, 0, 0, 0, time.UTC), kept[77].BarEnd, "末根 16:00（纽约）收盘")
	// 区间之后多带的是 11-12 晚上的根，开始时刻不早于区间终点。
	for _, row := range rows[len(kept):] {
		require.False(t, row.BarStart.Before(end))
	}

	for _, tc := range []struct {
		label, start                           string
		open, high, low, close, volume, amount float64
	}{
		// 列顺序是 时间,开,收,高,低,成交量(股),成交额(美元)。当天首根的开盘价是真实开盘价。
		{"22:35 当天首根", "2026-11-11 22:30:00", 349.545, 353.35, 349.325, 352.06, 2331875, 816847880},
		{"22:40 普通一根", "2026-11-11 22:35:00", 352.06, 352.85, 351.18, 352.445, 656447, 230984226},
		{"05:00 冬令时末根", "2026-11-12 04:55:00", 355.405, 355.91, 355.28, 355.63, 10630410, 3765914850},
	} {
		day, clock, _ := strings.Cut(tc.start, " ")
		row, ok := byStart[cst(day, clock)]
		require.True(t, ok, tc.label)
		require.Equal(t, []float64{tc.open, tc.high, tc.low, tc.close, tc.volume, tc.amount},
			[]float64{row.Open, row.High, row.Low, row.Close, row.Volume, row.Amount}, tc.label+"：成交量单位已是股，不换算")
	}
	// 标签 22:40 北京时间 = 14:40 UTC，键是开始时刻 14:35 UTC。
	require.Equal(t, time.Date(2026, 11, 11, 14, 35, 0, 0, time.UTC), kept[1].BarStart)
	require.Equal(t, time.Date(2026, 11, 11, 14, 40, 0, 0, time.UTC), kept[1].BarEnd)
}

// 夏令时的一个交易日：首根标签 21:35、末根次日 04:00，开始时刻从 13:30 UTC 起。
// testdata/kline_5m_AAPL_20261029_dst.json 的 78 根是 2026-11-12 同一次全量返回（lmt=100000，1412 根）里纽约 10-29 的那一段。
// 真实请求的返回在前后还会多带别的根（见 date_range_requests.json 里的 39 组、下面两个用 out11 实测返回的测试），
// 这里只用这一段验证夏令时的键与区间换算。
func TestFetchKlinesDaylightSavingDay(t *testing.T) {
	body := readTestdata(t, "kline_5m_AAPL_20261029_dst.json")
	now := cst("2026-11-12", "12:00:00")
	start, end := cst("2026-10-29", "21:30:00"), cst("2026-10-30", "04:00:00")
	provider := fixtureProvider(now, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "20261028", r.URL.Query().Get("beg"), "首根标签 21:35 减 22:30 是前一天 23:05，beg 取 10-28：夏令时当天 21:35～22:25 的 11 根要这样才取得到")
		require.Equal(t, "20261031", r.URL.Query().Get("end"))
		_, _ = w.Write(body)
	})
	rows, err := provider.FetchKlines(context.Background(), aaplRequest(start, end, now, 100))
	require.NoError(t, err)
	require.Len(t, rows, 78)
	require.Equal(t, time.Date(2026, 10, 29, 13, 30, 0, 0, time.UTC), rows[0].BarStart, "21:35 标签的首根，纽约 09:30")
	require.Equal(t, 348.0, rows[0].Open, "当天首根的开盘价是真实开盘价")
	require.Equal(t, time.Date(2026, 10, 29, 19, 55, 0, 0, time.UTC), rows[77].BarStart, "04:00 标签的末根，纽约 15:55 开始")
	require.Equal(t, time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC), rows[77].BarEnd)
	for index, row := range rows {
		require.Equal(t, rows[0].BarStart.Add(time.Duration(index)*5*time.Minute), row.BarStart)
	}
}

// 夏令时周一的首根（标签 21:35）：beg 是周日 10-25。样例是 out11.json（2026-11-12 补测）的
// “105.AAPL beg=20261025&end=20261027”：78 根，首根 10-26 21:35、末根 10-27 04:00，与规则算出的 beg、end 完全相同。
// 实时请求时只有这一根已收盘（时钟在 21:36），其余都在 Now 之后，不返回。
func TestFetchKlinesDaylightSavingMondayFirstBar(t *testing.T) {
	body := readTestdata(t, "kline_5m_AAPL_20261025_20261027.json")
	now := cst("2026-10-26", "21:36:00")
	start, end := cst("2026-10-26", "21:30:00"), cst("2026-10-26", "21:35:00")
	provider := fixtureProvider(now, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "20261025", r.URL.Query().Get("beg"), "标签 21:35 减 22:30 是前一天 23:05：周一的 beg 是周日")
		require.Equal(t, "20261027", r.URL.Query().Get("end"))
		_, _ = w.Write(body)
	})
	rows, err := provider.FetchKlines(context.Background(), aaplRequest(start, end, now, 1))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, time.Date(2026, 10, 26, 13, 30, 0, 0, time.UTC), rows[0].BarStart, "21:35 标签，纽约 09:30 开盘第一根")
	require.Equal(t, []float64{343.9, 346.215, 342.11, 344.05, 2565332, 881328360}, []float64{rows[0].Open, rows[0].High, rows[0].Low, rows[0].Close, rows[0].Volume, rows[0].Amount})
}

// 夏令时一个窗口的前 11 根（10-29 的 21:35～22:25）：beg 取前一天 10-28 才取得到。样例是 out11.json 的
// “105.AAPL beg=20261028&end=20261031”：223 根，首根 10-28 22:30（10-28 自己的 21:35～22:25 不在里面）、末根 10-31 04:00，
// 里面有 10-29 整天的 78 根；beg=20261028&end=20261030 实测是 145 根，beg 换成 10-29 则少了那 11 根（out10 的 67 根）。
// 请求区间是 10-29 整天（首根标签 21:35、末根 10-30 04:00），区间之后多带的 10-30 的根不在区间内。
func TestFetchKlinesDaylightSavingFrontBarsAreFetched(t *testing.T) {
	body := readTestdata(t, "kline_5m_AAPL_20261028_20261031.json")
	now := cst("2026-11-12", "12:00:00")
	start, end := cst("2026-10-29", "21:30:00"), cst("2026-10-30", "04:00:00")
	provider := fixtureProvider(now, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "20261028", r.URL.Query().Get("beg"))
		require.Equal(t, "20261031", r.URL.Query().Get("end"))
		_, _ = w.Write(body)
	})
	rows, err := provider.FetchKlines(context.Background(), aaplRequest(start, end, now, 100))
	require.NoError(t, err)
	kept := inRequest(rows, start, end)
	require.Equal(t, 78, len(kept))
	require.Equal(t, time.Date(2026, 10, 29, 13, 30, 0, 0, time.UTC), kept[0].BarStart, "21:35 标签的前 11 根在返回里")
	require.Equal(t, time.Date(2026, 10, 29, 19, 55, 0, 0, time.UTC), kept[77].BarStart)
	require.Greater(t, len(rows), len(kept), "返回还多带了区间外的根")
}

// 实时请求只要目标那一根。样例是 2026-11-12 22:50:04 从南京云函数取到的最后两根
// （docs/计划/美股实测数据/poll_2250.json 里 AAPL 的记录）：22:50 已收盘定稿，22:55 刚开始，还在进行中。
func TestFetchKlinesRealtimeTargetBar(t *testing.T) {
	body := readTestdata(t, "kline_5m_AAPL_boundary_2250.json")
	now := cst("2026-11-12", "22:50:04")
	start, end := cst("2026-11-12", "22:45:00"), cst("2026-11-12", "22:50:00")
	provider := fixtureProvider(now, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "105.AAPL", r.URL.Query().Get("secid"))
		require.Equal(t, "20261112", r.URL.Query().Get("beg"), "标签 22:50 减 22:30 是 00:20，beg 是当天")
		require.Equal(t, "20261113", r.URL.Query().Get("end"), "end 取标签日期加一天，否则 beg、end 同一天返回空")
		_, _ = w.Write(body)
	})
	rows, err := provider.FetchKlines(context.Background(), aaplRequest(start, end, now, 1))
	require.NoError(t, err)
	require.Len(t, rows, 1, "进行中的 22:55 不返回")
	require.Equal(t, time.Date(2026, 11, 12, 14, 45, 0, 0, time.UTC), rows[0].BarStart)
	require.Equal(t, []float64{356.87, 357.48, 356.1, 357.26, 493951, 176273051}, []float64{rows[0].Open, rows[0].High, rows[0].Low, rows[0].Close, rows[0].Volume, rows[0].Amount})
}

// 不带起止时刻（取最近若干根）：不带 beg，end 取请求时刻（北京时间）的日期加一天。
func TestFetchKlinesLatestWithoutRange(t *testing.T) {
	body := readTestdata(t, "kline_5m_AAPL_boundary_2250.json")
	now := cst("2026-11-12", "22:50:04")
	provider := fixtureProvider(now, func(w http.ResponseWriter, r *http.Request) {
		require.False(t, r.URL.Query().Has("beg"))
		require.Equal(t, "20261113", r.URL.Query().Get("end"))
		require.Equal(t, "2", r.URL.Query().Get("lmt"))
		_, _ = w.Write(body)
	})
	rows, err := provider.FetchKlines(context.Background(), aaplRequest(time.Time{}, time.Time{}, now, 2))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, time.Date(2026, 11, 12, 14, 45, 0, 0, time.UTC), rows[0].BarStart)
}

// 区间外的根（东方财富多带回的前一个、后一个交易日的根）不参与校验：它们不会被写入，一根不合法也不该让整次请求失败。
// 区间内的根不合法仍然整次失败（与 A 股、港股一致）。
// 样例的 22:55 那一根是构造的：收盘价 360 超出最高价 357.273（收盘竞价参考价曾在港股造成这类根）。
func TestFetchKlinesInvalidBarOutsideTheRequestedRangeIsTolerated(t *testing.T) {
	good := "2026-11-12 22:50,356.870,357.260,357.480,356.100,493951,176273051.000"
	bad := "2026-11-12 22:55,357.260,360.000,357.273,357.210,4996,1784870.000"
	serve := func(now time.Time) *Provider {
		return fixtureProvider(now, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(klinesBody(good, bad)) })
	}

	// 目标是 22:50 那一根，22:55 在区间之外（而且已经收盘）：整次请求成功。
	now := cst("2026-11-12", "22:55:10")
	rows, err := serve(now).FetchKlines(context.Background(), aaplRequest(cst("2026-11-12", "22:45:00"), cst("2026-11-12", "22:50:00"), now, 1))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, time.Date(2026, 11, 12, 14, 45, 0, 0, time.UTC), rows[0].BarStart)

	// 区间覆盖 22:55 时，这一根不合法，整次请求失败。
	rows, err = serve(now).FetchKlines(context.Background(), aaplRequest(cst("2026-11-12", "22:45:00"), cst("2026-11-12", "22:55:00"), now, 2))
	require.ErrorContains(t, err, "high must be >= open/close/low")
	require.Nil(t, rows)

	// 夏令时窗口多带第二天最前面的根（这里是 10-30 21:35，构造成不合法）时同理。
	nowNextDay := cst("2026-10-30", "23:00:00")
	provider := fixtureProvider(nowNextDay, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(klinesBody(
			"2026-10-30 04:00,348.920,348.910,349.060,348.300,16479171,5749156525.000",
			"2026-10-30 21:35,349.000,351.000,350.000,348.000,100000,34900000.000"))
	})
	rows, err = provider.FetchKlines(context.Background(), aaplRequest(cst("2026-10-30", "03:55:00"), cst("2026-10-30", "04:00:00"), nowNextDay, 1))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, time.Date(2026, 10, 29, 19, 55, 0, 0, time.UTC), rows[0].BarStart)
}

// 半日市（2026-11-27 感恩节次日，13:00 收市）没有真实样例（计划 U0 第 3 项要等当天实测），用构造的 42 根：
// 标签 11-27 22:35～11-28 02:00，收盘 13:00（纽约，冬令时）= 02:00（北京）。
func TestFetchKlinesHalfDay(t *testing.T) {
	lines := make([]string, 0, 42)
	for label := cst("2026-11-27", "22:35:00"); !label.After(cst("2026-11-28", "02:00:00")); label = label.Add(5 * time.Minute) {
		lines = append(lines, fmt.Sprintf("%s,300.000,300.200,300.400,299.800,%d,30000000.000", label.In(beijing).Format("2006-01-02 15:04"), 100000+len(lines)))
	}
	require.Len(t, lines, 42)
	now := cst("2026-11-28", "02:00:05")
	start, end := cst("2026-11-27", "22:30:00"), cst("2026-11-28", "02:00:00")
	provider := fixtureProvider(now, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "20261127", r.URL.Query().Get("beg"))
		require.Equal(t, "20261129", r.URL.Query().Get("end"))
		_, _ = w.Write(klinesBody(lines...))
	})
	rows, err := provider.FetchKlines(context.Background(), aaplRequest(start, end, now, 100))
	require.NoError(t, err)
	require.Len(t, rows, 42)
	require.Equal(t, cst("2026-11-27", "22:30:00"), rows[0].BarStart)
	require.Equal(t, cst("2026-11-28", "01:55:00"), rows[41].BarStart, "02:00 标签的那根是当天最后一根")
}

func TestSecIDMapsExchangeAndCode(t *testing.T) {
	for _, tc := range []struct {
		exchange, symbol, want string
	}{
		{"XNAS", "AAPL", "105.AAPL"},
		{"XNYS", "JPM", "106.JPM"},
		{"XASE", "SPY", "107.SPY"},
		{"XNYS", "BRK.B", "106.BRK_B"},
		{"XNYS", "BF.B", "106.BF_B"},
		{"xnys", "brk.b", "106.BRK_B"},
	} {
		got, err := secID(marketdata.KlineRequest{SubjectID: tc.symbol + "." + tc.exchange, ExchangeID: marketdata.ExchangeID(tc.exchange), ProviderSymbol: tc.symbol})
		require.NoError(t, err, tc.symbol)
		require.Equal(t, tc.want, got)
	}
	for _, exchange := range []string{"XSHG", "XHKG", "XCBO", ""} {
		_, err := secID(marketdata.KlineRequest{SubjectID: "AAPL." + exchange, ExchangeID: marketdata.ExchangeID(exchange), ProviderSymbol: "AAPL"})
		require.ErrorIs(t, err, marketdata.ErrUnsupportedSymbol, exchange)
	}
	_, err := secID(marketdata.KlineRequest{SubjectID: ".XNAS", ExchangeID: "XNAS", ProviderSymbol: " "})
	require.ErrorIs(t, err, marketdata.ErrUnsupportedSymbol)
}

// 带点的代码（BRK.B）：请求里的 secid 是 106.BRK_B，写成点返回空列表；归一化后的 ProviderSymbol 仍是 BRK.B。
func TestFetchKlinesDottedCode(t *testing.T) {
	now := cst("2026-11-12", "22:55:10")
	provider := fixtureProvider(now, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "106.BRK_B", r.URL.Query().Get("secid"))
		_, _ = w.Write(klinesBody("2026-11-12 22:50,500.000,500.400,500.600,499.800,30000,15000000.000"))
	})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stockus", ExchangeID: "XNYS", InstrumentType: marketdata.InstrumentEquity, SubjectID: "BRK.B.XNYS", ProviderSymbol: "BRK.B",
		Frequency: "5m", Limit: 1, StartTime: cst("2026-11-12", "22:45:00"), EndTime: cst("2026-11-12", "22:50:00"), Now: now, RequestID: "req-brk",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "BRK.B", rows[0].ProviderSymbol)
	require.Equal(t, "BRK.B.XNYS", rows[0].SubjectID)
}

func TestFetchKlinesRejectsOtherFrequenciesAndExchanges(t *testing.T) {
	now := cst("2026-11-12", "22:55:10")
	provider := fixtureProvider(now, func(http.ResponseWriter, *http.Request) { t.Fatal("不应发请求") })
	for _, frequency := range []string{"1m", "1d", "15m"} {
		_, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: "AAPL.XNAS", ExchangeID: "XNAS", ProviderSymbol: "AAPL", Frequency: frequency, Limit: 1, Now: now, RequestID: "req"})
		require.ErrorIs(t, err, marketdata.ErrUnsupportedFrequency, frequency)
	}
	for _, exchange := range []string{"XSHG", "XHKG", ""} {
		_, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: "AAPL." + exchange, ExchangeID: marketdata.ExchangeID(exchange), ProviderSymbol: "AAPL", Frequency: "5m", Limit: 1, Now: now, RequestID: "req"})
		require.ErrorIs(t, err, marketdata.ErrUnsupportedSymbol, exchange)
	}
}

func TestKlineSpecDeclaresOnlyFiveMinutes(t *testing.T) {
	provider := New(Config{})
	spec := provider.KlineSpec()
	require.Equal(t, []string{"stockus"}, spec.Markets)
	require.Equal(t, []string{"XNAS", "XNYS", "XASE"}, spec.Exchanges)
	require.Equal(t, []string{"5m"}, spec.Frequencies)
	require.Equal(t, marketdata.TimestampModeClose, spec.TimestampMode)
	require.True(t, spec.CompleteOHLCV)
	require.True(t, spec.HasAmount)
	require.Equal(t, 1205, spec.MaxBarsPerRequest)
	require.Equal(t, 21*24*time.Hour, spec.HistoryFor("5m").MaxLookback, "实测 18 个交易日，按自然时长声明 21 天")
	require.False(t, spec.HistoryFor("5m").SupportsArbitraryRange)
	require.NoError(t, spec.Validate())
	for _, exchange := range []string{"XNAS", "XNYS", "XASE"} {
		require.True(t, spec.SupportsRequest(marketdata.KlineRequest{MarketID: "stockus", ExchangeID: marketdata.ExchangeID(exchange), InstrumentType: marketdata.InstrumentEquity, Frequency: "5m"}), exchange)
	}
	require.False(t, spec.SupportsRequest(marketdata.KlineRequest{MarketID: "stockus", ExchangeID: "XNAS", Frequency: "1d"}))
	require.False(t, spec.SupportsRequest(marketdata.KlineRequest{MarketID: "stockus", ExchangeID: "XSHG", Frequency: "5m"}))
	require.False(t, spec.SupportsRequest(marketdata.KlineRequest{MarketID: "stockhk", ExchangeID: "XNAS", Frequency: "5m"}))

	descriptor := provider.Descriptor()
	require.Equal(t, "eastmoney/stockus_http", descriptor.SourceKey().String())
	require.Equal(t, []string{"push2his.eastmoney.com"}, descriptor.Hosts)

	// 数据源配置给的 source_id、限速、单次根数原样生效。
	limit := marketdata.RateLimitPolicy{RequestsPerSecond: 50, Burst: 20, MaxConcurrent: 16, Cooldown: time.Second, RequestTimeout: 5 * time.Second}
	configured := New(Config{SourceID: "Other_HTTP", RateLimit: limit, MaxBarsPerRequest: 300})
	require.Equal(t, "other_http", configured.Descriptor().SourceID)
	require.Equal(t, limit, configured.KlineSpec().RateLimit)
	require.Equal(t, 300, configured.KlineSpec().MaxBarsPerRequest)
}

// 只取 K 线：不能取全部标的、不能取指数成分，否则东方财富列表里的上万个美股代码会被登记成标的来源。
func TestProviderOnlyFetchesKlines(t *testing.T) {
	var provider marketdata.MarketProvider = New(Config{})
	_, isKline := provider.(marketdata.KlineFetcher)
	require.True(t, isKline)
	_, isInstrument := provider.(marketdata.InstrumentFetcher)
	require.False(t, isInstrument)
	_, isIndex := provider.(marketdata.IndexConstituentFetcher)
	require.False(t, isIndex)
	_, isField := provider.(marketdata.FieldSource)
	require.False(t, isField)
}
