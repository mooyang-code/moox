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
		if query.Get("instType") != "SWAP" || query.Get("limit") != "100" || query.Get("begin") == "" || query.Get("end") == "" {
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
			kind := []string{"2", "8", "1"}[id%3]
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
		want := map[string]exchange.LedgerType{"2": exchange.LedgerTrade, "8": exchange.LedgerFundingFee, "1": exchange.LedgerTransfer}[strings.Split(entry.ExchangeType, "/")[0]]
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
