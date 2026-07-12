package okx

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

func installOKXHTTPStub(t *testing.T, handler func(*http.Request) string) {
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

func okxData(data string) string {
	return `{"code":"0","msg":"","data":` + data + `}`
}

func TestAdapter_HTTPBackedMetadataAndAccountMethods_ShouldMapResponses(t *testing.T) {
	installOKXHTTPStub(t, func(req *http.Request) string {
		switch req.URL.Path {
		case "/api/v5/public/instruments":
			return okxData(`[{"instId":"BTC-USDT","instType":"SPOT","baseCcy":"BTC","quoteCcy":"USDT","tickSz":"0.01","lotSz":"0.001","minSz":"0.001","state":"live"}]`)
		case "/api/v5/market/tickers":
			return okxData(`[{"instId":"BTC-USDT","last":"100"}]`)
		case "/api/v5/account/balance":
			return okxData(`[{"totalEq":"12","data":[{"ccy":"USDT","availBal":"10","frozenBal":"2","eq":"12"},{"ccy":"BTC","availBal":"","frozenBal":"1","eq":""}]}]`)
		case "/api/v5/account/trade-fee":
			return okxData(`[{"maker":"0.001","taker":"0.002"}]`)
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
			return okxData(`[]`)
		}
	})
	a := &Adapter{}
	cred := exchange.Credential{APIKey: "ak", APISecret: "sk", Passphrase: "pass"}

	latency, err := a.Ping(context.Background(), cred)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, latency, int64(0))

	instruments, err := a.GetInstruments(context.Background(), exchange.MarketSpot)
	require.NoError(t, err)
	require.Len(t, instruments, 1)
	assert.Equal(t, "100", instruments[0].LastPrice)

	balances, err := a.GetBalances(context.Background(), cred, exchange.MarketSpot, []string{"usdt"})
	require.NoError(t, err)
	require.Len(t, balances, 1)
	assert.Equal(t, "12", balances[0].Total)

	info, err := a.GetAccountInfo(context.Background(), cred, exchange.MarketSpot)
	require.NoError(t, err)
	assert.Equal(t, "12", info.TotalEq)

	fee, err := a.GetTradeFee(context.Background(), cred, exchange.MarketSpot, "BTC-USDT")
	require.NoError(t, err)
	assert.Equal(t, "0.001", fee.Maker)
}

func TestAdapter_HTTPBackedOrderAndFundsMethods_ShouldMapResponses(t *testing.T) {
	installOKXHTTPStub(t, func(req *http.Request) string {
		switch req.URL.Path {
		case "/api/v5/trade/order":
			if req.Method == http.MethodPost {
				return okxData(`[{"ordId":"ex-1","clOrdId":"client-1","sCode":"0","state":"filled"}]`)
			}
			return okxData(`[{"ordId":"ex-1","clOrdId":"client-1","instId":"BTC-USDT","side":"buy","ordType":"limit","px":"100","sz":"1","fillSz":"1","fillPx":"100","avgPx":"100","state":"filled","fee":"0.1","feeCcy":"USDT","cTime":"1","uTime":"2"}]`)
		case "/api/v5/trade/cancel-order":
			return okxData(`[{"ordId":"ex-1","clOrdId":"client-1","sCode":"0","state":"canceled"}]`)
		case "/api/v5/trade/amend-order":
			return okxData(`[{"ordId":"ex-1","clOrdId":"client-1","sCode":"0","state":"live"}]`)
		case "/api/v5/account/set-leverage", "/api/v5/trade/close-position":
			return okxData(`[]`)
		case "/api/v5/trade/orders-pending", "/api/v5/trade/orders-history":
			return okxData(`[{"ordId":"ex-1","clOrdId":"client-1","instId":"BTC-USDT","side":"buy","ordType":"limit","px":"100","sz":"1","fillSz":"0","state":"live"}]`)
		case "/api/v5/trade/fills":
			return okxData(`[{"tradeId":"t1","ordId":"ex-1","instId":"BTC-USDT","side":"buy","fillPx":"100","fillSz":"1","fee":"0.1","feeCcy":"USDT","ts":"123"}]`)
		case "/api/v5/account/positions":
			return okxData(`[{"instId":"BTC-USDT-SWAP","posSide":"long","pos":"2","avgPx":"100","lever":"5","margin":"20","liqPx":"50","upl":"3"},{"instId":"ETH-USDT-SWAP","pos":"0"}]`)
		case "/api/v5/asset/bills":
			return okxData(`[{"billId":"b1","ccy":"USDT","type":"transfer","amt":"1","balChg":"-1","ts":"456"}]`)
		case "/api/v5/asset/transfer":
			return okxData(`[{"transId":"tr-1"}]`)
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
			return okxData(`[]`)
		}
	})
	a := &Adapter{}
	cred := exchange.Credential{APIKey: "ak", APISecret: "sk", Passphrase: "pass"}

	placed, err := a.PlaceOrder(context.Background(), cred, &exchange.PlaceOrderReq{Market: exchange.MarketSpot, Symbol: "BTC-USDT", Side: exchange.SideBuy, Type: exchange.TypeLimit, Quantity: "1", Price: "100", ClientOrderID: "client-1"})
	require.NoError(t, err)
	assert.Equal(t, exchange.StatusSubmitted, placed.Status)

	canceled, err := a.CancelOrder(context.Background(), cred, &exchange.CancelOrderReq{Market: exchange.MarketSpot, Symbol: "BTC-USDT", ClientOrderID: "client-1"})
	require.NoError(t, err)
	assert.Equal(t, exchange.StatusCanceled, canceled.Status)

	amended, err := a.AmendOrder(context.Background(), cred, &exchange.AmendOrderReq{Market: exchange.MarketSpot, Symbol: "BTC-USDT", ClientOrderID: "client-1", NewPrice: "101"})
	require.NoError(t, err)
	assert.Equal(t, exchange.StatusSubmitted, amended.Status)

	require.NoError(t, a.SetLeverage(context.Background(), cred, exchange.MarketSwap, "BTC-USDT-SWAP", "5"))
	require.NoError(t, a.ClosePosition(context.Background(), cred, exchange.MarketSwap, "BTC-USDT-SWAP", "long"))

	got, err := a.GetOrder(context.Background(), cred, &exchange.GetOrderReq{Market: exchange.MarketSpot, Symbol: "BTC-USDT", ClientOrderID: "client-1"})
	require.NoError(t, err)
	assert.Equal(t, exchange.StatusSubmitted, got.Status)

	openOrders, err := a.ListOpenOrders(context.Background(), cred, &exchange.ListOrdersReq{Market: exchange.MarketSpot, Symbol: "BTC-USDT", Limit: 1})
	require.NoError(t, err)
	assert.Len(t, openOrders, 1)

	orders, err := a.ListOrders(context.Background(), cred, &exchange.ListOrdersReq{Market: exchange.MarketSpot, Symbol: "BTC-USDT", Limit: 1})
	require.NoError(t, err)
	assert.Len(t, orders, 1)

	trades, err := a.ListTrades(context.Background(), cred, &exchange.ListTradesReq{Market: exchange.MarketSpot, Symbol: "BTC-USDT", OrderID: "ex-1", Limit: 1})
	require.NoError(t, err)
	require.Len(t, trades, 1)
	assert.Equal(t, int64(123), trades[0].TradedAt)

	positions, err := a.ListPositions(context.Background(), cred, exchange.MarketSwap, "BTC-USDT-SWAP")
	require.NoError(t, err)
	require.Len(t, positions, 1)
	assert.Equal(t, "long", positions[0].PosSide)

	flows, err := a.ListFundFlows(context.Background(), cred, &exchange.FundFlowQuery{Currency: "usdt", Limit: 1})
	require.NoError(t, err)
	require.Len(t, flows, 1)
	assert.Equal(t, -1, flows[0].Direction)

	transfer, err := a.Transfer(context.Background(), cred, &exchange.TransferReq{Currency: "usdt", Amount: "1", From: exchange.MarketSpot, To: exchange.MarketSwap})
	require.NoError(t, err)
	assert.Equal(t, "tr-1", transfer.TransferID)
}
