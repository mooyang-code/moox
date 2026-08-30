package test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/binance"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
)

func TestBinanceHTTPMockTargetToFilledOrderConvergesEndToEnd(t *testing.T) {
	now := time.Now().UTC()
	mock := newBinanceHTTPMock(t, now)
	routeBinanceHTTPToMock(t, mock.server.URL)

	adapter := binance.New(exchange.AccountConfig{
		TradingAccountID: testAccount,
		Exchange:         exchange.ExchangeBinance,
		MarketType:       exchange.MarketTypeSpot,
		ExecutionMode:    exchange.ExecutionModeLive,
		Environment:      exchange.AccountEnvironmentTestnet,
		SettlementAsset:  "USDT",
	}, exchange.Credential{
		APIKey:    "mock-api-key",
		APISecret: "mock-api-secret",
	})
	f := newFixture(t, exchange.MarketTypeSpot, adapter)
	seedLogicalAccount(t, f.store)
	f.orders.Now = func() time.Time { return now }
	f.orders.Validator.Now = func() time.Time { return time.Now().UTC() }
	f.sync.Now = func() time.Time { return now.Add(time.Minute) }

	handled := eventconsumer.HandleTarget(
		context.Background(),
		targetDelivery(t, now, "mock-binance-target", 1, "9.99"),
		eventconsumer.TargetOptions{
			Store:          f.store,
			Now:            func() time.Time { return now },
			WeightResolver: testTargetWeightResolver{},
		},
	)
	require.Equal(t, jetstream.ACK, handled.Decision, handled.Err)

	executor := &targetapp.Executor{
		Store: f.store, Orders: f.orders,
		Prices: targetapp.ExchangePriceSource{
			Adapters: adapterSource{adapter: adapter},
		},
		Now:              func() time.Time { return now },
		MaxChildNotional: shared.MustDecimal("1000000"),
	}
	first, err := executor.Converge(
		context.Background(),
		testSpace,
		testLogicalAccount,
	)
	require.NoError(t, err)
	require.Equal(t, "place", first.Action)

	second, err := executor.Converge(
		context.Background(),
		testSpace,
		testLogicalAccount,
	)
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusConverged, second.Status)

	target, err := f.store.GetLogicalAccountTarget(
		context.Background(),
		testSpace,
		testLogicalAccount,
	)
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusConverged, target.Status)

	orders, total, err := f.store.ListOrders(
		context.Background(),
		testSpace,
		store.OrderQuery{TradingAccountID: testAccount, Limit: 10},
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, orders, 1)
	require.Equal(t, string(orderdomain.Filled), orders[0].State)
	require.Equal(t, string(orderdomain.OwnerTarget), orders[0].OwnerType)
	require.Equal(t, "mock-binance-target", orders[0].OwnerID)
	require.Equal(t, "0.01", orders[0].FilledQuantity)

	fills, fillTotal, err := f.store.ListFills(
		context.Background(),
		testSpace,
		store.FillQuery{TradingAccountID: testAccount, Limit: 10},
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), fillTotal)
	require.Len(t, fills, 1)
	require.Equal(t, "1", fills[0].ExchangeTradeID)

	account, err := f.store.GetTradingAccountByID(context.Background(), testAccount)
	require.NoError(t, err)
	require.Equal(t, "9.99", accountAssetTotal(account.Snapshot, "BTC"))

	mock.mu.Lock()
	defer mock.mu.Unlock()
	require.Equal(t, 1, mock.placeCalls)
	require.Equal(t, exchange.SideSell, exchange.Side(mock.placed.Get("side")))
	require.Equal(t, "0.01", mock.placed.Get("quantity"))
	require.Equal(t, "RESULT", mock.placed.Get("newOrderRespType"))
	require.NotEmpty(t, mock.placed.Get("signature"))
	require.NotEmpty(t, mock.clientOrderID)
	require.Equal(t, "mock-api-key", mock.apiKey)
}

type binanceHTTPMock struct {
	server *httptest.Server

	mu            sync.Mutex
	now           time.Time
	placeCalls    int
	placed        url.Values
	clientOrderID string
	apiKey        string
}

func newBinanceHTTPMock(t *testing.T, now time.Time) *binanceHTTPMock {
	t.Helper()
	mock := &binanceHTTPMock{now: now}
	mock.server = httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		mock.handle(t, writer, request)
	}))
	t.Cleanup(mock.server.Close)
	return mock
}

func (m *binanceHTTPMock) handle(
	t *testing.T,
	writer http.ResponseWriter,
	request *http.Request,
) {
	t.Helper()
	switch request.URL.Path {
	case "/api/v3/ticker/price":
		require.Equal(t, "BTCUSDT", request.URL.Query().Get("symbol"))
		_, _ = io.WriteString(writer, `{"symbol":"BTCUSDT","price":"50000"}`)
	case "/api/v3/ticker/24hr":
		require.Equal(t, "BTCUSDT", request.URL.Query().Get("symbol"))
		_, _ = fmt.Fprintf(writer, `{"symbol":"BTCUSDT","bidPrice":"49999","askPrice":"50001","lastPrice":"50000","closeTime":%d}`, m.now.UnixMilli())
	case "/api/v3/order":
		m.handleOrder(t, writer, request)
	case "/api/v3/openOrders":
		_, _ = io.WriteString(writer, `[]`)
	case "/api/v3/account":
		_, _ = fmt.Fprintf(
			writer,
			`{"updateTime":%d,"balances":[{"asset":"USDT","free":"100500","locked":"0"},{"asset":"BTC","free":"9.99","locked":"0"}]}`,
			m.now.Add(time.Second).UnixMilli(),
		)
	case "/api/v3/myTrades":
		m.handleTrades(writer, request)
	default:
		http.Error(writer, "unimplemented mock endpoint", http.StatusNotImplemented)
	}
}

func (m *binanceHTTPMock) handleOrder(
	t *testing.T,
	writer http.ResponseWriter,
	request *http.Request,
) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if request.Method == http.MethodPost {
		m.placeCalls++
		m.placed = request.URL.Query()
		m.clientOrderID = m.placed.Get("newClientOrderId")
		m.apiKey = request.Header.Get("X-MBX-APIKEY")
		m.writeFilledOrder(writer)
		return
	}
	require.Equal(t, http.MethodGet, request.Method)
	m.writeFilledOrder(writer)
}

func (m *binanceHTTPMock) handleTrades(
	writer http.ResponseWriter,
	request *http.Request,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if request.URL.Query().Get("fromId") != "0" {
		_, _ = io.WriteString(writer, `[]`)
		return
	}
	_, _ = fmt.Fprintf(
		writer,
		`[{"id":1,"orderId":42,"symbol":"BTCUSDT","price":"50000","qty":"0.01","commission":"0.1","commissionAsset":"USDT","time":%d,"isBuyer":false,"isMaker":false}]`,
		m.now.Add(time.Second).UnixMilli(),
	)
}

func (m *binanceHTTPMock) writeFilledOrder(writer http.ResponseWriter) {
	_, _ = fmt.Fprintf(
		writer,
		`{"orderId":42,"clientOrderId":%q,"symbol":"BTCUSDT","type":"MARKET","side":"SELL","origQty":"0.01","executedQty":"0.01","cummulativeQuoteQty":"500","status":"FILLED","time":%d,"updateTime":%d}`,
		m.clientOrderID,
		m.now.UnixMilli(),
		m.now.Add(time.Second).UnixMilli(),
	)
}

type rewriteBinanceTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (r rewriteBinanceTransport) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = r.target.Scheme
	clone.URL.Host = r.target.Host
	clone.Host = r.target.Host
	return r.base.RoundTrip(clone)
}

func routeBinanceHTTPToMock(t *testing.T, rawTarget string) {
	t.Helper()
	target, err := url.Parse(rawTarget)
	require.NoError(t, err)
	previous := http.DefaultTransport
	http.DefaultTransport = rewriteBinanceTransport{
		target: target,
		base:   previous,
	}
	t.Cleanup(func() {
		http.DefaultTransport = previous
	})
}

func accountAssetTotal(snapshot store.TradingAccountSnapshot, asset string) string {
	for _, balance := range snapshot.Balances {
		if balance.Asset == asset {
			return balance.Total
		}
	}
	return ""
}
