package paper

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type ledgerFills struct {
	FactStore
	fills []store.FillRecord
	query store.FillQuery
}

func (s *ledgerFills) ListFills(_ context.Context, _ string, query store.FillQuery) ([]store.FillRecord, int64, error) {
	s.query = query
	out := s.fills
	if len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, int64(len(s.fills)), nil
}

func TestPaperLedgerFromFills(t *testing.T) {
	facts := &ledgerFills{fills: []store.FillRecord{
		{FillID: "f2", ExchangeTradeID: "t2", ExchangeSymbol: "BTCUSDT", Fee: "0.02", FeeAsset: "USDT", SettlementAsset: "USDT", RealizedPnL: "1.5", TradedAt: 2_000},
		{FillID: "f1", ExchangeTradeID: "t1", ExchangeSymbol: "BTCUSDT", Fee: "0.01", FeeAsset: "USDT", SettlementAsset: "USDT", RealizedPnL: "0", TradedAt: 1_000},
	}}
	adapter := &Adapter{Account: store.TradingAccountRecord{SpaceID: "s", TradingAccountID: "paper-1"}, Store: facts}
	page, err := adapter.ListLedger(context.Background(), exchange.LedgerQuery{Start: time.UnixMilli(0), End: time.UnixMilli(10_000), Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if facts.query.TradingAccountID != "paper-1" || facts.query.EndTime != 10_000 {
		t.Fatalf("query = %+v", facts.query)
	}
	// 两笔成交生成 3 条流水（第二笔没有盈亏），只要 2 条，标记还有更多。
	if len(page.Entries) != 2 || !page.HasMore {
		t.Fatalf("page = %+v", page)
	}
	if page.Entries[0].Type != exchange.LedgerRealizedPnL || page.Entries[0].Amount.String() != "1.5" ||
		page.Entries[1].Type != exchange.LedgerFee || page.Entries[1].Amount.String() != "-0.02" {
		t.Fatalf("entries = %+v", page.Entries)
	}
}
