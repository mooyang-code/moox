package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/contracttest"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
)

func TestAdapterRequestValidationContract(t *testing.T) {
	adapter := testAdapter(exchange.MarketTypeSpot, "http://unused")
	contracttest.RunRequestValidation(t, exchange.MarketTypeSpot, adapter.PlaceOrder)
}

func TestAdapterOrderMappings(t *testing.T) {
	tests := []struct {
		name   string
		market exchange.MarketType
		order  exchange.OrderRequest
		check  func(*testing.T, url.Values)
	}{
		{
			name: "spot market", market: exchange.MarketTypeSpot,
			order: orderRequest(exchange.OrderTypeMarket),
			check: func(t *testing.T, values url.Values) {
				if values.Get("type") != "MARKET" || values.Has("price") || values.Has("timeInForce") {
					t.Fatalf("unexpected MARKET query: %v", values)
				}
			},
		},
		{
			name: "spot limit", market: exchange.MarketTypeSpot,
			order: limitOrderRequest(),
			check: func(t *testing.T, values url.Values) {
				if values.Get("type") != "LIMIT" || values.Get("price") != "100.5" ||
					values.Get("timeInForce") != "IOC" {
					t.Fatalf("unexpected LIMIT query: %v", values)
				}
			},
		},
		{
			name: "swap market net reduce only", market: exchange.MarketTypeSwap,
			order: func() exchange.OrderRequest {
				request := orderRequest(exchange.OrderTypeMarket)
				request.PositionSide = exchange.PositionSideNet
				request.ReduceOnly = true
				return request
			}(),
			check: func(t *testing.T, values url.Values) {
				if values.Get("positionSide") != "BOTH" || values.Get("reduceOnly") != "true" {
					t.Fatalf("unexpected SWAP query: %v", values)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost {
					t.Errorf("method = %s", request.Method)
				}
				test.check(t, request.URL.Query())
				_, _ = io.WriteString(w, `{"orderId":42,"clientOrderId":"cid","symbol":"BTCUSDT","status":"NEW"}`)
			}))
			defer server.Close()
			adapter := testAdapter(test.market, server.URL)
			order, err := adapter.PlaceOrder(context.Background(), test.order)
			if err != nil {
				t.Fatal(err)
			}
			if order.ClientOrderID != "cid" || order.ReduceOnly != test.order.ReduceOnly {
				t.Fatalf("roundtrip order = %+v", order)
			}
		})
	}
}

func TestLoadSwapInstruments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"symbols":[{"symbol":"BTCUSDT","contractType":"PERPETUAL","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","marginAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","notional":"5"}]}]}`)
	}))
	defer server.Close()
	adapter := testAdapter(exchange.MarketTypeSwap, server.URL)
	instruments, err := adapter.LoadInstruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(instruments) != 1 || !instruments[0].Linear ||
		instruments[0].ContractValue.String() != "1" ||
		instruments[0].SettlementAsset != "USDT" {
		t.Fatalf("instrument = %+v", instruments)
	}
}

func TestGetReferencePriceUsesMarketTicker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v3/ticker/price" ||
			request.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Fatalf("request = %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"symbol":"BTCUSDT","price":"100.25"}`)
	}))
	defer server.Close()

	before := time.Now()
	quote, err := testAdapter(exchange.MarketTypeSpot, server.URL).
		GetReferencePrice(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if quote.Price.String() != "100.25" || quote.UpdatedAt.Before(before) {
		t.Fatalf("quote = %+v", quote)
	}
}

func TestGetTerminalOrderAndTypedErrors(t *testing.T) {
	t.Run("terminal lookup", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.URL.Query().Get("origClientOrderId") != "cid" {
				t.Fatalf("query = %v", request.URL.Query())
			}
			_, _ = io.WriteString(w, `{"orderId":42,"clientOrderId":"cid","symbol":"BTCUSDT","type":"MARKET","side":"BUY","origQty":"1","executedQty":"1","cummulativeQuoteQty":"100","status":"FILLED"}`)
		}))
		defer server.Close()
		order, err := testAdapter(exchange.MarketTypeSpot, server.URL).
			GetOrder(context.Background(), "BTCUSDT", "cid")
		if err != nil || order.Status != exchange.OrderStatusFilled ||
			order.AveragePrice.String() != "100" {
			t.Fatalf("order=%+v err=%v", order, err)
		}
	})
	t.Run("rate limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"code":-1003,"msg":"slow down"}`)
		}))
		defer server.Close()
		_, err := testAdapter(exchange.MarketTypeSpot, server.URL).
			GetOrder(context.Background(), "BTCUSDT", "cid")
		if !exchange.IsKind(err, exchange.ErrorRateLimited) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unavailable historical cumulative quote", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"orderId":42,"clientOrderId":"cid","symbol":"BTCUSDT","type":"MARKET","side":"BUY","origQty":"1","executedQty":"1","cummulativeQuoteQty":"-1","status":"FILLED"}`)
		}))
		defer server.Close()
		order, err := testAdapter(exchange.MarketTypeSpot, server.URL).
			GetOrder(context.Background(), "BTCUSDT", "cid")
		if err != nil || !order.AveragePrice.IsZero() {
			t.Fatalf("order=%+v err=%v", order, err)
		}
	})
	t.Run("server ambiguity", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()
		_, err := testAdapter(exchange.MarketTypeSpot, server.URL).
			PlaceOrder(context.Background(), orderRequest(exchange.OrderTypeMarket))
		if !exchange.IsKind(err, exchange.ErrorTransportUnknown) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestListRecentFillsNormalizesSwap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("symbol") != "BTCUSDT" ||
			request.URL.Query().Get("fromId") != "7" {
			if request.URL.Query().Get("orderId") != "42" {
				t.Fatalf("query = %v", request.URL.Query())
			}
			_, _ = io.WriteString(w, `{"orderId":42,"clientOrderId":"cid","symbol":"BTCUSDT"}`)
			return
		}
		_, _ = io.WriteString(w, `[{"id":7,"orderId":42,"symbol":"BTCUSDT","price":"100","qty":"0.01","commission":"-0.001","commissionAsset":"USDT","realizedPnl":"2.5","time":1700000000000,"isBuyer":true,"isMaker":false,"positionSide":"BOTH"}]`)
	}))
	defer server.Close()
	fills, cursor, err := testAdapter(exchange.MarketTypeSwap, server.URL).
		ListRecentFills(context.Background(), "BTCUSDT", "6")
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 1 || fills[0].Quantity.String() != "0.01" ||
		fills[0].Fee.String() != "0.001" ||
		fills[0].PositionSide != exchange.PositionSideNet ||
		fills[0].SettlementAsset != "USDT" ||
		fills[0].ClientOrderID != "cid" || cursor != "7" {
		t.Fatalf("fills=%+v cursor=%s", fills, cursor)
	}
}

func TestListRecentFillsConsumesEveryCatchUpPage(t *testing.T) {
	pageCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path == "/api/v3/order" {
			_, _ = io.WriteString(
				writer,
				`{"orderId":42,"clientOrderId":"cid","symbol":"BTCUSDT"}`,
			)
			return
		}
		pageCalls++
		fromID := request.URL.Query().Get("fromId")
		if request.URL.Query().Get("limit") != "1000" {
			t.Fatalf("query = %v", request.URL.Query())
		}
		count := 1000
		start := 0
		if fromID == "1000" {
			count = 1
			start = 1000
		} else if fromID != "0" {
			t.Fatalf("fromId = %s", fromID)
		}
		rows := make([]tradePayload, 0, count)
		for id := start; id < start+count; id++ {
			rows = append(rows, tradePayload{
				ID: json.Number(fmt.Sprint(id)), OrderID: json.Number("42"),
				Symbol: "BTCUSDT", Price: "100", Qty: "0.01",
				Commission: "0", CommissionAsset: "USDT", Time: 1,
				IsBuyer: true,
			})
		}
		_ = json.NewEncoder(writer).Encode(rows)
	}))
	defer server.Close()

	fills, cursor, err := testAdapter(exchange.MarketTypeSpot, server.URL).
		ListRecentFills(context.Background(), "BTCUSDT", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 1001 || cursor != "1000" || pageCalls != 2 {
		t.Fatalf("fills=%d cursor=%s pageCalls=%d", len(fills), cursor, pageCalls)
	}
}

func TestMutationWithMalformedSuccessResponseIsTransportUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"orderId":`)
	}))
	defer server.Close()
	_, err := testAdapter(exchange.MarketTypeSpot, server.URL).
		PlaceOrder(context.Background(), orderRequest(exchange.OrderTypeMarket))
	if !exchange.IsKind(err, exchange.ErrorTransportUnknown) {
		t.Fatalf("error = %v", err)
	}
}

func TestRejectsHedgeModeAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/fapi/v1/positionSide/dual" {
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		_, _ = io.WriteString(w, `{"dualSidePosition":true}`)
	}))
	defer server.Close()
	_, err := testAdapter(exchange.MarketTypeSwap, server.URL).
		GetAccountSnapshot(context.Background())
	if !exchange.IsKind(err, exchange.ErrorRejected) {
		t.Fatalf("error = %v", err)
	}
}

func TestSwapAccountSnapshotUsesAssetUpdateTime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/fapi/v1/positionSide/dual":
			_, _ = io.WriteString(writer, `{"dualSidePosition":false}`)
		case "/fapi/v1/multiAssetsMargin":
			_, _ = io.WriteString(writer, `{"multiAssetsMargin":false}`)
		case "/fapi/v3/account":
			_, _ = io.WriteString(writer, `{
				"totalWalletBalance":"100","totalMarginBalance":"90","availableBalance":"80",
				"totalInitialMargin":"20","totalMaintMargin":"2",
				"totalUnrealizedProfit":"-10",
				"assets":[
					{"asset":"USDT","walletBalance":"100","availableBalance":"80",
					 "initialMargin":"20","unrealizedProfit":"1","updateTime":1700000000000}
				]
			}`)
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()
	snapshot, err := testAdapter(exchange.MarketTypeSwap, server.URL).
		GetAccountSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ExchangeUpdatedAt.UnixMilli() != 1_700_000_000_000 ||
		snapshot.AvailableFunds.String() != "80" ||
		snapshot.Equity.String() != "90" ||
		snapshot.UnrealizedPnL.String() != "-10" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestSwapAccountSnapshotRejectsMultiAssetsMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/fapi/v1/positionSide/dual":
			_, _ = io.WriteString(writer, `{"dualSidePosition":false}`)
		case "/fapi/v1/multiAssetsMargin":
			_, _ = io.WriteString(writer, `{"multiAssetsMargin":true}`)
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()
	_, err := testAdapter(exchange.MarketTypeSwap, server.URL).
		GetAccountSnapshot(context.Background())
	if !exchange.IsKind(err, exchange.ErrorRejected) {
		t.Fatalf("error = %v", err)
	}
}

func testAdapter(market exchange.MarketType, baseURL string) *Adapter {
	config := exchange.AccountConfig{
		ExchangeAccountID: "account-1", Exchange: exchange.ExchangeBinance,
		MarketType: market, ExecutionMode: exchange.ExecutionModeLive,
		SettlementAsset: "USDT",
	}
	if market == exchange.MarketTypeSwap {
		config.MarginMode = exchange.MarginModeCross
	}
	client := httpclient.New(baseURL)
	return NewWithClients(config, exchange.Credential{APIKey: "key", APISecret: "secret"}, client, client)
}

func orderRequest(orderType exchange.OrderType) exchange.OrderRequest {
	return exchange.OrderRequest{
		ClientOrderID: "cid", Symbol: "BTCUSDT", OrderType: orderType,
		Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"),
	}
}

func limitOrderRequest() exchange.OrderRequest {
	request := orderRequest(exchange.OrderTypeLimit)
	price := shared.MustDecimal("100.5")
	request.LimitPrice = &price
	request.TimeInForce = exchange.TimeInForceIOC
	return request
}
