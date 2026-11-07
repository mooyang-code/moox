package binance

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// 适配器返回的每条订单、成交、持仓都必须带上市场，每条余额都必须带钱包：
// store 和成交入账对空值一律拒绝，漏一处整个账户的同步都会失败。

type stampSample struct {
	market exchange.MarketType
	file   string
}

func TestEverySamplePrivateEventCarriesMarketAndWallet(t *testing.T) {
	for _, test := range []stampSample{
		{exchange.MarketTypeSpot, "spot_execution_report_new.json"},
		{exchange.MarketTypeSpot, "spot_execution_report_trade.json"},
		{exchange.MarketTypeSpot, "spot_execution_report_canceled.json"},
		{exchange.MarketTypeSpot, "spot_outbound_account_position.json"},
		{exchange.MarketTypeSwap, "futures_order_trade_update.json"},
		{exchange.MarketTypeSwap, "futures_account_update.json"},
	} {
		handler, err := dispatchSample(t, test.market, test.file)
		if err != nil {
			t.Fatalf("%s: %v", test.file, err)
		}
		wallet := exchange.WalletSpot
		if test.market == exchange.MarketTypeSwap {
			wallet = exchange.WalletFutures
		}
		seen := len(handler.orders) + len(handler.fills) + len(handler.positions) + len(handler.accounts)
		if seen == 0 {
			t.Fatalf("%s: 没有解析出任何事件", test.file)
		}
		for _, order := range handler.orders {
			if order.Market != test.market {
				t.Errorf("%s: 订单市场 = %q", test.file, order.Market)
			}
		}
		for _, fill := range handler.fills {
			if fill.Market != test.market {
				t.Errorf("%s: 成交市场 = %q", test.file, fill.Market)
			}
		}
		for _, position := range handler.positions {
			if position.Market != test.market {
				t.Errorf("%s: 持仓市场 = %q", test.file, position.Market)
			}
		}
		for _, snapshot := range handler.accounts {
			if len(snapshot.Balances) == 0 {
				t.Errorf("%s: 余额为空", test.file)
			}
			for _, balance := range snapshot.Balances {
				if balance.Wallet != wallet {
					t.Errorf("%s: 余额 %s 的钱包 = %q，想要 %q", test.file, balance.Asset, balance.Wallet, wallet)
				}
			}
		}
	}
}

func TestRESTReadsCarryMarketAndWallet(t *testing.T) {
	responses := map[string]string{
		"/api/v3/account":            `{"updateTime":1,"balances":[{"asset":"USDT","free":"10","locked":"1"}]}`,
		"/fapi/v3/account":           `{"totalMarginBalance":"100","availableBalance":"90","assets":[{"asset":"USDT","walletBalance":"100","availableBalance":"90","updateTime":2}]}`,
		"/fapi/v3/positionRisk":      `[{"symbol":"BTCUSDT","positionAmt":"0.5","entryPrice":"100","markPrice":"101","positionSide":"BOTH","updateTime":3,"liquidationPrice":"0","unRealizedProfit":"0","isolatedWallet":"0"}]`,
		"/api/v3/openOrders":         string(sample(t, "gct_spot_open_orders.json")),
		"/fapi/v1/openOrders":        `[{"orderId":9,"clientOrderId":"c","symbol":"BTCUSDT","type":"LIMIT","timeInForce":"GTC","side":"BUY","positionSide":"BOTH","origQty":"1","executedQty":"0","avgPrice":"0","price":"100","status":"NEW","time":1,"updateTime":1}]`,
		"/fapi/v1/positionSide/dual": `{"dualSidePosition":false}`,
		"/fapi/v1/multiAssetsMargin": `{"multiAssetsMargin":false}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, ok := responses[request.URL.Path]
		if !ok {
			t.Errorf("意外的请求 %s", request.URL.Path)
			http.NotFound(w, request)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		adapter := testAdapter(market, server.URL)
		snapshot, err := adapter.GetAccountSnapshot(context.Background())
		if err != nil || len(snapshot.Balances) == 0 {
			t.Fatalf("%s: snapshot = %+v err = %v", market, snapshot, err)
		}
		for _, balance := range snapshot.Balances {
			if balance.Wallet != adapter.wallet() || !balance.Wallet.Valid() {
				t.Errorf("%s: 余额钱包 = %q", market, balance.Wallet)
			}
		}
		orders, err := adapter.ListOpenOrders(context.Background())
		if err != nil || len(orders) == 0 {
			t.Fatalf("%s: orders = %+v err = %v", market, orders, err)
		}
		for _, order := range orders {
			if order.Market != market {
				t.Errorf("%s: 订单市场 = %q", market, order.Market)
			}
		}
		positions, err := adapter.ListPositionSnapshots(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", market, err)
		}
		if market == exchange.MarketTypeSwap && len(positions) != 1 {
			t.Fatalf("合约应有一条持仓：%+v", positions)
		}
		for _, position := range positions {
			if position.Market != market {
				t.Errorf("%s: 持仓市场 = %q", market, position.Market)
			}
		}
	}
}

// 适配器读到的快照要能真正写进库：store 对空钱包、重复的（钱包，资产）一律拒绝。
func TestAdapterSnapshotsPersistToStore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v3/account":
			_, _ = io.WriteString(w, `{"updateTime":1,"balances":[{"asset":"USDT","free":"10","locked":"1"},{"asset":"BTC","free":"1","locked":"0"}]}`)
		case "/fapi/v3/account":
			_, _ = io.WriteString(w, `{"totalMarginBalance":"100","availableBalance":"90","assets":[{"asset":"USDT","walletBalance":"100","availableBalance":"90","updateTime":2}]}`)
		case "/fapi/v1/positionSide/dual":
			_, _ = io.WriteString(w, `{"dualSidePosition":false}`)
		case "/fapi/v3/positionRisk":
			_, _ = io.WriteString(w, `[]`)
		default:
			t.Errorf("意外的请求 %s", request.URL.Path)
		}
	}))
	defer server.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	account := func(id string, market exchange.MarketType, snapshot exchange.AccountSnapshot) store.TradingAccountRecord {
		record := store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: id, Name: id, Exchange: "BINANCE", Markets: []string{string(market)},
			ExecutionMode: "LIVE", Environment: "TESTNET", CredentialSecretID: "secret-" + id, SettlementAsset: "USDT",
			Status: "ENABLED", Snapshot: store.SnapshotFromExchange(snapshot),
		}
		if market == exchange.MarketTypeSwap {
			record.MarginMode = "CROSS"
		}
		return record
	}
	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		snapshot, err := testAdapter(market, server.URL).GetAccountSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		record := account("account-"+string(market), market, snapshot)
		if err := db.Transaction(context.Background(), func(tx *store.Tx) error { return tx.CreateTradingAccount(record) }); err != nil {
			t.Fatalf("%s: 适配器的快照写不进库：%v", market, err)
		}
		got, err := db.GetTradingAccount(context.Background(), "space-1", record.TradingAccountID)
		if err != nil {
			t.Fatal(err)
		}
		wallet := string(testAdapter(market, server.URL).wallet())
		if len(got.Snapshot.WalletBalances(wallet)) != len(snapshot.Balances) {
			t.Fatalf("%s: 钱包 %s 的余额 = %+v", market, wallet, got.Snapshot.Balances)
		}
	}
}
