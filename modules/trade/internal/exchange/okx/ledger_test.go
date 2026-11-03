package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestBillsLedgerPaginatesAndMapsTypes(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		query := request.URL.Query()
		if query.Has("instType") || query.Get("limit") != "100" || query.Get("begin") == "" || query.Get("end") == "" {
			t.Fatalf("query = %v", query)
		}
		// 共 150 条，billId 从 1150 递减；第二页从 after 之后开始。
		start := 1150
		if after := query.Get("after"); after != "" {
			start, _ = strconv.Atoi(after)
			start--
		}
		var rows []string
		for id := start; id > 1000 && len(rows) < 100; id-- {
			kind := []string{"7", "8", "1"}[id%3]
			rows = append(rows, `{"billId":"`+strconv.Itoa(id)+`","type":"`+kind+`","subType":"1","ccy":"USDT","balChg":"-0.5","instId":"BTC-USDT-SWAP","ts":"`+strconv.Itoa(1700000000000+id)+`"}`)
		}
		ok(w, "["+strings.Join(rows, ",")+"]")
	}))
	defer server.Close()
	now := time.Now()
	page, err := swapAdapter(server.URL).ListLedger(context.Background(), exchange.LedgerQuery{Start: now.Add(-time.Hour), End: now, Limit: 120})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 120 || !page.HasMore || page.Entries[0].ID != "1150" || page.Entries[119].ID != "1031" {
		t.Fatalf("len=%d hasMore=%v first=%+v", len(page.Entries), page.HasMore, page.Entries[0])
	}
	if len(paths) != 2 || paths[0] != "/api/v5/account/bills" {
		t.Fatalf("paths = %v", paths)
	}
	for _, entry := range page.Entries[:3] {
		want := map[string]exchange.LedgerType{"7": exchange.LedgerInterest, "8": exchange.LedgerFundingFee, "1": exchange.LedgerTransfer}[strings.Split(entry.ExchangeType, "/")[0]]
		if entry.Type != want || entry.Amount.String() != "-0.5" || entry.Asset != "USDT" {
			t.Fatalf("entry = %+v", entry)
		}
	}
}

func TestBillsLedgerUsesArchiveForOlderWindow(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		path = request.URL.Path
		ok(w, `[]`)
	}))
	defer server.Close()
	now := time.Now()
	page, err := swapAdapter(server.URL).ListLedger(context.Background(), exchange.LedgerQuery{Start: now.Add(-30 * 24 * time.Hour), End: now, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/v5/account/bills-archive" || page.HasMore || len(page.Entries) != 0 {
		t.Fatalf("path=%s page=%+v", path, page)
	}
}

func TestSwapTradeBillSplitsFeeAndPnl(t *testing.T) {
	entries, err := billEntries(billPayload{BillID: "9", InstType: "SWAP", Type: "2", SubType: "1", Ccy: "USDT",
		BalChg: "1.48", Pnl: "1.5", Fee: "-0.02", InstID: "BTC-USDT-SWAP", Ts: "1700000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Type != exchange.LedgerRealizedPnL || entries[0].Amount.String() != "1.5" ||
		entries[1].Type != exchange.LedgerFee || entries[1].Amount.String() != "-0.02" || entries[0].ID == entries[1].ID {
		t.Fatalf("entries = %+v", entries)
	}
	spot, err := billEntries(billPayload{BillID: "10", InstType: "SPOT", Type: "2", Ccy: "BTC", BalChg: "0.01", Ts: "1700000000000"})
	if err != nil || len(spot) != 1 || spot[0].Type != exchange.LedgerTrade || spot[0].Amount.String() != "0.01" {
		t.Fatalf("spot = %+v, %v", spot, err)
	}
}

func TestBillsLedgerReturnsPartialWhenRateLimited(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		if calls > 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var rows []string
		for id := 200; id > 100; id-- {
			rows = append(rows, `{"billId":"`+strconv.Itoa(id)+`","type":"8","ccy":"USDT","balChg":"-0.1","ts":"`+strconv.Itoa(1700000000000+id)+`"}`)
		}
		ok(w, "["+strings.Join(rows, ",")+"]")
	}))
	defer server.Close()
	now := time.Now()
	page, err := swapAdapter(server.URL).ListLedger(context.Background(), exchange.LedgerQuery{Start: now.Add(-time.Hour), End: now, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 100 || !page.HasMore {
		t.Fatalf("len=%d hasMore=%v", len(page.Entries), page.HasMore)
	}
}

func TestBillsLedgerKeepsOnlyOwnMarketTradeBills(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ok(w, `[{"billId":"3","instType":"SPOT","type":"2","ccy":"BTC","balChg":"0.1","ts":"1700000000003"},
			{"billId":"2","instType":"SWAP","type":"8","ccy":"USDT","balChg":"-0.2","ts":"1700000000002"},
			{"billId":"1","instType":"","type":"1","ccy":"USDT","balChg":"100","ts":"1700000000001"}]`)
	}))
	defer server.Close()
	now := time.Now()
	page, err := swapAdapter(server.URL).ListLedger(context.Background(), exchange.LedgerQuery{Start: now.Add(-time.Hour), End: now, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Entries[0].ID != "2" || page.Entries[1].Type != exchange.LedgerTransfer {
		t.Fatalf("entries = %+v", page.Entries)
	}
}
