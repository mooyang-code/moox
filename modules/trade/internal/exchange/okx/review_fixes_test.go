package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type accountRecorder struct {
	handler
	accounts []exchange.AccountSnapshot
}

func (h *accountRecorder) OnAccountSnapshot(_ context.Context, value exchange.AccountSnapshot) error {
	h.accounts = append(h.accounts, value)
	return nil
}

const balanceWithBTC = `[{"uTime":"1700000000000","totalEq":"91000","adjEq":"91000","details":[
	{"ccy":"USDT","eq":"1000","cashBal":"1000","availBal":"900","availEq":"900","frozenBal":"100","imr":"50","mmr":"5","upl":"1"},
	{"ccy":"BTC","eq":"1","cashBal":"1","availBal":"1","availEq":"1","frozenBal":"0","imr":"0","mmr":"0","upl":"0"}]}]`

func TestEquityUsesSettlementCurrencyOnBothPaths(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v5/account/config":
			ok(w, `[{"posMode":"net_mode"}]`)
		case "/api/v5/account/balance":
			ok(w, balanceWithBTC)
		}
	}))
	defer server.Close()
	snapshot, err := swapAdapter(server.URL).GetAccountSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Equity.String() != "1000" || snapshot.AvailableFunds.String() != "900" {
		t.Fatalf("REST snapshot = %+v", snapshot)
	}
	recorder := &accountRecorder{}
	err = swapAdapter("http://unused").dispatchPrivate(context.Background(),
		[]byte(`{"arg":{"channel":"account"},"data":`+balanceWithBTC+`}`), recorder)
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.accounts) != 1 || recorder.accounts[0].Equity.String() != "1000" ||
		!recorder.accounts[0].Present.Equity || recorder.accounts[0].RequiresSync {
		t.Fatalf("push = %+v", recorder.accounts)
	}
}

func TestIncrementalPushWithoutSettlementKeepsEquity(t *testing.T) {
	recorder := &accountRecorder{}
	err := swapAdapter("http://unused").dispatchPrivate(context.Background(), []byte(`{"arg":{"channel":"account"},"data":[
		{"uTime":"1700000000000","totalEq":"90000","details":[{"ccy":"BTC","eq":"1","availEq":"1","frozenBal":"0"}]}]}`), recorder)
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.accounts) != 1 {
		t.Fatalf("accounts = %+v", recorder.accounts)
	}
	present := recorder.accounts[0].Present
	if present.Equity || present.AvailableFunds || !present.Balances || !recorder.accounts[0].RequiresSync {
		t.Fatalf("presence = %+v", recorder.accounts[0])
	}
}

func TestAccountAndPositionChannelsPushOnlyOnEvents(t *testing.T) {
	swap := New(exchange.AccountConfig{MarketType: exchange.MarketTypeSwap}, exchange.Credential{})
	for _, channel := range swap.privateChannels() {
		switch channel["channel"] {
		case "account", "positions":
			if channel["extraParams"] != `{"updateInterval": "0"}` {
				t.Fatalf("channel = %+v", channel)
			}
		}
	}
}

func TestOpenOrdersPaginates(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v5/trade/orders-pending" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		pages++
		start, count := 1000, 100
		if after := request.URL.Query().Get("after"); after != "" {
			if after != "901" {
				t.Fatalf("after = %s", after)
			}
			start, count = 900, 50
		}
		body := "["
		for index := 0; index < count; index++ {
			if index > 0 {
				body += ","
			}
			id := strconv.Itoa(start - index)
			body += `{"instId":"BTC-USDT-SWAP","ordId":"` + id + `","clOrdId":"c` + id + `","side":"buy","posSide":"net","ordType":"limit","px":"100","sz":"1","accFillSz":"0","state":"live","cTime":"1700000000000","uTime":"1700000000000"}`
		}
		ok(w, body+"]")
	}))
	defer server.Close()
	adapter := swapAdapter(server.URL)
	adapter.instruments["BTC-USDT-SWAP"] = testInstrument()
	orders, err := adapter.ListOpenOrders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 150 || pages != 2 {
		t.Fatalf("orders=%d pages=%d", len(orders), pages)
	}
}

func TestCancelOrderSendsOneRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		ok(w, `[{"ordId":"42","clOrdId":"cid","sCode":"0","sMsg":""}]`)
	}))
	defer server.Close()
	order, err := swapAdapter(server.URL).CancelOrder(context.Background(), "BTC-USDT-SWAP", "cid")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || order.ExchangeOrderID != "42" || order.ClientOrderID != "cid" {
		t.Fatalf("calls=%d order=%+v", calls, order)
	}
}

func TestOfficialAccountPushUsesSettlementEquity(t *testing.T) {
	raw, err := os.ReadFile("testdata/account_push.json")
	if err != nil {
		t.Fatal(err)
	}
	recorder := &accountRecorder{}
	if err := swapAdapter("http://unused").dispatchPrivate(context.Background(), raw, recorder); err != nil {
		t.Fatal(err)
	}
	if len(recorder.accounts) != 1 {
		t.Fatalf("accounts = %+v", recorder.accounts)
	}
	snapshot := recorder.accounts[0]
	if snapshot.Equity.String() != "4892.951170691435" || snapshot.AvailableFunds.String() != "4734.371190691435" ||
		!snapshot.Present.Equity || snapshot.ExchangeUpdatedAt.UnixMilli() != 1705564223311 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestOKXErrorCodeClassification(t *testing.T) {
	for code, want := range map[string]exchange.ErrorKind{
		"50014": exchange.ErrorRejected, // 参数不能为空
		"50000": exchange.ErrorRejected, // 请求体为空
		"50111": exchange.ErrorAuthentication,
		"50113": exchange.ErrorAuthentication,
		"50105": exchange.ErrorAuthentication,
		"50004": exchange.ErrorTransportUnknown,
		"50013": exchange.ErrorTransportUnknown,
		"51149": exchange.ErrorTransportUnknown,
		"50011": exchange.ErrorOrderRateLimited,
		"51603": exchange.ErrorOrderNotFound,
	} {
		if err := classifyOKXCode(code, "x"); !exchange.IsKind(err, want) {
			t.Fatalf("%s: err = %v, want %s", code, err, want)
		}
	}
}
