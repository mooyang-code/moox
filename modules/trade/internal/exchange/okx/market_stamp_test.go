package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// 适配器返回的每条订单、成交、持仓都必须带上市场，每条余额都必须带钱包（OKX 只有统一钱包）：
// store 和成交入账对空值一律拒绝。

func TestRESTReadsCarryMarketAndWallet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v5/public/instruments":
			ok(w, `[{"instId":"BTC-USDT-SWAP","instType":"SWAP","baseCcy":"BTC","quoteCcy":"USDT","settleCcy":"USDT","tickSz":"0.1","lotSz":"1","minSz":"1","ctVal":"0.01","ctValCcy":"BTC","ctType":"linear","state":"live"}]`)
		case "/api/v5/trade/fills-history":
			ok(w, `[{"billId":"101","instId":"BTC-USDT-SWAP","tradeId":"7","ordId":"42","clOrdId":"cid","side":"sell","posSide":"net","fillSz":"3","fillPx":"100","fee":"-0.01","feeCcy":"USDT","fillPnl":"2","execType":"T","ts":"1700000000001","fillTime":"1700000000000"}]`)
		case "/api/v5/account/positions":
			ok(w, `[{"instId":"BTC-USDT-SWAP","posSide":"net","pos":"-4","avgPx":"100","markPx":"101","lever":"5","mgnMode":"cross","margin":"10","liqPx":"50","upl":"2","realizedPnl":"1","uTime":"1700000000000"}]`)
		case "/api/v5/account/config":
			ok(w, `[{"posMode":"net_mode"}]`)
		case "/api/v5/account/balance":
			ok(w, balanceWithBTC)
		case "/api/v5/trade/orders-pending":
			ok(w, `[{"instId":"BTC-USDT-SWAP","ordId":"43","clOrdId":"c2","side":"buy","posSide":"net","ordType":"limit","sz":"2","px":"90","accFillSz":"0","avgPx":"0","state":"live","cTime":"1700000000000","uTime":"1700000000000"}]`)
		default:
			t.Errorf("意外的请求 %s", request.URL.Path)
		}
	}))
	defer server.Close()
	adapter := swapAdapter(server.URL)
	ctx := context.Background()
	if _, err := adapter.LoadInstruments(ctx); err != nil {
		t.Fatal(err)
	}
	fills, _, err := adapter.ListRecentFills(ctx, "BTC-USDT-SWAP", "99")
	if err != nil || len(fills) != 1 || fills[0].Market != exchange.MarketTypeSwap {
		t.Fatalf("fills = %+v err = %v", fills, err)
	}
	positions, err := adapter.ListPositionSnapshots(ctx)
	if err != nil || len(positions) != 1 || positions[0].Market != exchange.MarketTypeSwap {
		t.Fatalf("positions = %+v err = %v", positions, err)
	}
	orders, err := adapter.ListOpenOrders(ctx)
	if err != nil || len(orders) != 1 || orders[0].Market != exchange.MarketTypeSwap {
		t.Fatalf("orders = %+v err = %v", orders, err)
	}
	snapshot, err := adapter.GetAccountSnapshot(ctx)
	if err != nil || len(snapshot.Balances) == 0 {
		t.Fatalf("snapshot = %+v err = %v", snapshot, err)
	}
	for _, balance := range snapshot.Balances {
		if balance.Wallet != exchange.WalletUnified {
			t.Errorf("余额 %s 的钱包 = %q", balance.Asset, balance.Wallet)
		}
	}
}

func TestPrivatePushesCarryMarketAndWallet(t *testing.T) {
	raw, err := os.ReadFile("testdata/account_push.json")
	if err != nil {
		t.Fatal(err)
	}
	recorder := &accountRecorder{}
	adapter := swapAdapter("http://unused")
	if err := adapter.dispatchPrivate(context.Background(), raw, recorder); err != nil {
		t.Fatal(err)
	}
	if len(recorder.accounts) != 1 || len(recorder.accounts[0].Balances) == 0 {
		t.Fatalf("accounts = %+v", recorder.accounts)
	}
	for _, balance := range recorder.accounts[0].Balances {
		if balance.Wallet != exchange.WalletUnified {
			t.Errorf("推送里余额 %s 的钱包 = %q", balance.Asset, balance.Wallet)
		}
	}
	adapter.instruments[exchange.SymbolKey{Market: exchange.MarketTypeSwap, Symbol: "BTC-USDT-SWAP"}] = testInstrument()
	if err := adapter.dispatchPrivate(context.Background(), []byte(`{"arg":{"channel":"orders"},"data":[{
		"instId":"BTC-USDT-SWAP","ordId":"42","clOrdId":"cid","side":"sell","posSide":"net","ordType":"market","sz":"5",
		"accFillSz":"2","avgPx":"100","state":"partially_filled","tradeId":"1","fillSz":"2","fillPx":"100",
		"fillFee":"-0.01","fillFeeCcy":"USDT","fillPnl":"0","execType":"M","fillTime":"1700000000001"}]}`), recorder); err != nil {
		t.Fatal(err)
	}
	if len(recorder.orders) != 1 || recorder.orders[0].Market != exchange.MarketTypeSwap ||
		len(recorder.fills) != 1 || recorder.fills[0].Market != exchange.MarketTypeSwap {
		t.Fatalf("orders = %+v fills = %+v", recorder.orders, recorder.fills)
	}
	if err := adapter.dispatchPrivate(context.Background(), []byte(`{"arg":{"channel":"positions"},"data":[{
		"instId":"BTC-USDT-SWAP","posSide":"net","pos":"3","avgPx":"100","markPx":"101","lever":"5","mgnMode":"cross",
		"margin":"10","liqPx":"50","upl":"2","realizedPnl":"1","uTime":"1700000000001"}]}`), recorder); err != nil {
		t.Fatal(err)
	}
	if len(recorder.positions) == 0 || recorder.positions[len(recorder.positions)-1].Market != exchange.MarketTypeSwap {
		t.Fatalf("positions = %+v", recorder.positions)
	}
}
