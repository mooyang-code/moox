package okx

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"
)

func TestOrdersChannelFeeCompletenessAndRESTParity(t *testing.T) {
	for _, tc := range []struct {
		name, fee, currency, want string
		invalid                   bool
	}{
		{"charge", "-0.01", "USDT", "0.01", false},
		{"rebate", "0.01", "BTC", "-0.01", false},
		{"zero", "0", "USDT", "0", false},
		{"missing amount", "", "USDT", "", true},
		{"missing currency", "-0.01", "", "", true},
		{"invalid amount", "bad", "USDT", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := fmt.Sprintf(`{"instId":"BTC-USDT","ordId":"42","clOrdId":"cid",
			"side":"buy","ordType":"market","sz":"2","accFillSz":"1","avgPx":"100",
			"state":"partially_filled","tradeId":"7","fillSz":"1","fillPx":"100",
			"fillFee":%q,"fillFeeCcy":%q,"fee":"-9","feeCcy":"WRONG",
			"fillTime":"1700000000000","execType":"M"}`, tc.fee, tc.currency)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/api/v5/trade/fills-history", r.URL.Path)
				ok(w, fmt.Sprintf(`[{"billId":"8","instId":"BTC-USDT","ordId":"42","clOrdId":"cid",
				"side":"buy","tradeId":"7","fillSz":"1","fillPx":"100","fee":%q,"feeCcy":%q,
				"ts":"1700000000001","fillTime":"1700000000000","execType":"M"}]`, tc.fee, tc.currency))
			}))
			defer server.Close()
			adapter := newWithClient(exchange.AccountConfig{TradingAccountID: "account-1", MarketType: exchange.MarketTypeSpot, SettlementAsset: "USDT"}, exchange.Credential{}, httpclient.New(server.URL))
			h := &handler{}
			err := adapter.dispatchPrivate(context.Background(), []byte(`{"arg":{"channel":"orders"},"data":[`+row+`]}`), h)
			fills, _, restErr := adapter.ListRecentFills(context.Background(), "BTC-USDT", "")
			if tc.invalid {
				require.Error(t, err)
				require.Empty(t, h.fills)
				require.Error(t, restErr)
				return
			}
			require.NoError(t, err)
			require.NoError(t, restErr)
			require.Len(t, h.fills, 1)
			require.Equal(t, tc.want, h.fills[0].Fee.String())
			require.Equal(t, h.fills, fills)
			for _, first := range []consumer.FillOrigin{consumer.OriginPrivateSocket, consumer.OriginRESTSnapshot} {
				t.Run(string(first), func(t *testing.T) {
					s := openOKXReplayStore(t)
					r := consumer.Reducer{Store: s}
					second := consumer.OriginRESTSnapshot
					if first == second {
						second = consumer.OriginPrivateSocket
					}
					for i, origin := range []consumer.FillOrigin{first, second, first, second} {
						fill := fills[0]
						if origin == consumer.OriginPrivateSocket {
							fill = h.fills[0]
						}
						applied, err := r.ApplyFill(context.Background(), fill, consumer.Source{SpaceID: "space-1", TradingAccountID: "account-1", Kind: origin})
						require.NoError(t, err)
						require.Equal(t, i == 0, applied)
					}
					conflict := fills[0]
					conflict.Fee = conflict.Fee.Add(shared.MustDecimal("1"))
					_, err := r.ApplyFill(context.Background(), conflict, consumer.Source{SpaceID: "space-1", TradingAccountID: "account-1", Kind: second})
					require.ErrorIs(t, err, store.ErrConflict)
					record, err := s.GetOrder(context.Background(), "space-1", "order-1")
					require.NoError(t, err)
					require.Equal(t, "1", record.FilledQuantity)
				})
			}
		})
	}
}

func openOKXReplayStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "replay", Exchange: "OKX", MarketType: "SPOT",
			ExecutionMode: "LIVE", Environment: "PRODUCTION", CredentialSecretID: "unused", SettlementAsset: "USDT", Status: "ENABLED",
		}); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "OKX", MarketType: "SPOT", ExchangeSymbol: "BTC-USDT", InstrumentID: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
			ContractValue: "1", ContractValueAsset: "BTC", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
		}); err != nil {
			return err
		}
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "order-1", TradingAccountID: "account-1", ClientOrderID: "cid", ExchangeOrderID: "42",
			Exchange: "OKX", MarketType: "SPOT", ExchangeSymbol: "BTC-USDT", OrderType: "MARKET", Side: "BUY", Quantity: "2",
			ReferencePrice: "100", ReferencePriceAt: 1700000000000, OwnerType: "EXTERNAL", OwnerID: "42", State: "OPEN", ReservedAsset: "USDT",
			ReservedQuantity: "200", RemainingReservedQuantity: "200", Version: 1,
		})
	}))
	return s
}

func TestFillRejectsMissingExecutionTimeInsteadOfUsingGeneratedTime(t *testing.T) {
	a := New(exchange.AccountConfig{MarketType: exchange.MarketTypeSpot}, exchange.Credential{})
	for _, raw := range []string{"", "invalid", "0", "-1"} {
		t.Run(raw, func(t *testing.T) {
			_, err := a.fill(fillPayload{InstID: "BTC-USDT", FillSz: "1", FillPx: "100", Fee: "0", FeeCcy: "USDT", Ts: "1700000000000", FillTime: raw})
			require.ErrorContains(t, err, "fill execution time is required")
		})
	}
}

func TestOrdersChannelUsesPerFillFee(t *testing.T) {
	adapter := swapAdapter("http://unused")
	adapter.instruments["BTC-USDT-SWAP"] = testInstrument()
	h := &handler{}
	for i := 1; i <= 2; i++ {
		err := adapter.dispatchPrivate(context.Background(), []byte(fmt.Sprintf(`{
			"arg":{"channel":"orders"},"data":[{
			"instId":"BTC-USDT-SWAP","ordId":"42","clOrdId":"cid",
			"side":"sell","posSide":"net","ordType":"market","sz":"5",
			"accFillSz":"%d","avgPx":"100","state":"partially_filled",
			"tradeId":"%d","fillSz":"2","fillPx":"100",
			"fee":"-0.0%d","feeCcy":"USDT","fillFee":"-0.01","fillFeeCcy":"USDT",
			"fillPnl":"0","execType":"M","fillTime":"170000000000%d"
		}]}`, i*2, i, i, i)), h)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(h.fills) != 2 {
		t.Fatalf("fills = %+v", h.fills)
	}
	for _, fill := range h.fills {
		if fill.Fee.String() != "0.01" || fill.FeeAsset != "USDT" {
			t.Fatalf("per-fill fee = %s %s, want 0.01 USDT", fill.Fee, fill.FeeAsset)
		}
	}
}

func TestOrdersChannelMultipleFillsPersistIndependentFees(t *testing.T) {
	adapter := New(exchange.AccountConfig{TradingAccountID: "account-1", MarketType: exchange.MarketTypeSpot, SettlementAsset: "USDT"}, exchange.Credential{})
	h := &handler{}
	for i := 1; i <= 2; i++ {
		require.NoError(t, adapter.dispatchPrivate(context.Background(), []byte(fmt.Sprintf(`{
		"arg":{"channel":"orders"},"data":[{"instId":"BTC-USDT","ordId":"42","clOrdId":"cid",
		"side":"buy","ordType":"market","sz":"2","accFillSz":"%d","avgPx":"100","state":"partially_filled",
		"tradeId":"%d","fillSz":"1","fillPx":"100","fee":"-0.0%d","feeCcy":"USDT",
		"fillFee":"-0.01","fillFeeCcy":"USDT","fillTime":"170000000000%d"}]}`, i, i, i, i)), h))
	}
	s := openOKXReplayStore(t)
	r := consumer.Reducer{Store: s}
	for _, fill := range h.fills {
		applied, err := r.ApplyFill(context.Background(), fill, consumer.Source{SpaceID: "space-1", TradingAccountID: "account-1", Kind: consumer.OriginPrivateSocket})
		require.NoError(t, err)
		require.True(t, applied)
	}
	rows, _, err := s.ListFills(context.Background(), "space-1", store.FillQuery{TradingAccountID: "account-1", Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	total := shared.Zero()
	for _, row := range rows {
		total = total.Add(shared.MustDecimal(row.Fee))
	}
	require.Equal(t, "0.02", total.String())
	record, err := s.GetOrder(context.Background(), "space-1", "order-1")
	require.NoError(t, err)
	require.Equal(t, "2", record.FilledQuantity)
	require.Equal(t, "FILLED", record.State)
}

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
			"fillFee":"-0.01","fillFeeCcy":"USDT","fillPnl":"3","execType":"M",
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
