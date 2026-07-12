package binance

import (
	"context"
	"net/http"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdapter_GetSwapInstruments_ShouldLoadAndCache(t *testing.T) {
	installBinanceHTTPStub(t, func(req *http.Request) string {
		switch req.URL.Path {
		case "/fapi/v1/exchangeInfo":
			return `{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.01"},{"filterType":"MARKET_LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","minNotional":"5"}]}]}`
		case "/fapi/v1/ticker/price":
			return `[{"symbol":"BTCUSDT","price":"50000"}]`
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
			return `{}`
		}
	})
	a := &Adapter{}
	first, err := a.GetInstruments(context.Background(), exchange.MarketSwap)
	require.NoError(t, err)
	require.Len(t, first, 1)
	assert.Equal(t, "50000", first[0].LastPrice)
	assert.Equal(t, "0.001", first[0].LotSize)

	second, err := a.GetInstruments(context.Background(), exchange.MarketSwap)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, first[0].Symbol, second[0].Symbol)
}

func TestAdapter_Ping_SpotFailureFuturesSuccess_ShouldReturnLatency(t *testing.T) {
	installBinanceHTTPStub(t, func(req *http.Request) string {
		switch req.URL.Path {
		case "/api/v3/account":
			return `{"code":-2015,"msg":"invalid"}`
		case "/fapi/v3/balance":
			return `[{"asset":"USDT","balance":"1","availableBalance":"1"}]`
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
			return `{}`
		}
	})
	a := &Adapter{}
	latency, err := a.Ping(context.Background(), exchange.Credential{APIKey: "k", APISecret: "s"})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, latency, int64(0))
}

func TestAdapter_ClosePositionAndListFundFlows_ShouldReturnNotImplemented(t *testing.T) {
	a := &Adapter{}
	cred := exchange.Credential{APIKey: "k", APISecret: "s"}
	err := a.ClosePosition(context.Background(), cred, exchange.MarketSwap, "BTCUSDT", "long")
	require.Error(t, err)

	flows, err := a.ListFundFlows(context.Background(), cred, &exchange.FundFlowQuery{Currency: "USDT"})
	require.Error(t, err)
	assert.Nil(t, flows)
}
