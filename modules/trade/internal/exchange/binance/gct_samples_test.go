package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// 这些测试用 gocryptotrader 仓库录制的币安响应（exchanges/binance/testdata/http.json），见 testdata/README.md。

func sampleServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		name, ok := routes[request.Method+" "+request.URL.Path]
		if !ok {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(sample(t, name))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRecordedExchangeInfoFilters(t *testing.T) {
	for _, test := range []struct {
		market exchange.MarketType
		route  string
		file   string
	}{
		{exchange.MarketTypeSpot, "GET /api/v3/exchangeInfo", "gct_spot_exchange_info.json"},
		{exchange.MarketTypeSwap, "GET /fapi/v1/exchangeInfo", "gct_futures_exchange_info.json"},
	} {
		server := sampleServer(t, map[string]string{test.route: test.file})
		instruments, err := testAdapter(test.market, server.URL).LoadInstruments(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", test.file, err)
		}
		found := false
		for _, instrument := range instruments {
			if instrument.ExchangeSymbol != "BTCUSDT" {
				continue
			}
			found = true
			if instrument.MinNotional.IsZero() || instrument.ExchangeQuantityStep.IsZero() ||
				instrument.MinExchangeQuantity.IsZero() || instrument.PriceTick.IsZero() {
				t.Fatalf("%s: BTCUSDT rules = %+v", test.file, instrument)
			}
		}
		if !found {
			t.Fatalf("%s: BTCUSDT missing from %+v", test.file, instruments)
		}
	}
}

func TestRecordedSpotOrders(t *testing.T) {
	server := sampleServer(t, map[string]string{
		"GET /api/v3/openOrders": "gct_spot_open_orders.json",
		"GET /api/v3/order":      "gct_spot_order.json",
		"DELETE /api/v3/order":   "gct_spot_cancel_order.json",
	})
	adapter := testAdapter(exchange.MarketTypeSpot, server.URL)

	open, err := adapter.ListOpenOrders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ClientOrderID != "myOrder1" || open[0].Status != exchange.OrderStatusOpen ||
		open[0].LimitPrice == nil || open[0].LimitPrice.String() != "0.1" || open[0].Quantity.String() != "1" {
		t.Fatalf("open orders = %+v", open)
	}

	order, err := adapter.GetOrder(context.Background(), exchange.NewSymbolKey(exchange.MarketTypeSpot, "BTCUSDT"), "eliteOrder")
	if err != nil {
		t.Fatal(err)
	}
	if order.ExchangeOrderID != "1337" || order.ClientOrderID != "eliteOrder" || order.Status != exchange.OrderStatusOpen {
		t.Fatalf("order = %+v", order)
	}

	// 撤单回执的 clientOrderId 是撤单请求自己的单号，返回值必须是被撤订单的单号；部分成交后撤销。
	canceled, err := adapter.CancelOrder(context.Background(), exchange.NewSymbolKey(exchange.MarketTypeSpot, "LTCBTC"), "myOrder1")
	if err != nil {
		t.Fatal(err)
	}
	if canceled.ClientOrderID != "myOrder1" || canceled.Status != exchange.OrderStatusPartiallyCanceled ||
		canceled.FilledQuantity.String() != "8" || canceled.AveragePrice.String() != "1" {
		t.Fatalf("canceled = %+v", canceled)
	}
}

func TestRecordedFuturesBooks(t *testing.T) {
	server := sampleServer(t, map[string]string{"GET /fapi/v1/ticker/bookTicker": "gct_futures_book_ticker.json"})
	books, err := testAdapter(exchange.MarketTypeSwap, server.URL).allBooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if book := books[exchange.SymbolKey{Market: exchange.MarketTypeSwap, Symbol: "BTCUSDT"}]; book.Bid.String() != "11495.46" || book.Ask.String() != "11495.47" {
		t.Fatalf("books = %+v", books)
	}
}
