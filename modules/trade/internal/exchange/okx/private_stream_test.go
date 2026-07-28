package okx

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type handler struct {
	orders []exchange.Order
	fills  []exchange.Fill
}

func (h *handler) OnOrder(_ context.Context, value exchange.Order) error {
	h.orders = append(h.orders, value)
	return nil
}
func (h *handler) OnFill(_ context.Context, value exchange.Fill) error {
	h.fills = append(h.fills, value)
	return nil
}
func (*handler) OnPosition(context.Context, exchange.Position) error { return nil }
func (*handler) OnAccountSnapshot(context.Context, exchange.AccountSnapshot) error {
	return nil
}

func TestDispatchPrivateConvertsSwapFill(t *testing.T) {
	adapter := swapAdapter("http://unused")
	adapter.instruments["BTC-USDT-SWAP"] = testInstrument()
	handler := &handler{}
	err := adapter.dispatchPrivate(context.Background(), []byte(`{
		"arg":{"channel":"orders"},
		"data":[{
			"instId":"BTC-USDT-SWAP","ordId":"42","clOrdId":"cid",
			"side":"sell","posSide":"net","ordType":"market","sz":"5",
			"accFillSz":"2","avgPx":"100","state":"partially_filled",
			"reduceOnly":"true","tradeId":"7","fillSz":"2","fillPx":"100",
			"fee":"-0.01","feeCcy":"USDT","fillPnl":"3","execType":"M",
			"fillTime":"1700000000000"
		}]
	}`), handler)
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.orders) != 1 || handler.orders[0].Quantity.String() != "0.05" ||
		!handler.orders[0].ReduceOnly {
		t.Fatalf("orders = %+v", handler.orders)
	}
	if len(handler.fills) != 1 || handler.fills[0].Quantity.String() != "0.02" ||
		handler.fills[0].RealizedPnL.String() != "3" ||
		handler.fills[0].LiquidityRole != "MAKER" {
		t.Fatalf("fills = %+v", handler.fills)
	}
}

func TestPrivateChannelsExcludeSpotPositions(t *testing.T) {
	spot := New(exchange.AccountConfig{MarketType: exchange.MarketTypeSpot}, exchange.Credential{})
	channels := spot.privateChannels()
	if len(channels) != 2 || channels[0]["channel"] != "orders" ||
		channels[1]["channel"] != "account" {
		t.Fatalf("SPOT channels = %+v", channels)
	}
	swap := New(exchange.AccountConfig{MarketType: exchange.MarketTypeSwap}, exchange.Credential{})
	channels = swap.privateChannels()
	if len(channels) != 3 || channels[2]["channel"] != "positions" ||
		channels[2]["instType"] != "SWAP" {
		t.Fatalf("SWAP channels = %+v", channels)
	}
}

func TestDispatchPrivateIgnoresSubscriptionAcknowledgement(t *testing.T) {
	err := New(exchange.AccountConfig{}, exchange.Credential{}).dispatchPrivate(
		context.Background(),
		[]byte(`{"event":"subscribe","arg":{"channel":"account"}}`),
		&handler{},
	)
	if err != nil {
		t.Fatalf("subscription acknowledgement error = %v", err)
	}
}

func TestDispatchPrivateRejectsMalformedPositionDecimal(t *testing.T) {
	adapter := swapAdapter("http://unused")
	adapter.instruments["BTC-USDT-SWAP"] = testInstrument()
	err := adapter.dispatchPrivate(context.Background(), []byte(`{
		"arg":{"channel":"positions"},
		"data":[{"instId":"BTC-USDT-SWAP","posSide":"net","pos":"1",
			"avgPx":"not-a-number"}]
	}`), &handler{})
	if err == nil {
		t.Fatal("malformed position decimal was accepted")
	}
}

func testInstrument() exchange.Instrument {
	return exchange.Instrument{
		Exchange: exchange.ExchangeOKX, MarketType: exchange.MarketTypeSwap,
		Symbol: "BTC-USDT-SWAP", BaseAsset: "BTC", SettlementAsset: "USDT",
		Linear: true, ContractValue: must("0.01"), ContractValueAsset: "BTC",
		ExchangeQuantityStep: must("1"), MinExchangeQuantity: must("1"),
	}
}

func must(value string) shared.Decimal {
	return shared.MustDecimal(value)
}
