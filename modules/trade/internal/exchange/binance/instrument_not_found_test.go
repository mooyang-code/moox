package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// 已下架的标的：全市场报价里没有它，单标的请求回 HTTP 400 + -1121，估值要能识别为“标的不存在”。
func TestReferencePriceOfDelistedSymbolIsInstrumentNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("symbol") == "" {
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","price":"100"}]`))
			return
		}
		http.Error(w, `{"code":-1121,"msg":"Invalid symbol."}`, http.StatusBadRequest)
	}))
	defer server.Close()
	adapter := testAdapter(exchange.MarketTypeSpot, server.URL)
	_, err := adapter.GetReferencePrice(context.Background(), exchange.NewSymbolKey(exchange.MarketTypeSpot, "DEADUSDT"))
	if !exchange.IsInstrumentNotFound(err) || !exchange.IsKind(err, exchange.ErrorRejected) {
		t.Fatalf("err = %v", err)
	}
}

// HTTP 200 之后回执字段解析不了：交易所已受理，按结果不明处理，不当确定拒单。
func TestMalformedAcceptedReceiptIsTransportUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":42,"clientOrderId":"cid","origQty":"abc","executedQty":"0","status":"NEW"}`))
	}))
	defer server.Close()
	adapter := testAdapter(exchange.MarketTypeSpot, server.URL)
	_, err := adapter.PlaceOrder(context.Background(), orderRequest(exchange.OrderTypeMarket))
	if !exchange.IsKind(err, exchange.ErrorTransportUnknown) {
		t.Fatalf("下单 err = %v", err)
	}
	_, err = adapter.CancelOrder(context.Background(), exchange.NewSymbolKey(exchange.MarketTypeSpot, "BTCUSDT"), "cid")
	if !exchange.IsKind(err, exchange.ErrorTransportUnknown) {
		t.Fatalf("撤单 err = %v", err)
	}
}

// 已下架的标的：查成交、按标的查挂单回 HTTP 400 + -1121 时都要带上错误码，账户同步据此跳过这个标的，不让整次同步失败。
func TestFillsAndOpenOrdersOfDelistedSymbolAreInstrumentNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"code":-1121,"msg":"Invalid symbol."}`, http.StatusBadRequest)
	}))
	defer server.Close()
	adapter := testAdapter(exchange.MarketTypeSpot, server.URL)
	key := exchange.NewSymbolKey(exchange.MarketTypeSpot, "DEADUSDT")
	if _, _, err := adapter.ListRecentFills(context.Background(), key, ""); !exchange.IsInstrumentNotFound(err) {
		t.Fatalf("查成交 err = %v", err)
	}
	if _, err := adapter.ListOpenOrdersForSymbol(context.Background(), key); !exchange.IsInstrumentNotFound(err) {
		t.Fatalf("按标的查挂单 err = %v", err)
	}
}

// 统一账户周期对账按标的查挂单：其中一个现货标的已下架（-1121）时跳过它，其余标的照常查，整次查询不失败。
func TestPortfolioMarginScopedOpenOrdersSkipsDelistedSymbol(t *testing.T) {
	var queried []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		symbol := request.URL.Query().Get("symbol")
		queried = append(queried, request.URL.Path+"?"+symbol)
		if symbol == "DEADUSDT" {
			http.Error(w, `{"code":-1121,"msg":"Invalid symbol."}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	pm := testPM(server, exchange.MarketTypeSpot)
	_, err := pm.ListOpenOrdersForSymbols(context.Background(), []exchange.SymbolKey{
		exchange.NewSymbolKey(exchange.MarketTypeSpot, "DEADUSDT"),
		exchange.NewSymbolKey(exchange.MarketTypeSpot, "BTCUSDT"),
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(queried) != 2 {
		t.Fatalf("两个标的都要查，实际 %v", queried)
	}
}
