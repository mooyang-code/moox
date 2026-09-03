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
		ProviderID: "eastmoney", SourceID: "stock_hk_http", DisplayName: "EastMoney HK",
		MarketID: "stock_hk", InstrumentType: marketdata.InstrumentEquity,
		BaseURL: "http://fixture.test", Endpoint: "/kline", Host: "fixture.test",
		HTTPClient: client, Location: time.FixedZone("Asia/Hong_Kong", 8*60*60),
		SymbolFunc:  func(string) (string, error) { return "116.00005", nil },
		Frequencies: []string{"1m"}, MaxBarsPerRequest: 100,
		TimestampMode: marketdata.TimestampModeOpen, HasAmount: true,
		Now: func() time.Time { return time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC) },
	})

	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stock_hk", ExchangeID: "XHKG", SubjectID: "00005.XHKG",
		ProviderSymbol: "00005", Frequency: "1m", Limit: 1, RequestID: "request-1",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "eastmoney", rows[0].ProviderID)
	require.Equal(t, "stock_hk_http", rows[0].SourceID)
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
		ProviderID: "eastmoney", SourceID: "stock_hk_http", MarketID: "stock_hk",
		InstrumentType: marketdata.InstrumentEquity, Exchanges: []string{"XHKG"},
		BaseURL: "http://fixture.test", Endpoint: "/kline", Host: "fixture.test",
		HTTPClient: client, Location: time.FixedZone("Asia/Hong_Kong", 8*60*60),
		SymbolFunc:  func(string) (string, error) { return "116.00005", nil },
		Frequencies: []string{"1d"}, MaxBarsPerRequest: 100, HasAmount: true,
		Now: func() time.Time { return time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC) },
	})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stock_hk", ExchangeID: "XHKG", SubjectID: "00005.XHKG",
		ProviderSymbol: "00005", Frequency: "1d", Limit: 1, RequestID: "jsonp",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), rows[0].BarStart)
	require.Equal(t, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), rows[0].BarEnd)
}

func TestProviderRejectsUnsupportedMarketAndMissingAmount(t *testing.T) {
	provider := New(Config{
		ProviderID: "csindex", SourceID: "index_http", DisplayName: "CSIndex",
		MarketID: "stock_cn", InstrumentType: marketdata.InstrumentIndex,
		BaseURL: "http://fixture.test", Endpoint: "/kline", Host: "fixture.test",
		Location:    time.FixedZone("Asia/Shanghai", 8*60*60),
		SymbolFunc:  func(string) (string, error) { return "1.000001", nil },
		Frequencies: []string{"1d"}, MaxBarsPerRequest: 100,
		TimestampMode: marketdata.TimestampModeOpen, HasAmount: false,
	})
	_, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stock_hk", ExchangeID: "XHKG", SubjectID: "00005.XHKG",
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
				ProviderID: "eastmoney", SourceID: "stock_hk_http", DisplayName: "EastMoney HK",
				MarketID: "stock_hk", InstrumentType: marketdata.InstrumentEquity,
				Exchanges: []string{"XHKG"}, BaseURL: "http://fixture.test", Endpoint: "/kline",
				Host: "fixture.test", HTTPClient: client, Location: time.FixedZone("Asia/Hong_Kong", 8*60*60),
				SymbolFunc:  func(string) (string, error) { return "116.00005", nil },
				Frequencies: []string{"1m"}, MaxBarsPerRequest: 100, HasAmount: true,
			})
			_, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
				MarketID: "stock_hk", ExchangeID: "XHKG", SubjectID: "00005.XHKG",
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
