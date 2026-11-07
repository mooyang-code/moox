package binance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/stretchr/testify/require"
)

// 统一账户适配器的测试：请求路径和参数取自官方 SDK（testdata/README.md），响应用官方样例。

// 编译期断言：统一账户适配器具备执行账户需要的全部能力，普通账户适配器具备划转、抵扣、小额兑换。
var (
	_ execution.ExecutionAdapter      = (*PortfolioMargin)(nil)
	_ execution.AccountEventSource    = (*PortfolioMargin)(nil)
	_ execution.SymbolConfigurer      = (*PortfolioMargin)(nil)
	_ execution.LedgerSource          = (*PortfolioMargin)(nil)
	_ execution.FeeDiscountConfigurer = (*PortfolioMargin)(nil)
	_ execution.DustConverter         = (*PortfolioMargin)(nil)
	_ execution.WalletTransferer      = (*Adapter)(nil)
	_ execution.FeeDiscountConfigurer = (*Adapter)(nil)
	_ execution.DustConverter         = (*Adapter)(nil)
)

type recordedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
}

// pmServer 回放 papi：每个“方法 路径”给一个响应体，记录收到的每个请求。
type pmServer struct {
	t        *testing.T
	mu       sync.Mutex
	routes   map[string]string
	requests []recordedRequest
}

func newPMServer(t *testing.T, routes map[string]string) (*pmServer, *httptest.Server) {
	t.Helper()
	state := &pmServer{t: t, routes: routes}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.requests = append(state.requests, recordedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone()})
		state.mu.Unlock()
		body, ok := state.routes[r.Method+" "+r.URL.Path]
		if !ok {
			t.Errorf("意外的请求 %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"code":-1121,"msg":"Invalid symbol."}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return state, server
}

func (s *pmServer) find(method, path string) []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []recordedRequest
	for _, request := range s.requests {
		if request.Method == method && request.Path == path {
			out = append(out, request)
		}
	}
	return out
}

func pmConfig(markets ...exchange.MarketType) exchange.AccountConfig {
	config := exchange.AccountConfig{
		TradingAccountID: "account-1", Exchange: exchange.ExchangeBinance, Markets: markets,
		ExecutionMode: exchange.ExecutionModeLive, AccountMode: exchange.AccountModeBinancePortfolioMargin,
		Environment: exchange.AccountEnvironmentProduction, SettlementAsset: "USDT",
	}
	for _, market := range markets {
		if market == exchange.MarketTypeSwap {
			config.MarginMode = exchange.MarginModeCross
		}
	}
	return config
}

func testPM(server *httptest.Server, markets ...exchange.MarketType) *PortfolioMargin {
	papi := httpclient.New(server.URL)
	pm := newPortfolioMargin(pmConfig(markets...), exchange.Credential{APIKey: "key", APISecret: "secret"}, papi)
	for _, sub := range pm.markets {
		sub.spot, sub.swap = httpclient.New(server.URL), httpclient.New(server.URL)
	}
	return pm
}

func samplePath(t *testing.T, name string) string { return string(sample(t, name)) }

// withField 取样例 JSON（对象）并改一个字段。
func withFields(t *testing.T, name string, set map[string]any) string {
	t.Helper()
	var value map[string]any
	require.NoError(t, json.Unmarshal(sample(t, name), &value))
	for key, item := range set {
		value[key] = item
	}
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func marketOrderRequest(market exchange.MarketType, symbol string) exchange.OrderRequest {
	request := exchange.OrderRequest{
		ClientOrderID: "cid-1", Market: market, ExchangeSymbol: symbol, OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"),
	}
	if market == exchange.MarketTypeSwap {
		request.PositionSide = exchange.PositionSideNet
	}
	return request
}

// 统一账户现货下单必须显式带 sideEffectType=NO_SIDE_EFFECT：市价、限价（GTC、IOC、FOK）每个入口都断言，
// 并且是真实发出的查询串里带，不是只在内存里设了参数。
func TestPortfolioMarginSpotOrdersAlwaysSendNoSideEffect(t *testing.T) {
	state, server := newPMServer(t, map[string]string{
		"POST /papi/v1/margin/order": samplePath(t, "papi_margin_order_new.json"),
	})
	pm := testPM(server, exchange.MarketTypeSpot, exchange.MarketTypeSwap)
	price := shared.MustDecimal("100")
	requests := []exchange.OrderRequest{marketOrderRequest(exchange.MarketTypeSpot, "BTCUSDT")}
	for _, policy := range []exchange.FillPolicy{exchange.FillPolicyGTC, exchange.FillPolicyIOC, exchange.FillPolicyFOK} {
		request := marketOrderRequest(exchange.MarketTypeSpot, "BTCUSDT")
		request.OrderType, request.FillPolicy, request.LimitPrice = exchange.OrderTypeLimit, policy, &price
		requests = append(requests, request)
	}
	for _, side := range []exchange.Side{exchange.SideBuy, exchange.SideSell} {
		request := marketOrderRequest(exchange.MarketTypeSpot, "BTCUSDT")
		request.Side = side
		requests = append(requests, request)
	}
	for _, request := range requests {
		_, err := pm.PlaceOrder(context.Background(), request)
		require.NoError(t, err)
	}
	sent := state.find(http.MethodPost, "/papi/v1/margin/order")
	require.Len(t, sent, len(requests))
	for _, request := range sent {
		require.Equal(t, "NO_SIDE_EFFECT", request.Query.Get("sideEffectType"), "现货下单没有显式声明不借币：%v", request.Query)
		require.Equal(t, "key", request.Header.Get("X-MBX-APIKEY"))
		require.NotEmpty(t, request.Query.Get("signature"))
	}
}

func TestPortfolioMarginSwapOrderUsesUMEndpointWithoutSideEffect(t *testing.T) {
	// 官方 newUmOrder 样例是双向持仓的（positionSide 为 SHORT），改成单向持仓的 BOTH。
	reply := withFields(t, "papi_um_order_new.json", map[string]any{"positionSide": "BOTH", "type": "LIMIT"})
	state, server := newPMServer(t, map[string]string{"POST /papi/v1/um/order": reply})
	pm := testPM(server, exchange.MarketTypeSpot, exchange.MarketTypeSwap)
	request := marketOrderRequest(exchange.MarketTypeSwap, "BTCUSDT")
	request.ReduceOnly = true
	order, err := pm.PlaceOrder(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, exchange.MarketTypeSwap, order.Market)
	require.Equal(t, "22542179", order.ExchangeOrderID)
	sent := state.find(http.MethodPost, "/papi/v1/um/order")
	require.Len(t, sent, 1)
	require.Equal(t, "BOTH", sent[0].Query.Get("positionSide"))
	require.Equal(t, "true", sent[0].Query.Get("reduceOnly"))
	require.Empty(t, sent[0].Query.Get("sideEffectType"), "合约下单不带现货的借币参数")
}

// 周期对账读挂单：现货（杠杆账户）按给定标的逐个查（带 symbol），不发不带 symbol 的全量查询；合约照常列全部。
func TestPortfolioMarginListOpenOrdersForSymbolsQueriesEachSpotSymbolOnly(t *testing.T) {
	state, server := newPMServer(t, map[string]string{
		"GET /papi/v1/margin/openOrders": samplePath(t, "papi_margin_open_orders.json"),
		"GET /papi/v1/um/openOrders":     `[` + withFields(t, "papi_um_order_query.json", map[string]any{"positionSide": "BOTH"}) + `]`,
	})
	pm := testPM(server, exchange.MarketTypeSpot, exchange.MarketTypeSwap)
	symbols := []exchange.SymbolKey{
		exchange.NewSymbolKey(exchange.MarketTypeSpot, "BNBBTC"),
		exchange.NewSymbolKey(exchange.MarketTypeSpot, "ETHUSDT"),
		exchange.NewSymbolKey(exchange.MarketTypeSpot, "BNBBTC"),
		exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT"),
	}
	orders, err := pm.ListOpenOrdersForSymbols(context.Background(), symbols)
	require.NoError(t, err)

	margin := state.find(http.MethodGet, "/papi/v1/margin/openOrders")
	require.Len(t, margin, 2, "每个现货标的查一次，重复的合并")
	for _, request := range margin {
		require.NotEmpty(t, request.Query.Get("symbol"), "不能发不带 symbol 的全量查询")
	}
	require.ElementsMatch(t, []string{"BNBBTC", "ETHUSDT"}, []string{margin[0].Query.Get("symbol"), margin[1].Query.Get("symbol")})
	require.Len(t, state.find(http.MethodGet, "/papi/v1/um/openOrders"), 1, "合约照常列全部")
	require.NotEmpty(t, orders)

	// 没有现货标的时完全不查现货挂单。
	before := len(margin)
	_, err = pm.ListOpenOrdersForSymbols(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, state.find(http.MethodGet, "/papi/v1/margin/openOrders"), before)
	var _ execution.ScopedOpenOrderSource = pm
}

func TestPortfolioMarginRoutesQueryCancelOpenOrdersAndFillsPerMarket(t *testing.T) {
	umOrder := func(name string) string {
		return withFields(t, name, map[string]any{"positionSide": "BOTH"})
	}
	openUM := `[` + umOrder("papi_um_order_query.json") + `]`
	state, server := newPMServer(t, map[string]string{
		"GET /papi/v1/margin/order":      samplePath(t, "papi_margin_order_query.json"),
		"DELETE /papi/v1/margin/order":   samplePath(t, "papi_margin_order_cancel.json"),
		"GET /papi/v1/margin/openOrders": samplePath(t, "papi_margin_open_orders.json"),
		"GET /papi/v1/margin/myTrades":   samplePath(t, "papi_margin_my_trades.json"),
		"GET /papi/v1/um/order":          umOrder("papi_um_order_query.json"),
		"DELETE /papi/v1/um/order":       umOrder("papi_um_order_cancel.json"),
		"GET /papi/v1/um/openOrders":     openUM,
		"GET /papi/v1/um/userTrades":     strings.Replace(samplePath(t, "papi_um_user_trades.json"), `"time":`, `"time":`, 1),
	})
	pm := testPM(server, exchange.MarketTypeSpot, exchange.MarketTypeSwap)
	ctx := context.Background()
	spotKey, swapKey := exchange.NewSymbolKey(exchange.MarketTypeSpot, "BNBBTC"), exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT")

	spotOrder, err := pm.GetOrder(ctx, spotKey, "ZwfQzuDIGpceVhKW5DvCmO")
	require.NoError(t, err)
	require.Equal(t, exchange.MarketTypeSpot, spotOrder.Market)
	require.Equal(t, exchange.OrderStatusOpen, spotOrder.Status)
	swapOrder, err := pm.GetOrder(ctx, swapKey, "abc")
	require.NoError(t, err)
	require.Equal(t, exchange.MarketTypeSwap, swapOrder.Market)
	require.Equal(t, exchange.PositionSideNet, swapOrder.PositionSide)

	cancelled, err := pm.CancelOrder(ctx, spotKey, "myOrder1")
	require.NoError(t, err)
	require.Equal(t, "myOrder1", cancelled.ClientOrderID, "撤单回执的 clientOrderId 是撤单请求自己的单号，统一用被撤订单的单号")
	_, err = pm.CancelOrder(ctx, swapKey, "myOrder1")
	require.NoError(t, err)

	open, err := pm.ListOpenOrders(ctx)
	require.NoError(t, err)
	require.Len(t, open, 2)
	require.ElementsMatch(t, []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}, []exchange.MarketType{open[0].Market, open[1].Market})

	// 成交：账户创建前（时间戳 2019、2023）的成交不查订单号，只推进游标。
	pm.markets[exchange.MarketTypeSpot].config.CreatedAt = time.Now()
	pm.markets[exchange.MarketTypeSwap].config.CreatedAt = time.Now()
	spotFills, cursor, err := pm.ListRecentFills(ctx, exchange.NewSymbolKey(exchange.MarketTypeSpot, "BNBBTC"), "")
	require.NoError(t, err)
	require.Len(t, spotFills, 1)
	require.Equal(t, exchange.MarketTypeSpot, spotFills[0].Market)
	require.Equal(t, "34", cursor)
	swapFills, _, err := pm.ListRecentFills(ctx, swapKey, "")
	require.NoError(t, err)
	require.Len(t, swapFills, 1)
	require.Equal(t, exchange.MarketTypeSwap, swapFills[0].Market)
	require.Equal(t, exchange.SideSell, swapFills[0].Side)

	require.Len(t, state.find(http.MethodGet, "/papi/v1/margin/myTrades"), 1)
	require.Len(t, state.find(http.MethodGet, "/papi/v1/um/userTrades"), 1)
	// 没有任何请求落到普通账户的现货、合约接口上。
	for _, request := range state.requests {
		require.True(t, strings.HasPrefix(request.Path, "/papi/"), "统一账户的私有请求必须走 papi：%s", request.Path)
	}
}

func TestPortfolioMarginRejectsOrdersForMarketsNotOpened(t *testing.T) {
	_, server := newPMServer(t, nil)
	pm := testPM(server, exchange.MarketTypeSwap)
	_, err := pm.PlaceOrder(context.Background(), marketOrderRequest(exchange.MarketTypeSpot, "BTCUSDT"))
	require.True(t, exchange.IsKind(err, exchange.ErrorRejected), "%v", err)
}

func TestPortfolioMarginAccountSnapshotUsesOfficialSamples(t *testing.T) {
	// 官方 positionRisk 样例的持仓数量是 0，改成一笔真实持仓来核对未实现盈亏取自持仓接口。
	positions := `[` + strings.Replace(strings.Trim(strings.TrimSpace(samplePath(t, "papi_um_position_risk.json")), "[]"), `"positionAmt": "0.000"`, `"positionAmt": "0.500"`, 1) + `]`
	positions = strings.Replace(positions, `"unRealizedProfit": "0.00000000"`, `"unRealizedProfit": "-7.50000000"`, 1)
	require.Contains(t, positions, `"-7.50000000"`)
	_, server := newPMServer(t, map[string]string{
		"GET /papi/v1/balance":              samplePath(t, "papi_balance.json"),
		"GET /papi/v1/account":              samplePath(t, "papi_account.json"),
		"GET /papi/v1/um/positionRisk":      positions,
		"GET /papi/v1/um/positionSide/dual": `{"dualSidePosition": false}`,
	})
	pm := testPM(server, exchange.MarketTypeSpot, exchange.MarketTypeSwap)
	snapshot, err := pm.GetAccountSnapshot(context.Background())
	require.NoError(t, err)

	// 官方 balance 样例：crossMarginAsset 103、借款 10、利息 0.72469206、可用 100、冻结 3；umWalletBalance 0；umUnrealizedPNL 23.72469206。
	byWallet := map[exchange.Wallet]exchange.AssetBalance{}
	for _, balance := range snapshot.Balances {
		byWallet[balance.Wallet] = balance
	}
	margin := byWallet[exchange.WalletMargin]
	require.Equal(t, "103", margin.Total.String())
	require.Equal(t, "100", margin.Available.String())
	require.Equal(t, "3", margin.Locked.String())
	require.Equal(t, "10.72469206", margin.Borrowed.String(), "借款和利息都要从净额里扣")
	require.NotContains(t, byWallet, exchange.WalletUnified, "UM 钱包余额为 0 时不记条目")
	require.Equal(t, "-7.5", snapshot.UnrealizedPnL.String(), "未实现盈亏取持仓接口，不取余额行里的 umUnrealizedPNL")
	require.Equal(t, "73.47428058", snapshot.ExchangeEquity.String())
	require.Equal(t, "23.72469206", snapshot.UsedMargin.String())
	require.False(t, snapshot.ExchangeUpdatedAt.IsZero())
	require.Equal(t, snapshot.ExchangeUpdatedAt, snapshot.WalletUpdatedAt[exchange.WalletMargin])
	require.Equal(t, snapshot.ExchangeUpdatedAt, snapshot.WalletUpdatedAt[exchange.WalletUnified])
	// totalAvailableBalance 在官方样例里是空串：按杠杆钱包可用 + UM 钱包余额 − 已用保证金估算，不低于 0。
	require.Equal(t, "76.27530794", snapshot.AvailableFunds.String())
	require.True(t, snapshot.Present.UnrealizedPnL && snapshot.Present.Balances && snapshot.Present.AvailableFunds)

	// 持仓：官方样例的字段名（unRealizedProfit、leverage、positionAmt）。
	list, err := pm.ListPositionSnapshots(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, exchange.MarketTypeSwap, list[0].Market)
	require.Equal(t, "0.5", list[0].SignedQuantity.String())
	require.Equal(t, "10", list[0].Leverage.String())
	require.Equal(t, exchange.MarginModeCross, list[0].MarginMode)
}

func TestPortfolioMarginSnapshotRejectsHedgeMode(t *testing.T) {
	_, server := newPMServer(t, map[string]string{"GET /papi/v1/um/positionSide/dual": samplePath(t, "papi_um_position_dual.json")})
	pm := testPM(server, exchange.MarketTypeSwap)
	_, err := pm.GetAccountSnapshot(context.Background())
	require.True(t, exchange.IsKind(err, exchange.ErrorRejected), "官方样例 dualSidePosition=true 是双向持仓，必须拒绝：%v", err)
}

func TestPortfolioMarginSpotOnlyAccountDoesNotTouchUMEndpoints(t *testing.T) {
	state, server := newPMServer(t, map[string]string{
		"GET /papi/v1/balance": samplePath(t, "papi_balance.json"),
		"GET /papi/v1/account": samplePath(t, "papi_account.json"),
	})
	pm := testPM(server, exchange.MarketTypeSpot)
	_, err := pm.GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	for _, request := range state.requests {
		require.NotContains(t, request.Path, "/um/")
	}
}

// 私有流事件：ORDER_TRADE_UPDATE、ACCOUNT_UPDATE 带 fs（UM 或 CM），executionReport 是杠杆账户的现货订单。
func TestPortfolioMarginDispatchesStreamEventsByType(t *testing.T) {
	pm := newPortfolioMargin(pmConfig(exchange.MarketTypeSpot, exchange.MarketTypeSwap), exchange.Credential{}, httpclient.New("http://unused"))
	handler := &recordingHandler{}
	ctx := context.Background()

	um := strings.Replace(string(sample(t, "futures_order_trade_update.json")), `"e":"ORDER_TRADE_UPDATE"`, `"e":"ORDER_TRADE_UPDATE","fs":"UM","i":"account-alias"`, 1)
	require.NoError(t, pm.dispatchPrivate(ctx, []byte(um), handler))
	require.Len(t, handler.orders, 1)
	require.Len(t, handler.fills, 1)
	require.Equal(t, exchange.MarketTypeSwap, handler.orders[0].Market)
	require.Equal(t, exchange.MarketTypeSwap, handler.fills[0].Market)

	require.NoError(t, pm.dispatchPrivate(ctx, sample(t, "spot_execution_report_trade.json"), handler))
	require.Len(t, handler.orders, 2)
	require.Equal(t, exchange.MarketTypeSpot, handler.orders[1].Market)
	require.Equal(t, exchange.MarketTypeSpot, handler.fills[1].Market)

	// 币本位（CM）事件不在 MooX 的交易范围，忽略。
	cm := strings.Replace(um, `"fs":"UM"`, `"fs":"CM"`, 1)
	require.NoError(t, pm.dispatchPrivate(ctx, []byte(cm), handler))
	require.Len(t, handler.orders, 2)
}

func TestPortfolioMarginAccountUpdateHasNoMarginTypeField(t *testing.T) {
	pm := newPortfolioMargin(pmConfig(exchange.MarketTypeSpot, exchange.MarketTypeSwap), exchange.Credential{}, httpclient.New("http://unused"))
	handler := &recordingHandler{}
	// 官方统一账户 ACCOUNT_UPDATE 的持仓行字段是 s、pa、ep、cr、up、ps、bep，没有 mt；整理样例去掉 mt 并加上 fs。
	raw := strings.Replace(string(sample(t, "futures_account_update.json")), `"mt":"cross",`, "", 1)
	raw = strings.Replace(raw, `"e":"ACCOUNT_UPDATE"`, `"e":"ACCOUNT_UPDATE","fs":"UM"`, 1)
	require.NoError(t, pm.dispatchPrivate(context.Background(), []byte(raw), handler))
	require.Len(t, handler.accounts, 1)
	require.Equal(t, exchange.WalletUnified, handler.accounts[0].Balances[0].Wallet)
	require.Len(t, handler.positions, 1)
	require.Equal(t, exchange.MarketTypeSwap, handler.positions[0].Market)
	require.Equal(t, exchange.MarginModeCross, handler.positions[0].MarginMode)
}

// 杠杆账户的余额推送只带 free、locked：借款沿用最近一次完整同步的值。
func TestPortfolioMarginMarginAccountEventKeepsBorrowed(t *testing.T) {
	_, server := newPMServer(t, map[string]string{
		"GET /papi/v1/balance": samplePath(t, "papi_balance.json"),
		"GET /papi/v1/account": samplePath(t, "papi_account.json"),
	})
	pm := testPM(server, exchange.MarketTypeSpot)
	_, err := pm.GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	handler := &recordingHandler{}
	event := `{"e":"outboundAccountPosition","E":1564034571105,"u":1564034571073,"B":[{"a":"USDT","f":"90","l":"1"}]}`
	require.NoError(t, pm.dispatchPrivate(context.Background(), []byte(event), handler))
	require.Len(t, handler.accounts, 1)
	balance := handler.accounts[0].Balances[0]
	require.Equal(t, exchange.WalletMargin, balance.Wallet)
	require.Equal(t, "91", balance.Total.String())
	require.Equal(t, "10.72469206", balance.Borrowed.String())
}

func TestPortfolioMarginListenKeyExpiredEndsTheStream(t *testing.T) {
	pm := newPortfolioMargin(pmConfig(exchange.MarketTypeSwap), exchange.Credential{}, httpclient.New("http://unused"))
	err := pm.dispatchPrivate(context.Background(), []byte(`{"e":"listenKeyExpired","E":1,"listenKey":"k"}`), &recordingHandler{})
	require.ErrorIs(t, err, errListenKeyExpired)
}

// 统一账户整个账户只有一条私有流：双市场账户只创建一个 listenKey、只连一条连接，事件按类型分派，不会重复投递。
func TestPortfolioMarginSubscribeOpensSingleStreamForBothMarkets(t *testing.T) {
	var mu sync.Mutex
	listenKeyCalls, connections := 0, 0
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	um := strings.Replace(string(sample(t, "futures_order_trade_update.json")), `"e":"ORDER_TRADE_UPDATE"`, `"e":"ORDER_TRADE_UPDATE","fs":"UM"`, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/papi/v1/listenKey":
			mu.Lock()
			listenKeyCalls++
			mu.Unlock()
			_, _ = w.Write(sample(t, "papi_listen_key.json"))
		case strings.HasPrefix(r.URL.Path, "/pm/ws/"):
			connection, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			mu.Lock()
			connections++
			mu.Unlock()
			_ = connection.WriteMessage(websocket.TextMessage, []byte(um))
			_ = connection.WriteMessage(websocket.TextMessage, sample(t, "spot_execution_report_new.json"))
			_ = connection.WriteMessage(websocket.TextMessage, []byte(`{"e":"listenKeyExpired","E":1,"listenKey":"k"}`))
			_ = connection.Close()
		default:
			t.Errorf("意外的请求 %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	previous := pmStreamURL
	pmStreamURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/pm/ws/"
	defer func() { pmStreamURL = previous }()

	pm := testPM(server, exchange.MarketTypeSpot, exchange.MarketTypeSwap)
	handler := &subscribedRecorder{}
	err := pm.Subscribe(context.Background(), handler)
	require.ErrorIs(t, err, errListenKeyExpired)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, listenKeyCalls, "只创建一个 listenKey")
	require.Equal(t, 1, connections, "只连一条流")
	require.Equal(t, 1, handler.subscribed, "只通知一次已订阅")
	require.Len(t, handler.orders, 2, "每个事件只投递一次")
	require.Equal(t, exchange.MarketTypeSwap, handler.orders[0].Market)
	require.Equal(t, exchange.MarketTypeSpot, handler.orders[1].Market)
}

type subscribedRecorder struct {
	recordingHandler
	subscribed int
}

func (h *subscribedRecorder) OnSubscribed() { h.subscribed++ }

func TestPortfolioMarginFeeDiscountAndDust(t *testing.T) {
	state, server := newPMServer(t, map[string]string{
		"POST /sapi/v1/bnbBurn":        samplePath(t, "sapi_bnb_burn.json"),
		"GET /sapi/v1/bnbBurn":         samplePath(t, "sapi_bnb_burn_status.json"),
		"POST /papi/v1/um/feeBurn":     samplePath(t, "papi_um_fee_burn_toggle.json"),
		"GET /papi/v1/um/feeBurn":      samplePath(t, "papi_um_fee_burn_status.json"),
		"POST /sapi/v1/asset/dust-btc": samplePath(t, "sapi_dust_btc.json"),
		"POST /sapi/v1/asset/dust":     samplePath(t, "sapi_dust.json"),
	})
	pm := testPM(server, exchange.MarketTypeSpot, exchange.MarketTypeSwap)
	ctx := context.Background()
	require.NoError(t, pm.SetBNBFeeDiscount(ctx, true))
	burn := state.find(http.MethodPost, "/sapi/v1/bnbBurn")
	require.Len(t, burn, 1)
	require.Equal(t, "true", burn[0].Query.Get("spotBNBBurn"))
	require.Equal(t, "true", burn[0].Query.Get("interestBNBBurn"), "统一账户的借币利息也用 BNB 抵扣")
	require.Equal(t, "true", state.find(http.MethodPost, "/papi/v1/um/feeBurn")[0].Query.Get("feeBurn"))
	// 官方状态样例 interestBNBBurn=false：统一账户的抵扣没有全部开启。
	on, err := pm.GetBNBFeeDiscount(ctx)
	require.NoError(t, err)
	require.False(t, on)

	dust, err := pm.ListDust(ctx)
	require.NoError(t, err)
	require.Len(t, dust, 1)
	require.Equal(t, "ADA", dust[0].Asset)
	require.Equal(t, "MARGIN", state.find(http.MethodPost, "/sapi/v1/asset/dust-btc")[0].Query.Get("accountType"))
	result, err := pm.ConvertDust(ctx, []string{"ADA", "ETH"})
	require.NoError(t, err)
	require.Equal(t, []string{"ETH"}, result.Converted)
	require.Equal(t, []string{"ADA", "ETH"}, state.find(http.MethodPost, "/sapi/v1/asset/dust")[0].Query["asset"])
	require.False(t, pm.Supports(execution.CapabilityTransfer), "统一账户不划转")
}

func TestPortfolioMarginSetMarginModeIsNoOpAndLeverageUsesUMEndpoint(t *testing.T) {
	state, server := newPMServer(t, map[string]string{"POST /papi/v1/um/leverage": samplePath(t, "papi_um_leverage.json")})
	pm := testPM(server, exchange.MarketTypeSwap)
	key := exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT")
	require.NoError(t, pm.SetMarginMode(context.Background(), key, exchange.MarginModeCross))
	require.NoError(t, pm.SetLeverage(context.Background(), key, shared.MustDecimal("5")))
	require.Len(t, state.requests, 1, "统一账户没有切换保证金模式的接口")
	require.Equal(t, "/papi/v1/um/leverage", state.requests[0].Path)
}

func TestPortfolioMarginLedgerUsesUMIncome(t *testing.T) {
	state, server := newPMServer(t, map[string]string{"GET /papi/v1/um/income": samplePath(t, "papi_um_income.json")})
	pm := testPM(server, exchange.MarketTypeSpot, exchange.MarketTypeSwap)
	require.True(t, pm.LedgerSupported())
	page, err := pm.ListLedger(context.Background(), exchange.LedgerQuery{
		Start: time.UnixMilli(1570000000000), End: time.UnixMilli(1571000000000), Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, exchange.LedgerTransfer, page.Entries[0].Type)
	require.NotEmpty(t, state.find(http.MethodGet, "/papi/v1/um/income"))
	_, err = pm.ListLedger(context.Background(), exchange.LedgerQuery{Market: exchange.MarketTypeSpot, Start: time.UnixMilli(1), End: time.UnixMilli(2), Limit: 1})
	require.Error(t, err, "现货没有资金流水")
}

// 统一账户的现货走杠杆账户下单：exchangeInfo 里不允许杠杆交易的标的不上架；普通账户的现货不受影响。
func TestPortfolioMarginSpotInstrumentsSkipNonMarginSymbols(t *testing.T) {
	var info map[string]any
	require.NoError(t, json.Unmarshal(sample(t, "gct_spot_exchange_info.json"), &info))
	symbols := info["symbols"].([]any)
	require.GreaterOrEqual(t, len(symbols), 2)
	blocked := symbols[0].(map[string]any)["symbol"].(string)
	symbols[0].(map[string]any)["isMarginTradingAllowed"] = false
	raw, err := json.Marshal(info)
	require.NoError(t, err)
	_, server := newPMServer(t, map[string]string{"GET /api/v3/exchangeInfo": string(raw)})

	pm := testPM(server, exchange.MarketTypeSpot)
	instruments, err := pm.LoadInstruments(context.Background())
	require.NoError(t, err)
	require.Len(t, instruments, len(symbols)-1)
	for _, instrument := range instruments {
		require.NotEqual(t, blocked, instrument.ExchangeSymbol)
	}
	standard, err := testAdapter(exchange.MarketTypeSpot, server.URL).LoadInstruments(context.Background())
	require.NoError(t, err)
	require.Len(t, standard, len(symbols), "普通账户的现货不看 isMarginTradingAllowed")
}
