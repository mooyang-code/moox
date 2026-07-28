package binance

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type recordingHandler struct {
	orders []exchange.Order
	fills  []exchange.Fill
}

func (h *recordingHandler) OnOrder(_ context.Context, value exchange.Order) error {
	h.orders = append(h.orders, value)
	return nil
}
func (h *recordingHandler) OnFill(_ context.Context, value exchange.Fill) error {
	h.fills = append(h.fills, value)
	return nil
}
func (*recordingHandler) OnPosition(context.Context, exchange.Position) error { return nil }
func (*recordingHandler) OnAccountSnapshot(context.Context, exchange.AccountSnapshot) error {
	return nil
}

func TestDispatchPrivateSwapFill(t *testing.T) {
	adapter := testAdapter(exchange.MarketTypeSwap, "http://unused")
	handler := &recordingHandler{}
	err := adapter.dispatchPrivate(context.Background(), []byte(`{
		"e":"ORDER_TRADE_UPDATE","o":{
			"x":"TRADE","s":"BTCUSDT","S":"SELL","o":"MARKET","f":"GTC",
			"c":"cid","i":42,"X":"PARTIALLY_FILLED","ps":"BOTH","R":true,
			"q":"1","z":"0.25","ap":"100","t":7,"l":"0.25","L":"100",
			"n":"0.01","N":"USDT","rp":"2","m":true,"T":1700000000000
		}
	}`), handler)
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.orders) != 1 || len(handler.fills) != 1 {
		t.Fatalf("orders=%+v fills=%+v", handler.orders, handler.fills)
	}
	fill := handler.fills[0]
	if fill.PositionSide != exchange.PositionSideNet || fill.RealizedPnL.String() != "2" ||
		fill.SettlementAsset != "USDT" || fill.LiquidityRole != "MAKER" {
		t.Fatalf("fill = %+v", fill)
	}
	if !handler.orders[0].ReduceOnly {
		t.Fatalf("order = %+v", handler.orders[0])
	}
}
