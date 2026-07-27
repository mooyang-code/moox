package binance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicAggregateTradeAPIsUseCursorAndExpectedEndpoints(t *testing.T) {
	tests := []struct {
		name       string
		setBaseURL func(*Client, string) error
		call       func(*Client, *exchange.TradeRequest) ([]*exchange.Trade, error)
		path       string
	}{
		{name: "spot", setBaseURL: (*Client).SetSpotBaseURL, call: func(c *Client, req *exchange.TradeRequest) ([]*exchange.Trade, error) {
			return NewSpotAPI(c).GetRecentTrades(context.Background(), req)
		}, path: SpotTradesEndpoint},
		{name: "swap", setBaseURL: (*Client).SetSwapBaseURL, call: func(c *Client, req *exchange.TradeRequest) ([]*exchange.Trade, error) {
			return NewSwapAPI(c).GetRecentTrades(context.Background(), req)
		}, path: SwapTradesEndpoint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotFromID string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotFromID = r.URL.Query().Get("fromId")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[{"a":41,"p":"100.5","q":"0.2","T":1700000000000,"m":true}]`))
			}))
			defer server.Close()
			client := NewClient()
			client.HTTPClient = httpclient.NewHTTPClientWithClient(server.Client())
			require.NoError(t, tt.setBaseURL(client, server.URL))
			trades, err := tt.call(client, &exchange.TradeRequest{Symbol: "BTCUSDT", Limit: 100, FromID: 41})
			require.NoError(t, err)
			require.Len(t, trades, 1)
			assert.Equal(t, tt.path, gotPath)
			assert.Equal(t, "41", gotFromID)
			assert.Equal(t, int64(41), trades[0].ID)
		})
	}
}

func TestFormatSymbol_HyphenatedSymbol_ShouldRemoveSeparator(t *testing.T) {
	assert.Equal(t, "BTCUSDT", FormatSymbol("BTC-USDT"))
}

func TestParseSymbol_BinanceSymbol_ShouldReturnHyphenated(t *testing.T) {
	assert.Equal(t, "BTC-USDT", ParseSymbol("BTCUSDT", "USDT"))
	assert.Equal(t, "BTC-USDT", ParseSymbol("BTCUSDT", ""))
}

func TestClient_SetSpotBaseURL_ValidURL_ShouldUpdateDomain(t *testing.T) {
	c := NewClient()
	require.NoError(t, c.SetSpotBaseURL("https://testnet.binance.vision"))
	assert.Equal(t, "testnet.binance.vision", c.SpotDomain())
}

func TestClient_SetSwapBaseURL_InvalidURL_ShouldReturnError(t *testing.T) {
	c := NewClient()
	err := c.SetSwapBaseURL("://bad")
	assert.Error(t, err)
}

func TestDomainFromBaseURL_EmptyString_ShouldReturnEmpty(t *testing.T) {
	host, err := domainFromBaseURL("")
	require.NoError(t, err)
	assert.Equal(t, "", host)
}

func TestDomainFromBaseURL_HostOnly_ShouldParseHost(t *testing.T) {
	host, err := domainFromBaseURL("api.example.com")
	require.NoError(t, err)
	assert.Equal(t, "api.example.com", host)
}

func TestCandleStick_UnmarshalJSON_ArrayPayload_ShouldParseFields(t *testing.T) {
	raw := []byte(`[1499040000000,"0.01634790","0.80000000","0.01575800","0.01577100","148976.11427815",1499644799999,"2434.19055334",308,"1756.87402397","28.46694368","0"]`)
	var candle CandleStick
	require.NoError(t, json.Unmarshal(raw, &candle))
	assert.Equal(t, int64(1499040000000), candle.OpenTime)
	assert.Equal(t, "0.01634790", candle.Open)
	assert.Equal(t, int64(308), candle.TradeCount)
}

func TestCandleStick_UnmarshalJSON_InvalidPayload_ShouldReturnError(t *testing.T) {
	var candle CandleStick
	err := json.Unmarshal([]byte(`{"open":"1"}`), &candle)
	assert.Error(t, err)
}

func TestSymbolInfoRaw_ToSymbolInfo_ShouldMapTradingPair(t *testing.T) {
	raw := SymbolInfoRaw{
		Symbol:     "BTCUSDT",
		Status:     "TRADING",
		BaseAsset:  "BTC",
		QuoteAsset: "USDT",
		Filters: []FilterInfo{
			{FilterType: "LOT_SIZE", MinQty: "0.001", MaxQty: "1000", StepSize: "0.001"},
			{FilterType: "PRICE_FILTER", TickSize: "0.01"},
		},
	}
	info := raw.ToSymbolInfo()
	assert.Equal(t, "BTC-USDT", info.Symbol)
	assert.Equal(t, "active", info.Status)
	assert.Equal(t, "0.001", info.MinQty)
	assert.Equal(t, "0.01", info.TickSize)
}

func TestClient_DefaultDomains_ShouldUseBinanceHosts(t *testing.T) {
	c := NewClient()
	assert.Equal(t, SpotDomain, c.SpotDomain())
	assert.Equal(t, SwapDomain, c.SwapDomain())
}
