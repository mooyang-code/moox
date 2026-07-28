package binance

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestSpotSubscriptionUsesSignedWebSocketAPIRequest(t *testing.T) {
	adapter := New(
		exchange.AccountConfig{MarketType: exchange.MarketTypeSpot},
		exchange.Credential{APIKey: "key", APISecret: "secret"},
	)
	request := adapter.newSpotSubscriptionRequest(1_700_000_000_000)
	if request.Method != "userDataStream.subscribe.signature" {
		t.Fatalf("method = %s", request.Method)
	}
	values := url.Values{
		"apiKey":     []string{"key"},
		"recvWindow": []string{fmt.Sprint(recvWindow)},
		"timestamp":  []string{"1700000000000"},
	}
	want := fmt.Sprintf("%x", hmacSha256([]byte("secret"), []byte(values.Encode())))
	if request.Params["signature"] != want ||
		request.Params["timestamp"] != int64(1_700_000_000_000) {
		t.Fatalf("params = %+v", request.Params)
	}
}

func TestSpotSubscriptionClassifiesAuthenticationFailure(t *testing.T) {
	err := classifySpotSubscriptionError(
		401,
		[]byte(`{"code":-2015,"msg":"Invalid API-key"}`),
	)
	if !exchange.IsKind(err, exchange.ErrorAuthentication) {
		t.Fatalf("error = %v", err)
	}
}

func TestSpotSubscriptionClassifiesIPBanAsRateLimited(t *testing.T) {
	err := classifySpotSubscriptionError(
		418,
		[]byte(`{"code":-1003,"msg":"IP banned"}`),
	)
	if !exchange.IsKind(err, exchange.ErrorRateLimited) {
		t.Fatalf("error = %v", err)
	}
}

type recordingHandler struct {
	orders    []exchange.Order
	fills     []exchange.Fill
	positions []exchange.Position
}

func (h *recordingHandler) OnOrder(_ context.Context, value exchange.Order) error {
	h.orders = append(h.orders, value)
	return nil
}
func (h *recordingHandler) OnFill(_ context.Context, value exchange.Fill) error {
	h.fills = append(h.fills, value)
	return nil
}
func (h *recordingHandler) OnPosition(_ context.Context, value exchange.Position) error {
	h.positions = append(h.positions, value)
	return nil
}
func (*recordingHandler) OnAccountSnapshot(context.Context, exchange.AccountSnapshot) error {
	return nil
}

func TestDispatchPrivateSpotWebSocketAPIEnvelope(t *testing.T) {
	adapter := testAdapter(exchange.MarketTypeSpot, "http://unused")
	handler := &recordingHandler{}
	err := adapter.dispatchPrivate(context.Background(), []byte(`{
		"subscriptionId":0,
		"event":{
			"e":"executionReport","x":"NEW","s":"BTCUSDT","S":"BUY",
			"o":"MARKET","f":"GTC","c":"cid","i":42,"X":"FILLED",
			"q":"2","z":"2","Z":"210","T":1700000000000
		}
	}`), handler)
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.orders) != 1 || handler.orders[0].AveragePrice.String() != "105" {
		t.Fatalf("orders = %+v", handler.orders)
	}
}

func TestDispatchPrivateRejectsIsolatedSwapPosition(t *testing.T) {
	adapter := testAdapter(exchange.MarketTypeSwap, "http://unused")
	err := adapter.dispatchPrivate(context.Background(), []byte(`{
		"e":"ACCOUNT_UPDATE","E":1700000000000,
		"a":{"B":[],"P":[{"s":"BTCUSDT","pa":"1","ep":"100","up":"0","mt":"isolated","ps":"BOTH"}]}
	}`), &recordingHandler{})
	if err == nil {
		t.Fatal("isolated position was accepted")
	}
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
