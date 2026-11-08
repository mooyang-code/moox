package binance

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// 单市场适配器收到别的市场的标的键时直接报错，不会悄悄按本市场处理。
func TestAdapterRejectsKeyOfAnotherMarket(t *testing.T) {
	adapter := testAdapter(exchange.MarketTypeSwap, "http://unused")
	wrong := exchange.NewSymbolKey(exchange.MarketTypeSpot, "BTCUSDT")

	if _, err := adapter.GetOrder(context.Background(), wrong, "client"); err == nil {
		t.Fatal("合约适配器收到现货标的键应该报错")
	}
	if _, err := adapter.CancelOrder(context.Background(), wrong, "client"); err == nil {
		t.Fatal("合约适配器收到现货标的键应该报错")
	}
	if err := adapter.SetLeverage(context.Background(), wrong, shared.MustDecimal("5")); err == nil {
		t.Fatal("合约适配器收到现货标的键应该报错")
	}
}
