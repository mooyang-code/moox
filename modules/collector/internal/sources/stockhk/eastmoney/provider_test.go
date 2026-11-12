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
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/stretchr/testify/require"
)

// hkt 把香港时间换成 UTC。
func hkt(day, clock string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", day+" "+clock, time.FixedZone("HKT", 8*3600))
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

// klinesBody 把若干行拼成接口的返回。
func klinesBody(lines ...string) []byte {
	return []byte(`{"data":{"klines":["` + strings.Join(lines, `","`) + `"]}}`)
}

// dayBuckets 返回 barclock 给出的港股某天全部 5m 桶的开始时刻。
func dayBuckets(t *testing.T, day string) []time.Time {
	t.Helper()
	clock, err := markets.Clock("stockhk", "5m")
	require.NoError(t, err)
	buckets, err := clock.Between(hkt(day, "00:00:00"), hkt(day, "23:59:59"), 1000)
	require.NoError(t, err)
	starts := make([]time.Time, 0, len(buckets))
	for _, bucket := range buckets {
		starts = append(starts, bucket.Key)
	}
	return starts
}

// testdata/kline_5m_00700_20261111.json 的 66 行是 2026-11-12 14:09（香港时间）从南京的云函数请求
// push2his.eastmoney.com/api/qt/stock/kline/get?secid=116.00700&klt=5&fqt=0&beg=20261111&end=20261111 取到的前一天整日 K 线
// （原始记录在 docs/计划/港股C0实测数据/请求方式与整日5m_20261112.json 的“昨日5m全量00700”，那里只留了 klines 数组，
// 外层的 {"data":{"klines":[…]}} 是按接口格式补的）。16:00 那根是收市竞价并入之后的最终值。
func TestFetchKlinesParsesRecordedDay(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "kline_5m_00700_20261111.json"))
	require.NoError(t, err)
	now := hkt("2026-11-11", "16:30:00")
	provider := fixtureProvider(now, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "push2his.eastmoney.com", r.URL.Host)
		require.Equal(t, "/api/qt/stock/kline/get", r.URL.Path)
		require.Equal(t, "116.00700", r.URL.Query().Get("secid"), "港股的市场编号是 116，代码保留前导零")
		require.Equal(t, "5", r.URL.Query().Get("klt"))
		require.Equal(t, "0", r.URL.Query().Get("fqt"), "不复权")
		require.Equal(t, "20261111", r.URL.Query().Get("beg"))
		require.Equal(t, "20261111", r.URL.Query().Get("end"))
		require.Equal(t, "1205", r.URL.Query().Get("lmt"))
		require.Equal(t, "Mozilla/5.0", r.Header.Get("User-Agent"))
		require.Equal(t, "https://quote.eastmoney.com/", r.Header.Get("Referer"))
		_, _ = w.Write(body)
	})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stockhk", ExchangeID: "XHKG", InstrumentType: marketdata.InstrumentEquity, SubjectID: "00700.XHKG", ProviderSymbol: "00700",
		Frequency: "5m", Limit: 100, StartTime: hkt("2026-11-11", "09:30:00"), EndTime: hkt("2026-11-11", "16:00:00"), Now: now, RequestID: "req-day",
	})
	require.NoError(t, err)
	require.Len(t, rows, 66)

	// 每根的键都落在 barclock（stockhk）的桶上，且一根不多一根不少：09:35 标签的那根键是 09:30。
	starts := make([]time.Time, 0, len(rows))
	byStart := map[time.Time]marketdata.NormalizedKline{}
	for _, row := range rows {
		require.NoError(t, marketdata.ValidateNormalizedKline(row))
		require.Equal(t, "5m", row.Frequency)
		require.Equal(t, 5*time.Minute, row.BarEnd.Sub(row.BarStart))
		require.Equal(t, row.BarEnd, row.ProviderTimestamp)
		require.Equal(t, "eastmoney", row.ProviderID)
		require.Equal(t, "stockhk_http", row.SourceID)
		require.Equal(t, "00700", row.ProviderSymbol)
		starts = append(starts, row.BarStart)
		byStart[row.BarStart] = row
	}
	require.Equal(t, dayBuckets(t, "2026-11-11"), starts)

	for _, tc := range []struct {
		label, start                           string
		open, high, low, close, volume, amount float64
	}{
		// 列顺序是 时间,开,收,高,低,成交量(股),成交额(港元)。首根的开盘价是开盘竞价的成交价。
		{"09:35 首根", "09:30:00", 414.2, 414.6, 410.2, 410.4, 1183400, 488223340},
		{"10:00 普通一根", "09:55:00", 410.4, 412.0, 410.2, 412.0, 245700, 100933580},
		{"12:00 上午最后一根", "11:55:00", 412.8, 413.0, 412.6, 412.8, 104900, 43307140},
		{"13:05 下午第一根", "13:00:00", 412.8, 413.4, 411.8, 412.4, 253100, 104379900},
		{"16:00 收市竞价并入后的最终值", "15:55:00", 407.8, 409.6, 407.6, 409.6, 2819000, 1153976836},
	} {
		row, ok := byStart[hkt("2026-11-11", tc.start)]
		require.True(t, ok, tc.label)
		require.Equal(t, []float64{tc.open, tc.high, tc.low, tc.close, tc.volume, tc.amount},
			[]float64{row.Open, row.High, row.Low, row.Close, row.Volume, row.Amount}, tc.label+"：成交量单位已是股，不换算")
	}
	_, lunch := byStart[hkt("2026-11-11", "12:00:00")]
	require.False(t, lunch, "午休没有桶")
}

// 只按时间选目标那根，不等下一根出现。冷门股 00836 在 14:50 这个边界后一分多钟没有成交，新一根（14:55）不出现，
// 刚收盘的 14:50 仍是返回里的最后一根。
//
// 两段返回内容都是实测的（docs/计划/港股C0实测数据/轮询/poll_1450.json）：第一段是 14:49:57 取到的最后两根（14:45、14:50），
// 第二段是 14:51:09 取到的（14:55 第一次出现，14:50 那根的内容与 14:49:57 时相同）。这两次之间实测没有取样，
// 所以“14:50:05”这个时刻是构造的：用 14:49:57 的返回内容，把时钟拨到边界后 5 秒。
func TestFetchKlinesReturnsTheJustClosedBarWhileItIsStillTheLastOne(t *testing.T) {
	request := marketdata.KlineRequest{
		MarketID: "stockhk", ExchangeID: "XHKG", InstrumentType: marketdata.InstrumentEquity, SubjectID: "00836.XHKG", ProviderSymbol: "00836",
		Frequency: "5m", Limit: 1, StartTime: hkt("2026-11-12", "14:45:00"), EndTime: hkt("2026-11-12", "14:50:00"), RequestID: "req-target",
	}
	check := func(now time.Time, lines ...string) {
		provider := fixtureProvider(now, func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "116.00836", r.URL.Query().Get("secid"))
			require.Equal(t, "20261112", r.URL.Query().Get("beg"), "只要一根也按日期取整天，不按时刻截取")
			require.Equal(t, "20261112", r.URL.Query().Get("end"))
			_, _ = w.Write(klinesBody(lines...))
		})
		request.Now = now
		rows, err := provider.FetchKlines(context.Background(), request)
		require.NoError(t, err)
		last := rows[len(rows)-1]
		require.Equal(t, hkt("2026-11-12", "14:45:00"), last.BarStart, "已收盘的最后一根就是目标那根")
		require.Equal(t, 20.42, last.Close)
		require.Equal(t, 24000.0, last.Volume)
	}
	// 边界后 5 秒（时刻是构造的，内容取自 14:49:57 的取样）：新一根还没出现。
	check(hkt("2026-11-12", "14:50:05"),
		"2026-11-12 14:45,20.420,20.460,20.460,20.400,212000,4330480.000",
		"2026-11-12 14:50,20.460,20.420,20.460,20.420,24000,490280.000")
	// 边界后 69 秒（实测的时刻与内容）：新一根出现了但没收盘，不返回，目标那根照常取到。
	check(hkt("2026-11-12", "14:51:09"),
		"2026-11-12 14:50,20.460,20.420,20.460,20.420,24000,490280.000",
		"2026-11-12 14:55,20.420,20.400,20.420,20.400,2000,40800.000")
}

// 收市竞价期间（16:00 之后几分钟），16:00 这根的收盘价被换成收市竞价的参考价，会超出这一根的最高价、最低价。
// 样例是 09988 在 2026-11-12 16:02:39 的实际返回（docs/计划/港股C0实测数据/轮询/poll_1600b00.json）：收盘价 96.3、最高价 93.75。
// 它过不了 marketdata 的开高低收校验；一根不合法，这只标的整次请求失败，同一次返回里合法的 15:55 那根也不返回。
func TestFetchKlinesFailsTheWholeRequestOnAnInvalidBar(t *testing.T) {
	now := hkt("2026-11-12", "16:02:40")
	provider := fixtureProvider(now, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(klinesBody(
			"2026-11-12 15:55,93.600,93.700,93.850,93.400,3871100,362398515.000",
			"2026-11-12 16:00,93.700,96.300,93.750,93.500,2848000,266640075.000"))
	})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stockhk", ExchangeID: "XHKG", InstrumentType: marketdata.InstrumentEquity, SubjectID: "09988.XHKG", ProviderSymbol: "09988",
		Frequency: "5m", Limit: 1, StartTime: hkt("2026-11-12", "15:55:00"), EndTime: hkt("2026-11-12", "16:00:00"), Now: now, RequestID: "req-auction",
	})
	require.ErrorContains(t, err, "high must be >= open/close/low")
	require.Nil(t, rows)

	// 对照：同一根在 16:00:02 的返回（连续交易时段的值，poll_1600a.json）是合法的，两根都返回。
	provider = fixtureProvider(hkt("2026-11-12", "16:00:05"), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(klinesBody(
			"2026-11-12 15:55,93.600,93.700,93.850,93.400,3871100,362398515.000",
			"2026-11-12 16:00,93.700,93.600,93.750,93.500,2848000,266640075.000"))
	})
	rows, err = provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID: "09988.XHKG", ProviderSymbol: "09988", Frequency: "5m", Limit: 1,
		StartTime: hkt("2026-11-12", "15:55:00"), EndTime: hkt("2026-11-12", "16:00:00"), Now: hkt("2026-11-12", "16:00:05"), RequestID: "req-close",
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, hkt("2026-11-12", "15:55:00"), rows[1].BarStart)
}

// 半日市（2026-12-24 圣诞前夕，12:00 收市）没有真实样例，用构造的 30 根：标签 09:35～12:00。
// 归一化后的键与 barclock 给出的当天 30 个桶逐个相同。
func TestFetchKlinesHalfDayBarsFallOnTheClock(t *testing.T) {
	lines := make([]string, 0, 30)
	for label := hkt("2026-12-24", "09:35:00"); !label.After(hkt("2026-12-24", "12:00:00")); label = label.Add(5 * time.Minute) {
		lines = append(lines, fmt.Sprintf("%s,400.000,400.200,400.400,399.800,%d,40000000.000", label.In(hongKong).Format("2006-01-02 15:04"), 100000+len(lines)))
	}
	require.Len(t, lines, 30)
	now := hkt("2026-12-24", "12:00:05")
	provider := fixtureProvider(now, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(klinesBody(lines...)) })
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID: "00700.XHKG", ProviderSymbol: "00700", Frequency: "5m", Limit: 100,
		StartTime: hkt("2026-12-24", "09:30:00"), EndTime: hkt("2026-12-24", "12:00:00"), Now: now, RequestID: "req-half-day",
	})
	require.NoError(t, err)
	starts := make([]time.Time, 0, len(rows))
	for _, row := range rows {
		starts = append(starts, row.BarStart)
	}
	buckets := dayBuckets(t, "2026-12-24")
	require.Len(t, buckets, 30)
	require.Equal(t, buckets, starts)
	require.Equal(t, hkt("2026-12-24", "11:55:00"), starts[29], "12:00 标签的那根是当天最后一根")
}

func TestKlineSpecDeclaresOnlyFiveMinutes(t *testing.T) {
	provider := New(Config{})
	spec := provider.KlineSpec()
	require.Equal(t, []string{"stockhk"}, spec.Markets)
	require.Equal(t, []string{"XHKG"}, spec.Exchanges)
	require.Equal(t, []string{"5m"}, spec.Frequencies)
	require.Equal(t, marketdata.TimestampModeClose, spec.TimestampMode)
	require.True(t, spec.CompleteOHLCV)
	require.True(t, spec.HasAmount)
	require.Equal(t, 1205, spec.MaxBarsPerRequest)
	require.Equal(t, 28*24*time.Hour, spec.HistoryFor("5m").MaxLookback, "实测约 23 个交易日，按自然时长声明 28 天")
	require.False(t, spec.HistoryFor("5m").SupportsArbitraryRange)
	require.NoError(t, spec.RateLimit.Validate())
	require.True(t, spec.SupportsRequest(marketdata.KlineRequest{MarketID: "stockhk", ExchangeID: "XHKG", InstrumentType: marketdata.InstrumentEquity, Frequency: "5m"}))
	require.False(t, spec.SupportsRequest(marketdata.KlineRequest{MarketID: "stockhk", ExchangeID: "XHKG", Frequency: "1d"}))
	require.False(t, spec.SupportsRequest(marketdata.KlineRequest{MarketID: "stockcn", ExchangeID: "XSHG", Frequency: "5m"}))

	descriptor := provider.Descriptor()
	require.Equal(t, "eastmoney/stockhk_http", descriptor.SourceKey().String())
	require.Equal(t, []string{"push2his.eastmoney.com"}, descriptor.Hosts)

	// 数据源配置给的 source_id、限速、单次根数原样生效。
	limit := marketdata.RateLimitPolicy{RequestsPerSecond: 50, Burst: 20, MaxConcurrent: 16, Cooldown: time.Second, RequestTimeout: 5 * time.Second}
	configured := New(Config{SourceID: "Other_HTTP", RateLimit: limit, MaxBarsPerRequest: 300})
	require.Equal(t, "other_http", configured.Descriptor().SourceID)
	require.Equal(t, limit, configured.KlineSpec().RateLimit)
	require.Equal(t, 300, configured.KlineSpec().MaxBarsPerRequest)
}

// 只取 K 线：不能取全部标的、不能取指数成分，否则东方财富的 A 股标的会被登记成港股的来源。
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

func TestFetchKlinesRejectsOtherFrequenciesAndSubjects(t *testing.T) {
	now := hkt("2026-11-11", "16:30:00")
	provider := fixtureProvider(now, func(http.ResponseWriter, *http.Request) { t.Fatal("不应发请求") })
	for _, frequency := range []string{"1m", "1d", "15m"} {
		_, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: "00700.XHKG", ProviderSymbol: "00700", Frequency: frequency, Limit: 1, Now: now, RequestID: "req"})
		require.ErrorIs(t, err, marketdata.ErrUnsupportedFrequency, frequency)
	}
	for _, subject := range []string{"600519.XSHG", "700.XHKG", "00700.XSHG", "00700"} {
		_, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{SubjectID: subject, ProviderSymbol: "00700", Frequency: "5m", Limit: 1, Now: now, RequestID: "req"})
		require.ErrorIs(t, err, marketdata.ErrUnsupportedSymbol, subject)
	}
}
