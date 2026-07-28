package okx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
)

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
		instruments[0].QuoteAsset != "USDT" {
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
	adapter := NewWithClient(exchange.AccountConfig{
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
		OrderType: exchange.OrderTypeLimit, TimeInForce: exchange.TimeInForceFOK,
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
	return NewWithClient(exchange.AccountConfig{
		ExchangeAccountID: "account-1", Exchange: exchange.ExchangeOKX,
		MarketType: exchange.MarketTypeSwap, ExecutionMode: exchange.ExecutionModeLive,
		SettlementAsset: "USDT", MarginMode: exchange.MarginModeCross,
	}, exchange.Credential{APIKey: "key", APISecret: "secret", Passphrase: "pass"}, httpclient.New(baseURL))
}

func ok(w http.ResponseWriter, data string) {
	_, _ = io.WriteString(w, `{"code":"0","msg":"","data":`+data+`}`)
}
