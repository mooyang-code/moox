package okx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

// OKX 现货+永续双市场账户经 MultiMarket 设全仓和杠杆：会话 EnsureSymbolConfig 走的就是这条路径，
// 子适配器不满足 execution.SymbolConfigurer 时会被当成“不支持设置保证金模式”拒绝。
func TestMultiMarketSetsOKXSwapLeverage(t *testing.T) {
	var leverageBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v5/account/set-leverage" {
			t.Fatalf("意外请求 %s", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&leverageBody); err != nil {
			t.Fatal(err)
		}
		ok(w, `[{"instId":"BTC-USDT-SWAP","lever":"3","mgnMode":"cross","posSide":"net"}]`)
	}))
	defer server.Close()
	config := func(market exchange.MarketType) exchange.AccountConfig {
		return exchange.AccountConfig{
			TradingAccountID: "account-1", Exchange: exchange.ExchangeOKX,
			Markets: []exchange.MarketType{market}, ExecutionMode: exchange.ExecutionModeLive,
			SettlementAsset: "USDT", MarginMode: exchange.MarginModeCross,
		}
	}
	credential := exchange.Credential{APIKey: "key", APISecret: "secret", Passphrase: "pass"}
	spot := newWithClient(config(exchange.MarketTypeSpot), credential, httpclient.New(server.URL))
	swap := newWithClient(config(exchange.MarketTypeSwap), credential, httpclient.New(server.URL))
	multi, err := execution.NewMultiMarket(exchange.ExchangeOKX,
		[]exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
		map[exchange.MarketType]execution.ExecutionAdapter{exchange.MarketTypeSpot: spot, exchange.MarketTypeSwap: swap},
	)
	if err != nil {
		t.Fatal(err)
	}
	key := exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTC-USDT-SWAP")
	if err := multi.SetMarginMode(context.Background(), key, exchange.MarginModeCross); err != nil {
		t.Fatalf("SetMarginMode = %v", err)
	}
	if err := multi.SetLeverage(context.Background(), key, shared.MustDecimal("3")); err != nil {
		t.Fatalf("SetLeverage = %v", err)
	}
	if leverageBody["instId"] != "BTC-USDT-SWAP" || leverageBody["lever"] != "3" || leverageBody["mgnMode"] != "cross" {
		t.Fatalf("设杠杆请求 = %+v", leverageBody)
	}
	// 标的键的市场和适配器不一致时拒绝，不把现货标的当永续设置。
	if err := swap.SetMarginMode(context.Background(), exchange.NewSymbolKey(exchange.MarketTypeSpot, "BTC-USDT"), exchange.MarginModeCross); !exchange.IsKind(err, exchange.ErrorRejected) {
		t.Fatalf("跨市场 SetMarginMode = %v", err)
	}
}
