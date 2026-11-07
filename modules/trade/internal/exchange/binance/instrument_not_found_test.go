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
