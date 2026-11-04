package binance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// incomeServer 模拟 /fapi/v1/income：窗口内按时间升序返回最早的至多 limit 条。
func incomeServer(t *testing.T, rows []incomePayload) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/fapi/v1/income" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		calls++
		query := request.URL.Query()
		from, _ := strconv.ParseInt(query.Get("startTime"), 10, 64)
		to, _ := strconv.ParseInt(query.Get("endTime"), 10, 64)
		limit, _ := strconv.Atoi(query.Get("limit"))
		var out []incomePayload
		for _, row := range rows {
			if row.Time >= from && row.Time <= to && len(out) < limit {
				out = append(out, row)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func TestIncomeLedgerMapsTypesNewestFirst(t *testing.T) {
	base := time.UnixMilli(1_700_000_000_000)
	rows := []incomePayload{
		{Symbol: "BTCUSDT", IncomeType: "COMMISSION", Income: "-0.01", Asset: "USDT", Time: base.UnixMilli(), TranID: "1", TradeID: "7"},
		{Symbol: "BTCUSDT", IncomeType: "REALIZED_PNL", Income: "2.5", Asset: "USDT", Time: base.UnixMilli(), TranID: "1", TradeID: "7"},
		{Symbol: "BTCUSDT", IncomeType: "FUNDING_FEE", Income: "-0.3", Asset: "USDT", Time: base.Add(time.Minute).UnixMilli(), TranID: "2"},
		{IncomeType: "TRANSFER", Income: "100", Asset: "USDT", Time: base.Add(2 * time.Minute).UnixMilli(), TranID: "3", Info: "TRANSFER"},
	}
	server, _ := incomeServer(t, rows)
	page, err := testAdapter(exchange.MarketTypeSwap, server.URL).ListLedger(context.Background(), exchange.LedgerQuery{
		Start: base.Add(-time.Hour), End: base.Add(time.Hour), Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.HasMore || len(page.Entries) != 4 {
		t.Fatalf("page = %+v", page)
	}
	want := []exchange.LedgerType{exchange.LedgerTransfer, exchange.LedgerFundingFee, exchange.LedgerRealizedPnL, exchange.LedgerFee}
	for index, entry := range page.Entries {
		if entry.Type != want[index] {
			t.Fatalf("entry %d = %+v, want %s", index, entry, want[index])
		}
	}
	if page.Entries[2].ID == page.Entries[3].ID || page.Entries[3].Amount.String() != "-0.01" {
		t.Fatalf("entries = %+v", page.Entries)
	}
}

func TestIncomeLedgerWalksBackFromEndWhenDense(t *testing.T) {
	// 3 小时内每秒 1 条，共 10800 条：只要最新的 50 条，且要标记还有更早的。
	end := time.UnixMilli(1_700_000_000_000)
	var rows []incomePayload
	for second := 0; second < 3*3600; second++ {
		at := end.Add(-time.Duration(second) * time.Second)
		rows = append([]incomePayload{{IncomeType: "COMMISSION", Income: "-0.01", Asset: "USDT", Time: at.UnixMilli(), TranID: json.Number(strconv.Itoa(second))}}, rows...)
	}
	server, calls := incomeServer(t, rows)
	page, err := testAdapter(exchange.MarketTypeSwap, server.URL).ListLedger(context.Background(), exchange.LedgerQuery{
		Start: end.Add(-3 * time.Hour), End: end, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || len(page.Entries) != 50 || !page.Entries[0].Time.Equal(end) ||
		!page.Entries[49].Time.Equal(end.Add(-49*time.Second)) {
		t.Fatalf("hasMore=%v len=%d first=%v last=%v", page.HasMore, len(page.Entries), page.Entries[0].Time, page.Entries[len(page.Entries)-1].Time)
	}
	if *calls > incomeMaxRequests {
		t.Fatalf("calls = %d", *calls)
	}
}

func TestSpotLedgerUnsupported(t *testing.T) {
	adapter := testAdapter(exchange.MarketTypeSpot, "http://unused")
	if adapter.LedgerSupported() {
		t.Fatal("spot must not claim ledger support")
	}
	if _, err := adapter.ListLedger(context.Background(), exchange.LedgerQuery{Start: time.Now().Add(-time.Hour), End: time.Now(), Limit: 1}); err == nil {
		t.Fatal("spot ledger must fail")
	}
}

func TestIncomeLedgerSparseLongRangeTakesOneRequest(t *testing.T) {
	end := time.UnixMilli(1_700_000_000_000)
	var rows []incomePayload
	for day := 89; day >= 0; day-- {
		at := end.Add(-time.Duration(day) * 24 * time.Hour)
		rows = append(rows, incomePayload{IncomeType: "FUNDING_FEE", Income: "-0.1", Asset: "USDT", Time: at.UnixMilli(), TranID: json.Number(strconv.Itoa(day))})
	}
	server, calls := incomeServer(t, rows)
	page, err := testAdapter(exchange.MarketTypeSwap, server.URL).ListLedger(context.Background(), exchange.LedgerQuery{
		Start: end.Add(-90 * 24 * time.Hour), End: end, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || page.HasMore || len(page.Entries) != 90 {
		t.Fatalf("calls=%d hasMore=%v len=%d", *calls, page.HasMore, len(page.Entries))
	}
}
