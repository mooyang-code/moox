package okx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/rs/xid"
)

func TestNewUsesFixedEnvironmentEndpoints(t *testing.T) {
	production := New(exchange.AccountConfig{
		Environment: exchange.AccountEnvironmentProduction,
	}, exchange.Credential{})
	testnet := New(exchange.AccountConfig{
		Environment: exchange.AccountEnvironmentTestnet,
	}, exchange.Credential{})
	if production.client.BaseURL != defaultBaseURL ||
		testnet.client.BaseURL != defaultBaseURL {
		t.Fatalf(
			"REST endpoints = %q, %q; want %q",
			production.client.BaseURL, testnet.client.BaseURL, defaultBaseURL,
		)
	}
	if got := privateStreamEndpoint(
		exchange.AccountEnvironmentProduction,
	); got != "wss://ws.okx.com:8443/ws/v5/private" {
		t.Fatalf("production private stream = %q", got)
	}
	if got := privateStreamEndpoint(
		exchange.AccountEnvironmentTestnet,
	); got != "wss://wspap.okx.com:8443/ws/v5/private" {
		t.Fatalf("testnet private stream = %q", got)
	}
}

func TestTestnetRequestAddsSimulationHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Header.Get("x-simulated-trading") != "1" {
			t.Fatalf("x-simulated-trading = %q", request.Header.Get("x-simulated-trading"))
		}
		ok(writer, `[]`)
	}))
	defer server.Close()

	adapter := newWithClient(exchange.AccountConfig{
		Environment: exchange.AccountEnvironmentTestnet,
	}, exchange.Credential{}, httpclient.New(server.URL))
	if _, err := adapter.request(
		context.Background(), http.MethodGet, "/api/v5/public/instruments",
		nil, nil, false,
	); err != nil {
		t.Fatalf("request() error = %v", err)
	}
}

func TestAdapterRequestValidationContract(t *testing.T) {
	adapter := New(exchange.AccountConfig{MarketType: exchange.MarketTypeSpot}, exchange.Credential{})
	base := exchange.OrderRequest{
		ClientOrderID: "contract-client",
		Symbol:        "BTC-USDT",
		OrderType:     exchange.OrderTypeMarket,
		Side:          exchange.SideBuy,
		Quantity:      shared.MustDecimal("1"),
	}
	price := shared.MustDecimal("100")
	tests := []struct {
		name   string
		mutate func(*exchange.OrderRequest)
	}{
		{"MARKET rejects price", func(request *exchange.OrderRequest) {
			request.LimitPrice = &price
		}},
		{"MARKET rejects time in force", func(request *exchange.OrderRequest) {
			request.FillPolicy = exchange.FillPolicyGTC
		}},
		{"LIMIT requires price", func(request *exchange.OrderRequest) {
			request.OrderType = exchange.OrderTypeLimit
			request.FillPolicy = exchange.FillPolicyGTC
		}},
		{"SPOT rejects reduce only", func(request *exchange.OrderRequest) {
			request.ReduceOnly = true
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.mutate(&request)
			_, err := adapter.PlaceOrder(context.Background(), request)
			if !exchange.IsKind(err, exchange.ErrorRejected) {
				t.Fatalf("PlaceOrder() error = %v, want REJECTED", err)
			}
		})
	}
}

func TestOKXRejectsClientOrderIDWithDash(t *testing.T) {
	err := validateRequest(exchange.OrderRequest{
		ClientOrderID: "contains-dash", Symbol: "BTC-USDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
		Quantity: shared.MustDecimal("1"),
	}, exchange.MarketTypeSpot)
	if !exchange.IsKind(err, exchange.ErrorRejected) {
		t.Fatalf("validateRequest() error = %v, want REJECTED", err)
	}
}

func TestOKXRejectsClientOrderIDLongerThan32(t *testing.T) {
	err := validateRequest(exchange.OrderRequest{
		ClientOrderID: strings.Repeat("a", 33), Symbol: "BTC-USDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
		Quantity: shared.MustDecimal("1"),
	}, exchange.MarketTypeSpot)
	if !exchange.IsKind(err, exchange.ErrorRejected) {
		t.Fatalf("validateRequest() error = %v, want REJECTED", err)
	}
}

func TestOKXAcceptsGeneratedClientOrderID(t *testing.T) {
	clientOrderID := xid.New().String()
	err := validateRequest(exchange.OrderRequest{
		ClientOrderID: clientOrderID, Symbol: "BTC-USDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
		Quantity: shared.MustDecimal("1"),
	}, exchange.MarketTypeSpot)
	if err != nil {
		t.Fatalf("validateRequest() error = %v", err)
	}
	if len(clientOrderID) != 20 {
		t.Fatalf("generated client order ID length = %d, want 20", len(clientOrderID))
	}
}

func TestExchangeAdaptersMapFillPolicy(t *testing.T) {
	tests := []struct {
		policy exchange.FillPolicy
		want   string
	}{
		{exchange.FillPolicyGTC, "limit"},
		{exchange.FillPolicyIOC, "ioc"},
		{exchange.FillPolicyFOK, "fok"},
	}
	for _, test := range tests {
		t.Run(string(test.policy), func(t *testing.T) {
			if got := mapOKXLimitType(test.policy); got != test.want {
				t.Fatalf("mapOKXLimitType(%q) = %q, want %q", test.policy, got, test.want)
			}
		})
	}
}

func TestMutationClassifiesItemErrorWhenTopLevelCodeIsOne(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(
			w,
			`{"code":"1","msg":"operation failed","data":[{"sCode":"51008","sMsg":"insufficient balance"}]}`,
		)
	}))
	defer server.Close()
	adapter := newWithClient(exchange.AccountConfig{
		ExchangeAccountID: "account-1", Exchange: exchange.ExchangeOKX,
		MarketType: exchange.MarketTypeSpot, ExecutionMode: exchange.ExecutionModeLive,
		SettlementAsset: "USDT",
	}, exchange.Credential{APIKey: "key", APISecret: "secret", Passphrase: "pass"},
		httpclient.New(server.URL))
	_, err := adapter.PlaceOrder(context.Background(), exchange.OrderRequest{
		ClientOrderID: "cid", Symbol: "BTC-USDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
		Quantity: shared.MustDecimal("0.01"),
	})
	if !exchange.IsKind(err, exchange.ErrorInsufficientBalance) {
		t.Fatalf("error = %v", err)
	}
}

func TestGetReferencePriceUsesMarketTicker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v5/market/ticker" ||
			request.URL.Query().Get("instId") != "BTC-USDT" {
			t.Fatalf("request = %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		ok(w, `[{"instId":"BTC-USDT","last":"100.25","ts":"2000"}]`)
	}))
	defer server.Close()
	adapter := newWithClient(
		exchange.AccountConfig{MarketType: exchange.MarketTypeSpot},
		exchange.Credential{},
		httpclient.New(server.URL),
	)

	quote, err := adapter.GetReferencePrice(context.Background(), "BTC-USDT")

	if err != nil {
		t.Fatal(err)
	}
	if quote.Price.String() != "100.25" || quote.UpdatedAt.UnixMilli() != 2000 {
		t.Fatalf("quote = %+v", quote)
	}
}

func TestSwapMarketOrderConvertsBaseQuantity(t *testing.T) {
	var orderBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			ok(w, `[{"instId":"BTC-USDT-SWAP","instType":"SWAP","settleCcy":"USDT","tickSz":"0.1","lotSz":"1","minSz":"1","ctVal":"0.01","ctValCcy":"BTC","ctType":"linear","state":"live"}]`)
		case "/api/v5/trade/order":
			if request.Method == http.MethodPost {
				if err := json.NewDecoder(request.Body).Decode(&orderBody); err != nil {
					t.Fatal(err)
				}
				ok(w, `[{"ordId":"42","clOrdId":"cid","sCode":"0"}]`)
			} else {
				ok(w, `[{"ordId":"42","clOrdId":"cid","instId":"BTC-USDT-SWAP","ordType":"market","side":"buy","posSide":"net","sz":"5","accFillSz":"5","avgPx":"100","state":"filled","reduceOnly":"true"}]`)
			}
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()
	adapter := swapAdapter(server.URL)
	instruments, err := adapter.LoadInstruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(instruments) != 1 || instruments[0].BaseAsset != "BTC" ||
		instruments[0].QuoteAsset != "USDT" ||
		instruments[0].InstrumentID != "BTC-USDT-SWAP" {
		t.Fatalf("instruments = %+v", instruments)
	}
	order, err := adapter.PlaceOrder(context.Background(), exchange.OrderRequest{
		ClientOrderID: "cid", Symbol: "BTC-USDT-SWAP",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
		PositionSide: exchange.PositionSideNet,
		Quantity:     shared.MustDecimal("0.05"), ReduceOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if orderBody["sz"] != "5" || orderBody["ordType"] != "market" ||
		orderBody["tdMode"] != "cross" || orderBody["posSide"] != "net" ||
		orderBody["reduceOnly"] != "true" {
		t.Fatalf("body = %+v", orderBody)
	}
	if order.ClientOrderID != "cid" || !order.ReduceOnly {
		t.Fatalf("order = %+v", order)
	}
	terminal, err := adapter.GetOrder(context.Background(), "BTC-USDT-SWAP", "cid")
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Quantity.String() != "0.05" || terminal.FilledQuantity.String() != "0.05" ||
		terminal.Status != exchange.OrderStatusFilled {
		t.Fatalf("terminal = %+v", terminal)
	}
}

func TestSpotMarketBuyUsesBaseQuantity(t *testing.T) {
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v5/trade/order" {
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		ok(w, `[{"ordId":"42","clOrdId":"cid","sCode":"0"}]`)
	}))
	defer server.Close()
	adapter := newWithClient(exchange.AccountConfig{
		ExchangeAccountID: "account-1", Exchange: exchange.ExchangeOKX,
		MarketType: exchange.MarketTypeSpot, ExecutionMode: exchange.ExecutionModeLive,
		SettlementAsset: "USDT",
	}, exchange.Credential{APIKey: "key", APISecret: "secret", Passphrase: "pass"},
		httpclient.New(server.URL))
	_, err := adapter.PlaceOrder(context.Background(), exchange.OrderRequest{
		ClientOrderID: "cid", Symbol: "BTC-USDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
		Quantity: shared.MustDecimal("0.01"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["sz"] != "0.01" || body["tgtCcy"] != "base_ccy" {
		t.Fatalf("body = %+v", body)
	}
}

func TestSwapLimitAndLotValidation(t *testing.T) {
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			ok(w, `[{"instId":"BTC-USDT-SWAP","instType":"SWAP","baseCcy":"BTC","quoteCcy":"USDT","settleCcy":"USDT","tickSz":"0.1","lotSz":"2","minSz":"2","ctVal":"0.01","ctValCcy":"BTC","ctType":"linear","state":"live"}]`)
		case "/api/v5/trade/order":
			_ = json.NewDecoder(request.Body).Decode(&body)
			ok(w, `[{"ordId":"42","sCode":"0"}]`)
		}
	}))
	defer server.Close()
	adapter := swapAdapter(server.URL)
	if _, err := adapter.LoadInstruments(context.Background()); err != nil {
		t.Fatal(err)
	}
	price := shared.MustDecimal("100")
	_, err := adapter.PlaceOrder(context.Background(), exchange.OrderRequest{
		ClientOrderID: "cid", Symbol: "BTC-USDT-SWAP",
		OrderType: exchange.OrderTypeLimit, FillPolicy: exchange.FillPolicyFOK,
		Side: exchange.SideSell, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("0.04"), LimitPrice: &price,
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["sz"] != "4" || body["ordType"] != "fok" || body["px"] != "100" {
		t.Fatalf("body = %+v", body)
	}
	_, err = adapter.PlaceOrder(context.Background(), exchange.OrderRequest{
		ClientOrderID: "bad", Symbol: "BTC-USDT-SWAP",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
		PositionSide: exchange.PositionSideNet, Quantity: shared.MustDecimal("0.03"),
	})
	if !exchange.IsKind(err, exchange.ErrorRejected) {
		t.Fatalf("lot error = %v", err)
	}
}

func TestRecentFillsAndPositionsConvertContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			ok(w, `[{"instId":"BTC-USDT-SWAP","instType":"SWAP","baseCcy":"BTC","quoteCcy":"USDT","settleCcy":"USDT","tickSz":"0.1","lotSz":"1","minSz":"1","ctVal":"0.01","ctValCcy":"BTC","ctType":"linear","state":"live"}]`)
		case "/api/v5/trade/fills-history":
			if request.URL.Query().Get("instId") != "BTC-USDT-SWAP" ||
				request.URL.Query().Get("before") != "99" ||
				request.URL.Query().Has("after") {
				t.Fatalf("query = %v", request.URL.Query())
			}
			ok(w, `[{"billId":"101","instId":"BTC-USDT-SWAP","tradeId":"7","ordId":"42","clOrdId":"cid","side":"sell","posSide":"net","fillSz":"3","fillPx":"100","fee":"-0.01","feeCcy":"USDT","fillPnl":"2","execType":"T","ts":"1700000000000"}]`)
		case "/api/v5/account/positions":
			ok(w, `[{"instId":"BTC-USDT-SWAP","posSide":"net","pos":"-4","avgPx":"100","markPx":"101","lever":"5","mgnMode":"cross","margin":"10","liqPx":"50","upl":"2","realizedPnl":"1","uTime":"1700000000000"}]`)
		}
	}))
	defer server.Close()
	adapter := swapAdapter(server.URL)
	if _, err := adapter.LoadInstruments(context.Background()); err != nil {
		t.Fatal(err)
	}
	fills, cursor, err := adapter.ListRecentFills(
		context.Background(), "BTC-USDT-SWAP", "99",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 1 || fills[0].Quantity.String() != "0.03" ||
		fills[0].Fee.String() != "0.01" || fills[0].RealizedPnL.String() != "2" ||
		fills[0].LiquidityRole != "TAKER" || cursor != "101" {
		t.Fatalf("fills=%+v cursor=%s", fills, cursor)
	}
	positions, err := adapter.ListPositionSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 || positions[0].SignedQuantity.String() != "-0.04" {
		t.Fatalf("positions = %+v", positions)
	}
}

func TestRecentFillsPaginatesWithoutSkippingGap(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		var high, low int
		switch {
		case request.URL.Query().Get("before") == "99":
			high, low = 300, 201
		case request.URL.Query().Get("after") == "201":
			high, low = 200, 101
		case request.URL.Query().Get("after") == "101":
			high, low = 100, 100
		default:
			t.Fatalf("query = %v", request.URL.Query())
		}
		rows := make([]fillPayload, 0, high-low+1)
		for billID := high; billID >= low; billID-- {
			id := strconv.Itoa(billID)
			rows = append(rows, fillPayload{
				BillID: id, InstID: "BTC-USDT", TradeID: id,
				OrdID: "42", ClOrdID: "cid", Side: "buy",
				FillSz: "1", FillPx: "100", Ts: "1700000000000",
			})
		}
		data, err := json.Marshal(rows)
		if err != nil {
			t.Fatal(err)
		}
		ok(w, string(data))
	}))
	defer server.Close()
	adapter := newWithClient(exchange.AccountConfig{
		ExchangeAccountID: "account-1", Exchange: exchange.ExchangeOKX,
		MarketType: exchange.MarketTypeSpot, ExecutionMode: exchange.ExecutionModeLive,
		SettlementAsset: "USDT",
	}, exchange.Credential{APIKey: "key", APISecret: "secret", Passphrase: "pass"},
		httpclient.New(server.URL))
	fills, cursor, err := adapter.ListRecentFills(
		context.Background(), "BTC-USDT", "99",
	)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || len(fills) != 201 || cursor != "300" ||
		fills[0].ExchangeTradeID != "100" ||
		fills[len(fills)-1].ExchangeTradeID != "300" {
		t.Fatalf(
			"requests=%d fills=%d first=%s last=%s cursor=%s",
			requests,
			len(fills),
			fills[0].ExchangeTradeID,
			fills[len(fills)-1].ExchangeTradeID,
			cursor,
		)
	}
}

func TestRecentFillsEmptyCursorConsumesAllAvailablePages(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		var high, low int
		switch {
		case !request.URL.Query().Has("before") && !request.URL.Query().Has("after"):
			high, low = 150, 51
		case request.URL.Query().Get("after") == "51":
			high, low = 50, 1
		default:
			t.Fatalf("query = %v", request.URL.Query())
		}
		rows := make([]fillPayload, 0, high-low+1)
		for billID := high; billID >= low; billID-- {
			id := strconv.Itoa(billID)
			rows = append(rows, fillPayload{
				BillID: id, InstID: "BTC-USDT", TradeID: id,
				OrdID: "42", ClOrdID: "cid", Side: "buy",
				FillSz: "1", FillPx: "100", Ts: "1700000000000",
			})
		}
		data, err := json.Marshal(rows)
		if err != nil {
			t.Fatal(err)
		}
		ok(w, string(data))
	}))
	defer server.Close()
	adapter := newWithClient(exchange.AccountConfig{
		ExchangeAccountID: "account-1", Exchange: exchange.ExchangeOKX,
		MarketType: exchange.MarketTypeSpot, ExecutionMode: exchange.ExecutionModeLive,
		SettlementAsset: "USDT",
	}, exchange.Credential{APIKey: "key", APISecret: "secret", Passphrase: "pass"},
		httpclient.New(server.URL))
	fills, cursor, err := adapter.ListRecentFills(context.Background(), "BTC-USDT", "")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(fills) != 150 || cursor != "150" ||
		fills[0].ExchangeTradeID != "1" ||
		fills[len(fills)-1].ExchangeTradeID != "150" {
		t.Fatalf(
			"requests=%d fills=%d first=%s last=%s cursor=%s",
			requests,
			len(fills),
			fills[0].ExchangeTradeID,
			fills[len(fills)-1].ExchangeTradeID,
			cursor,
		)
	}
}

func TestMutationWithMalformedSuccessResponseIsTransportUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":`)
	}))
	defer server.Close()
	adapter := swapAdapter(server.URL)
	adapter.instruments["BTC-USDT-SWAP"] = testInstrument()
	_, err := adapter.PlaceOrder(context.Background(), exchange.OrderRequest{
		ClientOrderID: "cid", Symbol: "BTC-USDT-SWAP",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
		PositionSide: exchange.PositionSideNet, Quantity: shared.MustDecimal("0.01"),
	})
	if !exchange.IsKind(err, exchange.ErrorTransportUnknown) {
		t.Fatalf("error = %v", err)
	}
}

func TestRejectsUnsupportedAndClassifiesErrors(t *testing.T) {
	t.Run("inverse contract", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			ok(w, `[{"instId":"BTC-USD-SWAP","instType":"SWAP","baseCcy":"BTC","quoteCcy":"USD","settleCcy":"BTC","tickSz":"0.1","lotSz":"1","minSz":"1","ctVal":"100","ctValCcy":"USD","ctType":"inverse","state":"live"}]`)
		}))
		defer server.Close()
		instruments, err := swapAdapter(server.URL).LoadInstruments(context.Background())
		if err != nil || len(instruments) != 0 {
			t.Fatalf("instruments=%+v err=%v", instruments, err)
		}
	})
	t.Run("rate limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"code":"50011","msg":"slow down"}`)
		}))
		defer server.Close()
		_, err := swapAdapter(server.URL).GetAccountSnapshot(context.Background())
		if !exchange.IsKind(err, exchange.ErrorRateLimited) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("hedge mode", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/api/v5/account/config" {
				ok(w, `[{"posMode":"long_short_mode"}]`)
				return
			}
			t.Fatalf("unexpected path %s", request.URL.Path)
		}))
		defer server.Close()
		_, err := swapAdapter(server.URL).GetAccountSnapshot(context.Background())
		if !exchange.IsKind(err, exchange.ErrorRejected) {
			t.Fatalf("error = %v", err)
		}
	})
}

func swapAdapter(baseURL string) *Adapter {
	return newWithClient(exchange.AccountConfig{
		ExchangeAccountID: "account-1", Exchange: exchange.ExchangeOKX,
		MarketType: exchange.MarketTypeSwap, ExecutionMode: exchange.ExecutionModeLive,
		SettlementAsset: "USDT", MarginMode: exchange.MarginModeCross,
	}, exchange.Credential{APIKey: "key", APISecret: "secret", Passphrase: "pass"}, httpclient.New(baseURL))
}

func newWithClient(
	config exchange.AccountConfig,
	credential exchange.Credential,
	client *httpclient.Client,
) *Adapter {
	adapter := New(config, credential)
	if client != nil {
		adapter.client = client
	}
	return adapter
}

func ok(w http.ResponseWriter, data string) {
	_, _ = io.WriteString(w, `{"code":"0","msg":"","data":`+data+`}`)
}
