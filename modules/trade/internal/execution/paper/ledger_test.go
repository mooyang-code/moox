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
		{FillID: "f2", ExchangeTradeID: "t2", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", Fee: "0.02", FeeAsset: "USDT", SettlementAsset: "USDT", RealizedPnL: "1.5", TradedAt: 2_000},
		{FillID: "f1", ExchangeTradeID: "t1", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", Fee: "0.01", FeeAsset: "USDT", SettlementAsset: "USDT", RealizedPnL: "0", TradedAt: 1_000},
	}}
	adapter := &Adapter{Account: store.TradingAccountRecord{SpaceID: "s", TradingAccountID: "paper-1"}, Store: facts}
	page, err := adapter.ListLedger(context.Background(), exchange.LedgerQuery{Market: exchange.MarketTypeSwap, Start: time.UnixMilli(0), End: time.UnixMilli(10_000), Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if facts.query.TradingAccountID != "paper-1" || facts.query.EndTime != 10_000 || facts.query.Market != "SWAP" {
		t.Fatalf("query = %+v", facts.query)
	}
	// 两笔成交生成 3 条流水（第二笔没有盈亏），只要 2 条，标记还有更多。
	if len(page.Entries) != 2 || !page.HasMore {
		t.Fatalf("page = %+v", page)
	}
	if page.Entries[0].Type != exchange.LedgerRealizedPnL || page.Entries[0].Amount.String() != "1.5" ||
		page.Entries[1].Type != exchange.LedgerFee || page.Entries[1].Amount.String() != "-0.02" ||
		page.Entries[0].Market != exchange.MarketTypeSwap || page.Entries[1].Market != exchange.MarketTypeSwap {
		t.Fatalf("entries = %+v", page.Entries)
	}
}

// ledgerFillsAndFunding 在成交之外还有资金费记录。
type ledgerFillsAndFunding struct {
	ledgerFills
	fees []store.PaperFundingFeeRecord
}

func (s *ledgerFillsAndFunding) ListPaperFundingFees(_ context.Context, _, _ string, start, end int64, limit int) ([]store.PaperFundingFeeRecord, int64, error) {
	var matched []store.PaperFundingFeeRecord
	for _, fee := range s.fees {
		if fee.SettledAt >= start && fee.SettledAt <= end {
			matched = append(matched, fee)
		}
	}
	total := int64(len(matched))
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, total, nil
}

func TestPaperLedgerIncludesFundingFees(t *testing.T) {
	facts := &ledgerFillsAndFunding{
		ledgerFills: ledgerFills{fills: []store.FillRecord{
			{FillID: "f1", ExchangeTradeID: "t1", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", Fee: "0.02", FeeAsset: "USDT", SettlementAsset: "USDT", RealizedPnL: "0", TradedAt: 2_000},
		}},
		fees: []store.PaperFundingFeeRecord{
			{ExchangeSymbol: "BTCUSDT", SettledAt: 3_000, FundingRate: "0.0001", MarkPrice: "50000", Quantity: "2", Amount: "-10", Asset: "USDT"},
			{ExchangeSymbol: "ETHUSDT", SettledAt: 1_000, FundingRate: "0.0002", MarkPrice: "2000", Quantity: "-3", Amount: "1.2", Asset: "USDT"},
		},
	}
	adapter := &Adapter{Account: store.TradingAccountRecord{SpaceID: "s", TradingAccountID: "paper-1"}, Store: facts}
	page, err := adapter.ListLedger(context.Background(), exchange.LedgerQuery{Market: exchange.MarketTypeSwap, Start: time.UnixMilli(0), End: time.UnixMilli(10_000), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.HasMore || len(page.Entries) != 3 {
		t.Fatalf("page = %+v", page)
	}
	first, last := page.Entries[0], page.Entries[2]
	if first.Type != exchange.LedgerFundingFee || first.Amount.String() != "-10" || first.Market != exchange.MarketTypeSwap ||
		first.ExchangeSymbol != "BTCUSDT" || first.ExchangeType != "PAPER_FUNDING_FEE" || first.Info != "费率 0.0001，标记价格 50000，持仓 2" {
		t.Fatalf("first = %+v", first)
	}
	if page.Entries[1].Type != exchange.LedgerFee || last.Type != exchange.LedgerFundingFee || last.Amount.String() != "1.2" || first.ID == last.ID {
		t.Fatalf("entries = %+v", page.Entries)
	}
	// 只查现货：没有资金费。
	spot, err := adapter.ListLedger(context.Background(), exchange.LedgerQuery{Market: exchange.MarketTypeSpot, Start: time.UnixMilli(0), End: time.UnixMilli(10_000), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range spot.Entries {
		if entry.Type == exchange.LedgerFundingFee {
			t.Fatalf("现货流水里出现了资金费：%+v", entry)
		}
	}
}

// 资金费没取完时，只保留不早于已取到的最老资金费的流水，翻页才不会漏掉中间的资金费。
func TestPaperLedgerCutsAtTheOldestFetchedFundingFee(t *testing.T) {
	facts := &ledgerFillsAndFunding{
		ledgerFills: ledgerFills{fills: []store.FillRecord{
			{FillID: "f1", ExchangeTradeID: "t1", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", Fee: "0.02", FeeAsset: "USDT", SettlementAsset: "USDT", RealizedPnL: "0", TradedAt: 1_000},
		}},
		fees: []store.PaperFundingFeeRecord{
			{ExchangeSymbol: "BTCUSDT", SettledAt: 5_000, Amount: "-1", Asset: "USDT"},
			{ExchangeSymbol: "BTCUSDT", SettledAt: 4_000, Amount: "-1", Asset: "USDT"},
			{ExchangeSymbol: "BTCUSDT", SettledAt: 3_000, Amount: "-1", Asset: "USDT"},
		},
	}
	adapter := &Adapter{Account: store.TradingAccountRecord{SpaceID: "s", TradingAccountID: "paper-1"}, Store: facts}
	page, err := adapter.ListLedger(context.Background(), exchange.LedgerQuery{Start: time.UnixMilli(0), End: time.UnixMilli(10_000), Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || len(page.Entries) != 2 || page.Entries[1].Time.UnixMilli() != 4_000 {
		t.Fatalf("page = %+v", page)
	}
}
