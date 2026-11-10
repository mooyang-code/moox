package binance

import (
	"context"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
)

// 强平、自动减仓的成交执行类型是 CALCULATED：有成交号且成交量大于 0 就产生成交。
// 样例按官方文档（Event-Order-Update）的字段改写，见 testdata/README.md。
func TestLiquidationCalculatedEventProducesFill(t *testing.T) {
	handler, err := dispatchSample(t, exchange.MarketTypeSwap, "futures_order_trade_update_calculated.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.orders) != 1 || len(handler.fills) != 1 {
		t.Fatalf("orders=%d fills=%d", len(handler.orders), len(handler.fills))
	}
	fill := handler.fills[0]
	if fill.ClientOrderID != "autoclose-1568879465650" || fill.ExchangeTradeID != "123457" || fill.Side != exchange.SideSell ||
		fill.Quantity.String() != "0.002" || fill.Price.String() != "7060.5" || fill.RealizedPnL.String() != "-12.5" {
		t.Fatalf("fill = %+v", fill)
	}
}

// 统一账户的私有流走另一份分派代码，同样要覆盖 CALCULATED。
func TestPortfolioMarginLiquidationCalculatedEventProducesFill(t *testing.T) {
	pm := newPortfolioMargin(pmConfig(exchange.MarketTypeSpot, exchange.MarketTypeSwap), exchange.Credential{}, httpclient.New("http://unused"))
	handler := &recordingHandler{}
	raw := strings.Replace(string(sample(t, "futures_order_trade_update_calculated.json")), `"e":"ORDER_TRADE_UPDATE"`, `"e":"ORDER_TRADE_UPDATE","fs":"UM"`, 1)
	if err := pm.dispatchPrivate(context.Background(), []byte(raw), handler); err != nil {
		t.Fatal(err)
	}
	if len(handler.orders) != 1 || len(handler.fills) != 1 || handler.fills[0].Quantity.String() != "0.002" {
		t.Fatalf("orders=%d fills=%+v", len(handler.orders), handler.fills)
	}
}

// 没有成交的事件（成交量为 0 或成交号为 -1、为空）不产生成交，订单状态照常更新。
func TestPrivateOrderEventWithoutTradeProducesNoFill(t *testing.T) {
	handler, err := dispatchSample(t, exchange.MarketTypeSwap, "futures_order_trade_update_idle.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.orders) != 1 || len(handler.fills) != 0 {
		t.Fatalf("orders=%d fills=%d", len(handler.orders), len(handler.fills))
	}
	adapter := testAdapter(exchange.MarketTypeSwap, "http://unused")
	for name, patch := range map[string][2]string{
		"成交号为 -1": {`"t":123457`, `"t":-1`},
		"成交量为 0": {`"l":"0.002"`, `"l":"0"`},
	} {
		raw := strings.Replace(string(sample(t, "futures_order_trade_update_calculated.json")), patch[0], patch[1], 1)
		handler := &recordingHandler{}
		if err := adapter.dispatchPrivate(context.Background(), []byte(raw), handler); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(handler.orders) != 1 || len(handler.fills) != 0 {
			t.Fatalf("%s: orders=%d fills=%d", name, len(handler.orders), len(handler.fills))
		}
	}
}
