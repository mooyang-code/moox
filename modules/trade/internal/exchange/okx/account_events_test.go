package okx

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"golang.org/x/net/websocket"
)

type handler struct {
	orders    []exchange.Order
	fills     []exchange.Fill
	positions []exchange.Position
}

func (*handler) OnSubscribed() {}

func (h *handler) OnOrder(_ context.Context, value exchange.Order) error {
	h.orders = append(h.orders, value)
	return nil
}
func (h *handler) OnFill(_ context.Context, value exchange.Fill) error {
	h.fills = append(h.fills, value)
	return nil
}
func (h *handler) OnPosition(_ context.Context, value exchange.Position) error {
	h.positions = append(h.positions, value)
	return nil
}
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

func TestExpectSubscribeAcknowledgementsWaitsForEveryChannel(t *testing.T) {
	channels := []map[string]string{
		{"channel": "orders"},
		{"channel": "account"},
		{"channel": "positions"},
	}
	server := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
		for _, channel := range []string{"account", "positions", "orders"} {
			_ = websocket.JSON.Send(connection, map[string]any{
				"event": "subscribe",
				"arg":   map[string]string{"channel": channel},
			})
		}
	}))
	defer server.Close()
	config, err := websocket.NewConfig(
		"ws"+strings.TrimPrefix(server.URL, "http"),
		"http://localhost",
	)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := expectOKXSubscribeAcks(connection, channels); err != nil {
		t.Fatal(err)
	}
}

func TestExpectSubscribeAcknowledgementsRejectsDuplicate(t *testing.T) {
	server := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
		for range 2 {
			_ = websocket.JSON.Send(connection, map[string]any{
				"event": "subscribe",
				"arg":   map[string]string{"channel": "orders"},
			})
		}
	}))
	defer server.Close()
	config, err := websocket.NewConfig(
		"ws"+strings.TrimPrefix(server.URL, "http"),
		"http://localhost",
	)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, err = expectOKXSubscribeAcks(connection, []map[string]string{
		{"channel": "orders"},
		{"channel": "account"},
	})
	if err == nil {
		t.Fatal("duplicate acknowledgement was accepted")
	}
}

func TestExpectSubscribeAcknowledgementsBuffersInterleavedData(t *testing.T) {
	server := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
		_ = websocket.JSON.Send(connection, map[string]any{
			"event": "subscribe",
			"arg":   map[string]string{"channel": "orders"},
		})
		_ = websocket.JSON.Send(connection, map[string]any{
			"arg":  map[string]string{"channel": "account"},
			"data": []map[string]any{{"uTime": "1", "details": []any{}}},
		})
		_ = websocket.JSON.Send(connection, map[string]any{
			"event": "subscribe",
			"arg":   map[string]string{"channel": "account"},
		})
	}))
	defer server.Close()
	config, err := websocket.NewConfig(
		"ws"+strings.TrimPrefix(server.URL, "http"),
		"http://localhost",
	)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	buffered, err := expectOKXSubscribeAcks(connection, []map[string]string{
		{"channel": "orders"},
		{"channel": "account"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(buffered) != 1 ||
		!strings.Contains(string(buffered[0]), `"channel":"account"`) {
		t.Fatalf("buffered = %s", buffered)
	}
}

func TestReceivePrivateMessageRequiresPongAfterIdle(t *testing.T) {
	server := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
		var payload string
		_ = websocket.Message.Receive(connection, &payload)
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	config, err := websocket.NewConfig(
		"ws"+strings.TrimPrefix(server.URL, "http"),
		"http://localhost",
	)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	err = New(exchange.AccountConfig{}, exchange.Credential{}).
		receivePrivateMessageWithTimeouts(
			context.Background(),
			connection,
			&handler{},
			20*time.Millisecond,
			20*time.Millisecond,
		)
	if !exchange.IsKind(err, exchange.ErrorTransportUnknown) {
		t.Fatalf("error = %v", err)
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
		"data":[{"instId":"BTC-USDT-SWAP","posSide":"net","mgnMode":"cross","pos":"1",
			"avgPx":"not-a-number"}]
	}`), &handler{})
	if err == nil {
		t.Fatal("malformed position decimal was accepted")
	}
}

func TestDispatchPrivatePositionRequiresCrossAndUsesIMR(t *testing.T) {
	adapter := swapAdapter("http://unused")
	adapter.instruments["BTC-USDT-SWAP"] = testInstrument()
	recording := &handler{}
	err := adapter.dispatchPrivate(context.Background(), []byte(`{
		"arg":{"channel":"positions"},
		"data":[{"instId":"BTC-USDT-SWAP","posSide":"net","mgnMode":"cross",
			"pos":"2","avgPx":"100","markPx":"101","lever":"5",
			"imr":"4","margin":"","liqPx":"50","upl":"1","realizedPnl":"2",
			"uTime":"1700000000000"}]
	}`), recording)
	if err != nil {
		t.Fatal(err)
	}
	if len(recording.positions) != 1 ||
		recording.positions[0].UsedMargin.String() != "4" {
		t.Fatalf("positions = %+v", recording.positions)
	}

	err = adapter.dispatchPrivate(context.Background(), []byte(`{
		"arg":{"channel":"positions"},
		"data":[{"instId":"BTC-USDT-SWAP","posSide":"net","mgnMode":"isolated","pos":"2"}]
	}`), &handler{})
	if err == nil {
		t.Fatal("isolated position was accepted")
	}
}

func testInstrument() exchange.Instrument {
	return exchange.Instrument{
		Exchange: exchange.ExchangeOKX, MarketType: exchange.MarketTypeSwap,
		ExchangeSymbol: "BTC-USDT-SWAP", BaseAsset: "BTC", SettlementAsset: "USDT",
		Linear: true, ContractValue: must("0.01"), ContractValueAsset: "BTC",
		ExchangeQuantityStep: must("1"), MinExchangeQuantity: must("1"),
	}
}

func must(value string) shared.Decimal {
	return shared.MustDecimal(value)
}
