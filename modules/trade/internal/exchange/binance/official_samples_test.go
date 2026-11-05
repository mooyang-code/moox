package binance

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// 这些测试用官方文档原文（或按 testdata/README.md 说明改写）的样例，
// 防止手写样例掩盖字段名、大小写和类型上的差异。

func sample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func dispatchSample(t *testing.T, market exchange.MarketType, name string) (*recordingHandler, error) {
	t.Helper()
	handler := &recordingHandler{}
	err := testAdapter(market, "http://unused").dispatchPrivate(context.Background(), sample(t, name), handler)
	return handler, err
}

func TestOfficialSpotExecutionReportNew(t *testing.T) {
	handler, err := dispatchSample(t, exchange.MarketTypeSpot, "spot_execution_report_new.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.orders) != 1 || len(handler.fills) != 0 {
		t.Fatalf("orders=%d fills=%d", len(handler.orders), len(handler.fills))
	}
	order := handler.orders[0]
	if order.ClientOrderID != "mUvoqJxFIILMdfAW5iGSOW" || order.ExchangeOrderID != "4293153" ||
		order.ExchangeSymbol != "ETHBTC" || order.Side != exchange.SideBuy ||
		order.OrderType != exchange.OrderTypeLimit || order.TimeInForce != exchange.TimeInForceGTC ||
		order.Quantity.String() != "1" || order.LimitPrice == nil || order.LimitPrice.String() != "0.1026441" ||
		order.Status != exchange.OrderStatusOpen || order.CreatedAt.UnixMilli() != 1499405658657 {
		t.Fatalf("order = %+v", order)
	}
}

func TestOfficialSpotExecutionReportTrade(t *testing.T) {
	handler, err := dispatchSample(t, exchange.MarketTypeSpot, "spot_execution_report_trade.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.orders) != 1 || len(handler.fills) != 1 {
		t.Fatalf("orders=%d fills=%d", len(handler.orders), len(handler.fills))
	}
	order, fill := handler.orders[0], handler.fills[0]
	if order.Status != exchange.OrderStatusFilled || order.FilledQuantity.String() != "1" ||
		order.AveragePrice.String() != "0.1026441" || order.ClientOrderID != "mUvoqJxFIILMdfAW5iGSOW" {
		t.Fatalf("order = %+v", order)
	}
	if fill.ExchangeTradeID != "5001" || fill.ExchangeOrderID != "4293153" ||
		fill.ClientOrderID != "mUvoqJxFIILMdfAW5iGSOW" || fill.Side != exchange.SideBuy ||
		fill.Quantity.String() != "1" || fill.Price.String() != "0.1026441" ||
		fill.Fee.String() != "0.001" || fill.FeeAsset != "ETH" || fill.LiquidityRole != "MAKER" ||
		fill.TradedAt.UnixMilli() != 1499405658699 {
		t.Fatalf("fill = %+v", fill)
	}
}

func TestOfficialSpotExecutionReportCanceledUsesOriginalClientID(t *testing.T) {
	handler, err := dispatchSample(t, exchange.MarketTypeSpot, "spot_execution_report_canceled.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.orders) != 1 || handler.orders[0].ClientOrderID != "mUvoqJxFIILMdfAW5iGSOW" ||
		handler.orders[0].Status != exchange.OrderStatusCanceled {
		t.Fatalf("orders = %+v", handler.orders)
	}
}

func TestOfficialSpotAccountPosition(t *testing.T) {
	handler, err := dispatchSample(t, exchange.MarketTypeSpot, "spot_outbound_account_position.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.accounts) != 1 || len(handler.accounts[0].Balances) != 1 {
		t.Fatalf("accounts = %+v", handler.accounts)
	}
	snapshot := handler.accounts[0]
	balance := snapshot.Balances[0]
	if snapshot.ExchangeUpdatedAt.UnixMilli() != 1564034571073 || balance.Asset != "ETH" ||
		balance.Available.String() != "10000" || balance.Total.String() != "10000" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestOfficialStreamEndEventsRequireReconnect(t *testing.T) {
	for market, name := range map[exchange.MarketType]string{
		exchange.MarketTypeSpot: "spot_event_stream_terminated.json",
		exchange.MarketTypeSwap: "futures_listen_key_expired.json",
	} {
		if _, err := dispatchSample(t, market, name); !errors.Is(err, errListenKeyExpired) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestOfficialFuturesOrderTradeUpdate(t *testing.T) {
	handler, err := dispatchSample(t, exchange.MarketTypeSwap, "futures_order_trade_update.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.orders) != 1 || len(handler.fills) != 1 {
		t.Fatalf("orders=%d fills=%d", len(handler.orders), len(handler.fills))
	}
	order, fill := handler.orders[0], handler.fills[0]
	if order.ClientOrderID != "TEST" || order.ExchangeOrderID != "8886774" ||
		order.Status != exchange.OrderStatusFilled || order.AveragePrice.String() != "7100.5" ||
		order.PositionSide != exchange.PositionSideNet {
		t.Fatalf("order = %+v", order)
	}
	if fill.ExchangeTradeID != "123456" || fill.Side != exchange.SideBuy ||
		fill.Quantity.String() != "0.001" || fill.Price.String() != "7100.5" ||
		fill.Fee.String() != "0.0028402" || fill.FeeAsset != "USDT" || fill.LiquidityRole != "TAKER" {
		t.Fatalf("fill = %+v", fill)
	}
}

func TestOfficialFuturesAccountUpdate(t *testing.T) {
	handler, err := dispatchSample(t, exchange.MarketTypeSwap, "futures_account_update.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.accounts) != 1 || len(handler.positions) != 1 {
		t.Fatalf("accounts=%d positions=%d", len(handler.accounts), len(handler.positions))
	}
	balance := handler.accounts[0].Balances[0]
	position := handler.positions[0]
	if handler.accounts[0].ExchangeUpdatedAt.UnixMilli() != 1564745798938 ||
		balance.Asset != "USDT" || balance.Total.String() != "122624.12345678" ||
		position.ExchangeSymbol != "BTCUSDT" || position.SignedQuantity.String() != "0.001" {
		t.Fatalf("account=%+v position=%+v", handler.accounts[0], position)
	}
}

func TestOfficialTradeListSides(t *testing.T) {
	for _, test := range []struct {
		market exchange.MarketType
		name   string
		symbol string
		role   string
	}{
		{exchange.MarketTypeSwap, "futures_user_trades.json", "BTCUSDT", "TAKER"},
		{exchange.MarketTypeSpot, "spot_my_trades.json", "BNBBTC", "TAKER"},
	} {
		body := sample(t, test.name)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.URL.Query().Get("fromId") == "" {
				_, _ = io.WriteString(w, `{"orderId":1,"clientOrderId":"cid","symbol":"`+test.symbol+`"}`)
				return
			}
			_, _ = w.Write(body)
		}))
		fills, _, err := testAdapter(test.market, server.URL).ListRecentFills(context.Background(), "x", "1")
		server.Close()
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if len(fills) != 1 || fills[0].Side != exchange.SideBuy || fills[0].LiquidityRole != test.role {
			t.Fatalf("%s: fills = %+v", test.name, fills)
		}
	}
}

func TestSignatureIsAppendedLastOverSentBytes(t *testing.T) {
	var rawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		rawQuery = request.URL.RawQuery
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()
	if _, err := testAdapter(exchange.MarketTypeSwap, server.URL).ListOpenOrders(context.Background()); err != nil {
		t.Fatal(err)
	}
	index := strings.LastIndex(rawQuery, "&signature=")
	if index <= 0 || strings.Count(rawQuery, "signature=") != 1 {
		t.Fatalf("query = %s", rawQuery)
	}
	want := hex.EncodeToString(hmacSha256([]byte("secret"), []byte(rawQuery[:index])))
	if rawQuery[index+len("&signature="):] != want {
		t.Fatalf("signature does not cover the sent bytes: %s", rawQuery)
	}
}

func TestExpiredInMatchAndUnknownCodes(t *testing.T) {
	if got := mapOrderStatus("EXPIRED_IN_MATCH", shared.Zero()); got != exchange.OrderStatusExpired {
		t.Fatalf("status = %s", got)
	}
	for _, code := range []string{"-1006", "-1007"} {
		err := classifyAPIError(&exchange.Error{Kind: exchange.ErrorRejected, HTTPStatus: 400}, []byte(`{"code":`+code+`,"msg":"x"}`))
		if !exchange.IsKind(err, exchange.ErrorTransportUnknown) {
			t.Fatalf("%s: err = %v", code, err)
		}
	}
}

// 现货、合约的成交样例解析出来的记录必须带上各自的市场：同名代码、同号订单和成交靠市场区分。
func TestOfficialSamplesCarryTheirMarket(t *testing.T) {
	for _, test := range []struct {
		market exchange.MarketType
		name   string
		wallet exchange.Wallet
	}{
		{exchange.MarketTypeSwap, "futures_user_trades.json", exchange.WalletFutures},
		{exchange.MarketTypeSpot, "spot_my_trades.json", exchange.WalletSpot},
	} {
		body := sample(t, test.name)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.URL.Query().Get("fromId") == "" {
				_, _ = io.WriteString(w, `{"orderId":1,"clientOrderId":"cid","symbol":"x"}`)
				return
			}
			_, _ = w.Write(body)
		}))
		adapter := testAdapter(test.market, server.URL)
		fills, _, err := adapter.ListRecentFills(context.Background(), "x", "1")
		server.Close()
		if err != nil || len(fills) != 1 {
			t.Fatalf("%s: fills = %+v err = %v", test.name, fills, err)
		}
		if fills[0].Market != test.market {
			t.Fatalf("%s: fill market = %q，想要 %q", test.name, fills[0].Market, test.market)
		}
		if adapter.wallet() != test.wallet {
			t.Fatalf("%s: wallet = %q，想要 %q", test.name, adapter.wallet(), test.wallet)
		}
		if got := adapter.key("BTCUSDT"); got != (exchange.SymbolKey{Market: test.market, Symbol: "BTCUSDT"}) {
			t.Fatalf("key = %+v", got)
		}
	}
}
