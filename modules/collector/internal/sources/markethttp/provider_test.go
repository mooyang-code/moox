package markethttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestProviderFetchesEastMoneyStyleRowsWithMarketSourceIdentity(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		_, _ = recorder.WriteString(`{"data":{"klines":["2026-09-01 09:30,10,10.1,10.2,9.8,12,121.2"]}}`)
		return recorder.Result(), nil
	})}
	provider := New(Config{
		ProviderID: "eastmoney", SourceID: "stockhk_http", DisplayName: "EastMoney HK",
		MarketID: "stockhk", InstrumentType: marketdata.InstrumentEquity,
		BaseURL: "http://fixture.test", Endpoint: "/kline", Host: "fixture.test",
		HTTPClient: client, Location: time.FixedZone("Asia/Hong_Kong", 8*60*60),
		SymbolFunc:  func(string) (string, error) { return "116.00005", nil },
		Frequencies: []string{"1m"}, MaxBarsPerRequest: 100,
		TimestampMode: marketdata.TimestampModeOpen, HasAmount: true,
		Now: func() time.Time { return time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC) },
	})

	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stockhk", ExchangeID: "XHKG", SubjectID: "00005.XHKG",
		ProviderSymbol: "00005", Frequency: "1m", Limit: 1, RequestID: "request-1",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "eastmoney", rows[0].ProviderID)
	require.Equal(t, "stockhk_http", rows[0].SourceID)
	require.Equal(t, "00005", rows[0].ProviderSymbol)
	require.Equal(t, time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC), rows[0].BarStart)
	require.Equal(t, float64(12), rows[0].VolumeShares)
	require.Equal(t, float64(121.2), rows[0].AmountCNY)
}

func TestProviderStripsJSONPAndKeepsCalendarDayInUTC(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		_, _ = recorder.WriteString(`callback({"data":{"klines":["2026-09-01,10,10.1,10.2,9.8,12,121.2"]}});`)
		return recorder.Result(), nil
	})}
	provider := New(Config{
		ProviderID: "eastmoney", SourceID: "stockhk_http", MarketID: "stockhk",
		InstrumentType: marketdata.InstrumentEquity, Exchanges: []string{"XHKG"},
		BaseURL: "http://fixture.test", Endpoint: "/kline", Host: "fixture.test",
		HTTPClient: client, Location: time.FixedZone("Asia/Hong_Kong", 8*60*60),
		SymbolFunc:  func(string) (string, error) { return "116.00005", nil },
		Frequencies: []string{"1d"}, MaxBarsPerRequest: 100, HasAmount: true,
		Now: func() time.Time { return time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC) },
	})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stockhk", ExchangeID: "XHKG", SubjectID: "00005.XHKG",
		ProviderSymbol: "00005", Frequency: "1d", Limit: 1, RequestID: "jsonp",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), rows[0].BarStart)
	require.Equal(t, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), rows[0].BarEnd)
}

func TestProviderKeysEastMoneyWeeklyRowsByISOMonday(t *testing.T) {
	var klt string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		klt = req.URL.Query().Get("klt")
		recorder := httptest.NewRecorder()
		// 2026-10-17 实测上证指数周线标签：该周最后一个交易日（周四、周三、周五、周五）。
		_, _ = recorder.WriteString(`{"data":{"klines":[` +
			`"2026-09-24,10,10.1,10.2,9.8,12,121.2",` +
			`"2026-09-30,10,10.1,10.2,9.8,12,121.2",` +
			`"2026-10-09,10,10.1,10.2,9.8,12,121.2",` +
			`"2026-10-16,10,10.1,10.2,9.8,12,121.2"]}}`)
		return recorder.Result(), nil
	})}
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	provider := New(Config{
		ProviderID: "eastmoney", SourceID: "index_http", MarketID: "stockcn",
		InstrumentType: marketdata.InstrumentIndex, Exchanges: []string{"XSHG"},
		BaseURL: "http://fixture.test", Endpoint: "/kline", Host: "fixture.test",
		HTTPClient: client, Location: shanghai,
		SymbolFunc:  func(string) (string, error) { return "1.000001", nil },
		Frequencies: []string{"1w"}, MaxBarsPerRequest: 100, HasAmount: true,
		// 周六上午：本周（10-12 那周）的 BarEnd 是 10-19，尚未收盘，应被跳过。
		Now: func() time.Time { return time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC) },
	})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stockcn", ExchangeID: "XSHG", SubjectID: "000001.XSHG",
		ProviderSymbol: "000001", Frequency: "1w", Limit: 10, RequestID: "weekly",
	})
	require.NoError(t, err)
	require.Equal(t, "102", klt)
	want := []time.Time{
		time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
	}
	labels := []time.Time{
		time.Date(2026, 9, 24, 0, 0, 0, 0, shanghai),
		time.Date(2026, 9, 30, 0, 0, 0, 0, shanghai),
		time.Date(2026, 10, 9, 0, 0, 0, 0, shanghai),
	}
	require.Len(t, rows, len(want))
	for i, row := range rows {
		require.Equal(t, want[i], row.BarStart)
		require.Equal(t, time.Monday, row.BarStart.Weekday())
		require.Equal(t, want[i].AddDate(0, 0, 7), row.BarEnd)
		// ProviderTimestamp 保留上游原始标签。
		require.True(t, labels[i].Equal(row.ProviderTimestamp), "row %d provider time %s", i, row.ProviderTimestamp)
	}
}

func TestParseBarTimeWeeklyKeyIsISOMonday(t *testing.T) {
	provider := New(Config{Location: time.FixedZone("Asia/Shanghai", 8*60*60)})
	cases := map[string]time.Time{
		"2026-10-12": time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), // 周一不变
		"2026-10-16": time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), // 周五
		"2026-10-18": time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), // 周日仍归本周
		"2026-12-31": time.Date(2026, 12, 28, 0, 0, 0, 0, time.UTC), // 跨年周（元旦休市，周四收尾）
		"2027-01-08": time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC),
	}
	for raw, want := range cases {
		got, _, err := provider.parseBarTime(raw, marketdata.FrequencyWeek)
		require.NoError(t, err)
		require.Equal(t, want, got, raw)
		require.True(t, got.Truncate(marketdata.FrequencyWeek.Duration()).Equal(got), raw)
	}
	// 日线、月线不受影响。
	day, _, err := provider.parseBarTime("2026-10-16", marketdata.FrequencyDay)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), day)
	month, _, err := provider.parseBarTime("2026-10-30", marketdata.FrequencyMonth)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), month)
}

func TestProviderRejectsUnsupportedMarketAndMissingAmount(t *testing.T) {
	provider := New(Config{
		ProviderID: "csindex", SourceID: "index_http", DisplayName: "CSIndex",
		MarketID: "stockcn", InstrumentType: marketdata.InstrumentIndex,
		BaseURL: "http://fixture.test", Endpoint: "/kline", Host: "fixture.test",
		Location:    time.FixedZone("Asia/Shanghai", 8*60*60),
		SymbolFunc:  func(string) (string, error) { return "1.000001", nil },
		Frequencies: []string{"1d"}, MaxBarsPerRequest: 100,
		TimestampMode: marketdata.TimestampModeOpen, HasAmount: false,
	})
	_, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stockhk", ExchangeID: "XHKG", SubjectID: "00005.XHKG",
		ProviderSymbol: "00005", Frequency: "1d", Limit: 1, RequestID: "request-2",
	})
	require.ErrorIs(t, err, marketdata.ErrProviderNotFound)
}

func TestProviderClassifiesHTTPAndPayloadFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       error
	}{
		{name: "rate limited", statusCode: http.StatusTooManyRequests, want: marketdata.ErrRateLimited},
		{name: "server error", statusCode: http.StatusBadGateway, want: marketdata.ErrHTTPStatus},
		{name: "empty rows", statusCode: http.StatusOK, body: `{"data":{"klines":[]}}`, want: marketdata.ErrNoClosedBar},
		{name: "invalid json", statusCode: http.StatusOK, body: "{", want: marketdata.ErrProtocol},
		{name: "invalid ohlc", statusCode: http.StatusOK, body: `{"data":{"klines":["2026-09-01 09:30,10,8,9,9,12,121"]}}`, want: marketdata.ErrProtocol},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				recorder := httptest.NewRecorder()
				recorder.Code = tt.statusCode
				if tt.body != "" {
					_, _ = recorder.WriteString(tt.body)
				}
				return recorder.Result(), nil
			})}
			provider := New(Config{
				ProviderID: "eastmoney", SourceID: "stockhk_http", DisplayName: "EastMoney HK",
				MarketID: "stockhk", InstrumentType: marketdata.InstrumentEquity,
				Exchanges: []string{"XHKG"}, BaseURL: "http://fixture.test", Endpoint: "/kline",
				Host: "fixture.test", HTTPClient: client, Location: time.FixedZone("Asia/Hong_Kong", 8*60*60),
				SymbolFunc:  func(string) (string, error) { return "116.00005", nil },
				Frequencies: []string{"1m"}, MaxBarsPerRequest: 100, HasAmount: true,
			})
			_, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
				MarketID: "stockhk", ExchangeID: "XHKG", SubjectID: "00005.XHKG",
				ProviderSymbol: "00005", Frequency: "1m", Limit: 1, RequestID: "failure",
			})
			require.ErrorIs(t, err, tt.want)
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
