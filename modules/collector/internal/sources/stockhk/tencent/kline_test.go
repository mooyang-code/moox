package tencent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

// testdata/minute_hk00700_20261123.json 是 2026-11-23 15:53（香港时间）实际请求
// ifzq.gtimg.cn/appstock/app/hkMinute/query?code=hk00700 的原始返回：0930～1554 共 326 行，最后一行 1554 是正在走的这一分钟。

func hkTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, hongKong)
	require.NoError(t, err)
	return parsed.UTC()
}

func newKlineProvider(t *testing.T, body []byte, status int, now time.Time, requests *[]string) *KlineProvider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, minutePath, r.URL.Path)
		if requests != nil {
			*requests = append(*requests, r.URL.Query().Get("code"))
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return NewKline(KlineConfig{BaseURL: server.URL, Now: func() time.Time { return now }})
}

func klineRequest(subject, code string) marketdata.KlineRequest {
	return marketdata.KlineRequest{MarketID: "stockhk", ExchangeID: "XHKG", InstrumentType: marketdata.InstrumentEquity, SubjectID: subject, ProviderSymbol: code, Frequency: "5m", Limit: 2, RequestID: "req-1"}
}

func barsByStart(t *testing.T, rows []marketdata.NormalizedKline) map[string]marketdata.NormalizedKline {
	t.Helper()
	out := map[string]marketdata.NormalizedKline{}
	for _, row := range rows {
		out[row.BarStart.In(hongKong).Format("1504")] = row
	}
	return out
}

func TestKlineFromRecordedMinutes(t *testing.T) {
	var requests []string
	provider := newKlineProvider(t, fixture(t, "minute_hk00700_20261123.json"), http.StatusOK, hkTime(t, "2026-11-23 15:53"), &requests)
	rows, err := provider.FetchKlines(context.Background(), klineRequest("00700.XHKG", "00700"))
	require.NoError(t, err)
	require.Equal(t, []string{"hk00700"}, requests)
	// 上午 30 根；下午 13:00～15:45 共 34 根：15:50 那根要到 15:55 才收盘，正在走的 15:55 那根也不返回。
	require.Len(t, rows, 64)
	bars := barsByStart(t, rows)
	require.NotContains(t, bars, "1550")
	require.NotContains(t, bars, "1200")

	// 09:30 那根：开盘价是开盘那一行（含开盘竞价），成交量从当天零开始累计到 09:35。
	first := bars["0930"]
	require.Equal(t, hkTime(t, "2026-11-23 09:30"), first.BarStart)
	require.Equal(t, hkTime(t, "2026-11-23 09:35"), first.BarEnd)
	require.Equal(t, 335.0, first.Open)
	require.Equal(t, 337.6, first.Close)
	require.Equal(t, 3982504.0, first.Volume)
	require.InDelta(t, 1343015925.2, first.Amount, 1e-3)
	require.GreaterOrEqual(t, first.High, max(first.Open, first.Close))
	require.LessOrEqual(t, first.Low, min(first.Open, first.Close))
	require.Equal(t, "tencent", first.ProviderID)
	require.Equal(t, KlineSourceID, first.SourceID)
	require.Equal(t, "00700", first.ProviderSymbol)
	require.Equal(t, "00700.XHKG", first.SubjectID)
	require.False(t, first.AmountEstimated)

	// 10:00 那根：开盘价是 10:00 那一刻的价格（上一根最后一行），成交量是 10:00～10:05 的增量。
	ten := bars["1000"]
	require.Equal(t, 335.4, ten.Open)
	require.Equal(t, 336.0, ten.Close)
	require.Equal(t, 9178895.0-8524461.0, ten.Volume)

	// 11:55 那根含 12:00 那一行；13:00 那根从 13:00 那一行（与 12:00 相同的累计值）开始累计。
	require.Equal(t, 19037190.0-18813284.0, bars["1155"].Volume)
	afternoon := bars["1300"]
	require.Equal(t, 337.8, afternoon.Open)
	require.Equal(t, 19923945.0-19037190.0, afternoon.Volume)

	// 全天各根的成交量加起来等于最后一根收盘时刻（15:50）的累计值。
	total := 0.0
	for _, row := range rows {
		total += row.Volume
	}
	require.Equal(t, 32672630.0, total)
}

// 分时没有走到收盘时刻的根不返回：接口的数据比请求时刻落后时，不能把只含部分分钟的根当成收盘值。
func TestKlineSkipsBarsBeyondLatestMinute(t *testing.T) {
	provider := newKlineProvider(t, fixture(t, "minute_hk00700_20261123.json"), http.StatusOK, hkTime(t, "2026-11-23 16:30"), nil)
	rows, err := provider.FetchKlines(context.Background(), klineRequest("00700.XHKG", "00700"))
	require.NoError(t, err)
	bars := barsByStart(t, rows)
	require.Contains(t, bars, "1545")
	require.NotContains(t, bars, "1550", "最后一行是 15:54，15:50 那根还没走完")
}

func TestKlineSynthesizedAuctionAndLunch(t *testing.T) {
	body := []byte(`{"code":0,"msg":"","data":{"hk00005":{"data":{"date":"20261123","data":[
		"0930 100.000 1000 100000.0",
		"0931 101.000 1500 150500.0","0932 99.000 1600 160400.0","0933 100.500 1700 170450.0","0934 100.000 1800 180450.0","0935 100.200 2000 200490.0",
		"0936 100.300 2100 210520.0",
		"1156 102.000 5000 500000.0","1157 102.000 5000 500000.0","1158 102.000 5000 500000.0","1159 102.000 5000 500000.0","1200 103.000 5100 510300.0",
		"1300 103.000 5100 510300.0","1301 104.000 5200 520700.0","1302 104.000 5200 520700.0","1303 104.000 5200 520700.0","1304 104.000 5200 520700.0","1305 104.000 5200 520700.0",
		"1556 105.000 6000 600000.0","1557 105.000 6000 600000.0","1558 105.000 6000 600000.0","1559 105.000 6000 600000.0","1600 106.000 6100 610600.0",
		"1608 106.500 9100 930100.0"
	]}}}}`)
	provider := newKlineProvider(t, body, http.StatusOK, hkTime(t, "2026-11-23 16:20"), nil)
	rows, err := provider.FetchKlines(context.Background(), klineRequest("00005.XHKG", "00005"))
	require.NoError(t, err)
	bars := barsByStart(t, rows)

	first := bars["0930"]
	require.Equal(t, 100.0, first.Open)
	require.Equal(t, 101.0, first.High)
	require.Equal(t, 99.0, first.Low)
	require.Equal(t, 100.2, first.Close)
	require.Equal(t, 2000.0, first.Volume)

	// 09:35 那根只有 09:36 一行：开盘价是 09:35 那一刻的价格。
	require.Equal(t, 100.2, bars["0935"].Open)
	require.Equal(t, 100.0, bars["0935"].Volume)

	// 13:00 那一行不属于 11:55 那根，是 13:00 那根的开盘价。
	require.Equal(t, 5100.0-2100.0, bars["1155"].Volume)
	require.Equal(t, 103.0, bars["1155"].Close)
	require.Equal(t, 103.0, bars["1300"].Open)
	require.Equal(t, 100.0, bars["1300"].Volume)

	// 16:00 之后的行（收市竞价）并进 15:55 那根。
	last := bars["1555"]
	require.Equal(t, 106.5, last.Close)
	require.Equal(t, 106.5, last.High)
	require.Equal(t, 9100.0-5200.0, last.Volume)
}

func TestKlineErrors(t *testing.T) {
	now := hkTime(t, "2026-11-23 15:53")
	cases := map[string]struct {
		body   string
		status int
		want   error
	}{
		"上游报错":    {`{"code":-1,"msg":"code param error","data":[]}`, http.StatusOK, marketdata.ErrProtocol},
		"没有这只":    {`{"code":0,"msg":"","data":{"hk00001":{"data":{"date":"20261123","data":[]}}}}`, http.StatusOK, marketdata.ErrProtocol},
		"时刻倒退":    {`{"code":0,"data":{"hk00700":{"data":{"date":"20261123","data":["0931 1 10 10","0930 1 5 5"]}}}}`, http.StatusOK, marketdata.ErrProtocol},
		"累计值变小":   {`{"code":0,"data":{"hk00700":{"data":{"date":"20261123","data":["0930 1 10 10","0931 1 5 10"]}}}}`, http.StatusOK, marketdata.ErrProtocol},
		"价格为零":    {`{"code":0,"data":{"hk00700":{"data":{"date":"20261123","data":["0930 0 10 10"]}}}}`, http.StatusOK, marketdata.ErrProtocol},
		"日期不对":    {`{"code":0,"data":{"hk00700":{"data":{"date":"","data":["0930 1 10 10"]}}}}`, http.StatusOK, marketdata.ErrProtocol},
		"还没开盘":    {`{"code":0,"data":{"hk00700":{"data":{"date":"20261123","data":[]}}}}`, http.StatusOK, marketdata.ErrNoClosedBar},
		"限流":      {`{}`, http.StatusTooManyRequests, marketdata.ErrRateLimited},
		"服务端错误":   {`{}`, http.StatusBadGateway, marketdata.ErrHTTPStatus},
		"不是 JSON": {`<html>`, http.StatusOK, marketdata.ErrProtocol},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			provider := newKlineProvider(t, []byte(tc.body), tc.status, now, nil)
			_, err := provider.FetchKlines(context.Background(), klineRequest("00700.XHKG", "00700"))
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestKlineRejectsUnsupportedRequest(t *testing.T) {
	provider := NewKline(KlineConfig{BaseURL: "http://127.0.0.1:1"})
	req := klineRequest("00700.XHKG", "00700")
	req.Frequency = "1m"
	_, err := provider.FetchKlines(context.Background(), req)
	require.ErrorIs(t, err, marketdata.ErrUnsupportedFrequency)
	_, err = provider.FetchKlines(context.Background(), klineRequest("00700.XHKG", "0700"))
	require.ErrorIs(t, err, marketdata.ErrUnsupportedSymbol)

	spec := provider.KlineSpec()
	require.NoError(t, spec.Validate())
	require.Equal(t, []string{"5m"}, spec.Frequencies)
	require.False(t, spec.CompleteOHLCV)
	require.Equal(t, KlineSourceID, provider.Descriptor().SourceID)
}

// testdata/minute_hk00204_suspended_20261123.json、minute_hk00232_suspended_since_20261012.json 是 2026-11-23 16:3x（香港时间）
// 对两只停牌标的的实际返回：00204 当天给满 332 行、成交量全是 0；00232（长期停牌）返回的是最后交易日 10-12 的分时。
func TestKlineSuspendedSubjects(t *testing.T) {
	now := hkTime(t, "2026-11-23 16:30")
	req := klineRequest("00204.XHKG", "00204")
	req.StartTime, req.EndTime = hkTime(t, "2026-11-23 09:30"), hkTime(t, "2026-11-23 16:00")
	provider := newKlineProvider(t, fixture(t, "minute_hk00204_suspended_20261123.json"), http.StatusOK, now, nil)
	_, err := provider.FetchKlines(context.Background(), req)
	require.ErrorIs(t, err, marketdata.ErrNoClosedBar, "当天没有成交：与东方财富一样不出 K 线")

	req = klineRequest("00232.XHKG", "00232")
	req.StartTime, req.EndTime = hkTime(t, "2026-11-23 09:30"), hkTime(t, "2026-11-23 16:00")
	provider = newKlineProvider(t, fixture(t, "minute_hk00232_suspended_since_20261012.json"), http.StatusOK, now, nil)
	_, err = provider.FetchKlines(context.Background(), req)
	require.ErrorIs(t, err, marketdata.ErrHistoryCoverage, "返回的是别的交易日，证明不了请求的那天")
	require.NotErrorIs(t, err, marketdata.ErrNoClosedBar)
}

// 开盘前补上一交易日的缺口时，接口已经换成当天、还没有行：报覆盖不到，不能让补采当成行情源确认没有。
func TestKlineEarlierDayIsCoverageError(t *testing.T) {
	body := []byte(`{"code":0,"msg":"","data":{"hk00700":{"data":{"date":"20261124","data":[]}}}}`)
	provider := newKlineProvider(t, body, http.StatusOK, hkTime(t, "2026-11-24 09:05"), nil)
	req := klineRequest("00700.XHKG", "00700")
	req.StartTime, req.EndTime = hkTime(t, "2026-11-23 14:00"), hkTime(t, "2026-11-23 14:05")
	_, err := provider.FetchKlines(context.Background(), req)
	require.ErrorIs(t, err, marketdata.ErrHistoryCoverage)
}
