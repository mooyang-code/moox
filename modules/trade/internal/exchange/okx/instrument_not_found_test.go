package okx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
)

// 单标的 ticker 退回请求：OKX 回 51001，或成功但没有行，都按“标的不存在”返回，估值据此降级。
func TestReferencePriceOfMissingInstrumentIsInstrumentNotFound(t *testing.T) {
	for name, body := range map[string]string{
		"51001": `{"code":"51001","msg":"Instrument ID does not exist.","data":[]}`,
		"空数据":   `{"code":"0","msg":"","data":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/api/v5/market/tickers" {
					ok(w, `[{"instId":"BTC-USDT","last":"100","ts":"1"}]`)
					return
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			adapter := newWithClient(
				exchange.AccountConfig{Markets: []exchange.MarketType{exchange.MarketTypeSpot}},
				exchange.Credential{}, httpclient.New(server.URL),
			)
			_, err := adapter.GetReferencePrice(context.Background(), exchange.NewSymbolKey(exchange.MarketTypeSpot, "DEAD-USDT"))
			if !exchange.IsInstrumentNotFound(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
