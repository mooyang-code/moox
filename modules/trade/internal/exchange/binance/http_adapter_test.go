package binance

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func installBinanceHTTPStub(t *testing.T, handler func(*http.Request) string) {
	t.Helper()
	orig := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := handler(req)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = orig })
}

func TestAdapter_HTTPBackedAccountAndMetadataMethods_ShouldMapResponses(t *testing.T) {
	installBinanceHTTPStub(t, func(req *http.Request) string {
		switch req.URL.Path {
		case "/api/v3/account":
			return `{"balances":[{"asset":"USDT","free":"10","locked":"2"},{"asset":"BTC","free":"0","locked":"0"}]}`
		case "/fapi/v3/balance":
			return `[{"asset":"USDT","balance":"20","availableBalance":"17"},{"asset":"BTC","balance":"1","maxWithdrawAmount":"0.8"}]`
		case "/sapi/v1/asset/tradeFee":
			return `[{"symbol":"BTCUSDT","maker":"0.001","taker":"0.002"}]`
		case "/api/v3/exchangeInfo":
			return `{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.01"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","minNotional":"5"}]}]}`
		case "/api/v3/ticker/price":
			return `[{"symbol":"BTCUSDT","price":"100"}]`
		case "/fapi/v1/exchangeInfo":
			return `{"symbols":[{"symbol":"ETHUSDT","status":"TRADING","baseAsset":"ETH","quoteAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.01"},{"filterType":"MARKET_LOT_SIZE","stepSize":"0.01","minQty":"0.01"},{"filterType":"MIN_NOTIONAL","minNotional":"10"}]}]}`
		case "/fapi/v1/ticker/price":
			return `[{"symbol":"ETHUSDT","price":"2000"}]`
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
			return `{}`
		}
	})
	a := &Adapter{insCache: &instrumentCache{}}
	cred := exchange.Credential{APIKey: "ak", APISecret: "sk"}

	latency, err := a.Ping(context.Background(), cred)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, latency, int64(0))

	spotBalances, err := a.GetBalances(context.Background(), cred, exchange.MarketSpot, nil)
	require.NoError(t, err)
	require.Len(t, spotBalances, 1)
	assert.Equal(t, "12", spotBalances[0].Total)

	swapBalances, err := a.GetBalances(context.Background(), cred, exchange.MarketSwap, []string{"btc"})
	require.NoError(t, err)
	require.Len(t, swapBalances, 1)
	assert.Equal(t, "0.8", swapBalances[0].Available)

	info, err := a.GetAccountInfo(context.Background(), cred, exchange.MarketSpot)
	require.NoError(t, err)
	assert.Equal(t, "10", info.Available)

	fee, err := a.GetTradeFee(context.Background(), cred, exchange.MarketSpot, "btcusdt")
	require.NoError(t, err)
	assert.Equal(t, "BTCUSDT", fee.Symbol)

	instruments, err := a.GetInstruments(context.Background(), exchange.MarketSpot)
	require.NoError(t, err)
	require.Len(t, instruments, 1)
	assert.Equal(t, "100", instruments[0].LastPrice)

	swapInstruments, err := a.GetInstruments(context.Background(), exchange.MarketSwap)
	require.NoError(t, err)
	require.Len(t, swapInstruments, 1)
	assert.Equal(t, "ETHUSDT", swapInstruments[0].Symbol)
	assert.Equal(t, "2000", swapInstruments[0].LastPrice)
}

func TestAdapter_HTTPBackedOrderAndFundsMethods_ShouldMapResponses(t *testing.T) {
	installBinanceHTTPStub(t, func(req *http.Request) string {
		switch req.URL.Path {
		case "/api/v3/order":
			return `{"orderId":123,"clientOrderId":"client-1","symbol":"BTCUSDT","side":"BUY","type":"LIMIT","price":"100","origQty":"1","executedQty":"0.4","cummulativeQuoteQty":"40","status":"PARTIALLY_FILLED","time":11,"updateTime":12}`
		case "/api/v3/openOrders", "/api/v3/allOrders":
			return `[{"orderId":123,"clientOrderId":"client-1","symbol":"BTCUSDT","side":"BUY","type":"LIMIT","price":"100","origQty":"1","executedQty":"0.4","cummulativeQuoteQty":"40","status":"PARTIALLY_FILLED","time":11,"updateTime":12}]`
		case "/api/v3/myTrades":
			return `[{"id":7,"orderId":123,"symbol":"BTCUSDT","side":"BUY","price":"100","qty":"0.4","quoteQty":"40","commission":"0.01","commissionAsset":"USDT","maker":true,"time":99}]`
		case "/fapi/v3/positionRisk":
			return `[{"symbol":"BTCUSDT","positionAmt":"-2","positionSide":"BOTH","entryPrice":"100","leverage":"5","isolatedMargin":"20","unRealizedProfit":"3","liquidationPrice":"50","updateTime":77},{"symbol":"ETHUSDT","positionAmt":"0"}]`
		case "/sapi/v1/asset/transfer":
			return `{"tranId":456}`
		case "/sapi/v1/asset/dust-btc":
			return `{"details":[{"asset":"ABC","assetFullName":"ABC Token","amountFree":"1","toBTC":"0.1","toBNB":"0.2","toBNBOffExchange":"0.3","exchange":"0.01"}]}`
		case "/sapi/v1/asset/dust":
			return `{"totalServiceCharge":"0.01","totalTransfered":"0.2","transferResult":[{"amount":"1","fromAsset":"ABC","operateTime":88,"serviceChargeAmount":"0.01","tranId":789,"transferedAmount":"0.2"}]}`
		case "/fapi/v1/leverage":
			return `{"leverage":5}`
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
			return `{}`
		}
	})
	a := &Adapter{}
	cred := exchange.Credential{APIKey: "ak", APISecret: "sk"}

	placed, err := a.PlaceOrder(context.Background(), cred, &exchange.PlaceOrderReq{Market: exchange.MarketSpot, Symbol: "btcusdt", Side: exchange.SideBuy, Type: exchange.TypeLimit, Quantity: "1", Price: "100", ClientOrderID: "client-1"})
	require.NoError(t, err)
	assert.Equal(t, exchange.StatusPartiallyFilled, placed.Status)

	canceled, err := a.CancelOrder(context.Background(), cred, &exchange.CancelOrderReq{Market: exchange.MarketSpot, Symbol: "BTCUSDT", ClientOrderID: "client-1"})
	require.NoError(t, err)
	assert.Equal(t, "123", canceled.ExchangeOrderID)

	count, err := a.CancelAllOrders(context.Background(), cred, exchange.MarketSpot, "BTCUSDT")
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	amended, err := a.AmendOrder(context.Background(), cred, &exchange.AmendOrderReq{Market: exchange.MarketSpot, Symbol: "BTCUSDT", ClientOrderID: "client-1", NewPrice: "101", NewQuantity: "1"})
	require.NoError(t, err)
	assert.Equal(t, "client-1", amended.ClientOrderID)

	got, err := a.GetOrder(context.Background(), cred, &exchange.GetOrderReq{Market: exchange.MarketSpot, Symbol: "BTCUSDT", ClientOrderID: "client-1"})
	require.NoError(t, err)
	assert.Equal(t, exchange.StatusPartiallyFilled, got.Status)

	openOrders, err := a.ListOpenOrders(context.Background(), cred, &exchange.ListOrdersReq{Market: exchange.MarketSpot, Symbol: "BTCUSDT"})
	require.NoError(t, err)
	assert.Len(t, openOrders, 1)

	orders, err := a.ListOrders(context.Background(), cred, &exchange.ListOrdersReq{Market: exchange.MarketSpot, Symbol: "BTCUSDT"})
	require.NoError(t, err)
	assert.Len(t, orders, 1)

	trades, err := a.ListTrades(context.Background(), cred, &exchange.ListTradesReq{Market: exchange.MarketSpot, Symbol: "BTCUSDT"})
	require.NoError(t, err)
	require.Len(t, trades, 1)
	assert.Equal(t, "maker", trades[0].Role)

	positions, err := a.ListPositions(context.Background(), cred, exchange.MarketSwap, "BTCUSDT")
	require.NoError(t, err)
	require.Len(t, positions, 1)
	assert.Equal(t, "short", positions[0].PosSide)

	require.NoError(t, a.SetLeverage(context.Background(), cred, exchange.MarketSwap, "BTCUSDT", "5"))

	transfer, err := a.Transfer(context.Background(), cred, &exchange.TransferReq{Currency: "usdt", Amount: "1", From: exchange.MarketSpot, To: exchange.MarketSwap})
	require.NoError(t, err)
	assert.Equal(t, "456", transfer.TransferID)

	assets, err := a.ListConvertibleDustAssets(context.Background(), cred, nil)
	require.NoError(t, err)
	require.Len(t, assets, 1)
	assert.Equal(t, "ABC", assets[0].Asset)

	dust, err := a.ConvertDust(context.Background(), cred, &exchange.DustTransferReq{Assets: []string{"abc"}})
	require.NoError(t, err)
	require.Len(t, dust.Results, 1)
	assert.Equal(t, int64(789), dust.Results[0].TranID)
}
