package paper

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// fundingLedgerStore 是能读模拟盘资金费记录的事实存储（*store.Store 实现）；测试替身没有实现时流水里没有资金费。
type fundingLedgerStore interface {
	ListPaperFundingFees(ctx context.Context, spaceID, tradingAccountID string, start, end int64, limit int) ([]store.PaperFundingFeeRecord, int64, error)
}

// LedgerSupported：模拟盘的流水由本地成交（手续费、已实现盈亏）和资金费记录生成，没有划转。
func (a *Adapter) LedgerSupported() bool { return true }

// ListLedger 返回 [Start, End] 内最新的至多 Limit 条流水，按时间倒序。每笔成交至多生成手续费和已实现盈亏两条，
// 每条资金费记录生成一条资金费；每条都带所属市场（现货、永续同名标的靠它区分）。查现货时没有资金费。
// 成交和资金费各取至多 Limit 条再合并：哪一路没取完，就只保留不早于这一路已取到的最老时间的流水，翻页时不会漏掉中间的记录。
func (a *Adapter) ListLedger(ctx context.Context, query exchange.LedgerQuery) (exchange.LedgerPage, error) {
	if a == nil || a.Store == nil {
		return exchange.LedgerPage{}, errors.New("paper adapter: store is not configured")
	}
	if query.Limit <= 0 || query.End.Before(query.Start) {
		return exchange.LedgerPage{}, errors.New("paper adapter: invalid ledger query")
	}
	fills, total, err := a.Store.ListFills(ctx, a.Account.SpaceID, store.FillQuery{
		TradingAccountID: a.Account.TradingAccountID, Market: string(query.Market),
		StartTime: query.Start.UnixMilli(), EndTime: query.End.UnixMilli(), Limit: query.Limit,
	})
	if err != nil {
		return exchange.LedgerPage{}, err
	}
	entries := make([]exchange.LedgerEntry, 0, len(fills))
	var cutoff time.Time
	truncated := false
	if total > int64(len(fills)) && len(fills) > 0 {
		truncated = true
		cutoff = time.UnixMilli(fills[len(fills)-1].TradedAt).UTC()
	}
	for _, fill := range fills {
		at := time.UnixMilli(fill.TradedAt).UTC()
		if fee, err := shared.ParseDecimal(fill.Fee); err == nil && !fee.IsZero() {
			entries = append(entries, exchange.LedgerEntry{
				ID: fill.FillID + ":fee", Time: at, Type: exchange.LedgerFee, ExchangeType: "PAPER_FEE",
				Asset: fill.FeeAsset, Amount: fee.Neg(), Market: exchange.MarketType(fill.MarketType),
				ExchangeSymbol: fill.ExchangeSymbol, TradeID: fill.ExchangeTradeID,
			})
		}
		if pnl, err := shared.ParseDecimal(fill.RealizedPnL); err == nil && !pnl.IsZero() {
			entries = append(entries, exchange.LedgerEntry{
				ID: fill.FillID + ":pnl", Time: at, Type: exchange.LedgerRealizedPnL, ExchangeType: "PAPER_REALIZED_PNL",
				Asset: fill.SettlementAsset, Amount: pnl, Market: exchange.MarketType(fill.MarketType),
				ExchangeSymbol: fill.ExchangeSymbol, TradeID: fill.ExchangeTradeID,
			})
		}
	}
	if source, ok := a.Store.(fundingLedgerStore); ok && (query.Market == exchange.MarketTypeUnspecified || query.Market == exchange.MarketTypeSwap) {
		fees, feeTotal, err := source.ListPaperFundingFees(ctx, a.Account.SpaceID, a.Account.TradingAccountID, query.Start.UnixMilli(), query.End.UnixMilli(), query.Limit)
		if err != nil {
			return exchange.LedgerPage{}, err
		}
		if feeTotal > int64(len(fees)) && len(fees) > 0 {
			truncated = true
			if oldest := time.UnixMilli(fees[len(fees)-1].SettledAt).UTC(); oldest.After(cutoff) {
				cutoff = oldest
			}
		}
		for _, fee := range fees {
			entry, err := fundingLedgerEntry(fee)
			if err != nil {
				return exchange.LedgerPage{}, err
			}
			entries = append(entries, entry)
		}
	}
	if !cutoff.IsZero() {
		kept := entries[:0]
		for _, entry := range entries {
			if !entry.Time.Before(cutoff) {
				kept = append(kept, entry)
			}
		}
		entries = kept
	}
	exchange.SortLedgerNewestFirst(entries)
	hasMore := truncated || len(entries) > query.Limit
	if len(entries) > query.Limit {
		entries = entries[:query.Limit]
	}
	return exchange.LedgerPage{Entries: entries, HasMore: hasMore}, nil
}

// fundingLedgerEntry 把一条资金费记录换成流水，说明里写上算金额用的费率、标记价格和持仓。
func fundingLedgerEntry(fee store.PaperFundingFeeRecord) (exchange.LedgerEntry, error) {
	amount, err := shared.ParseDecimal(fee.Amount)
	if err != nil {
		return exchange.LedgerEntry{}, fmt.Errorf("%w: paper funding amount", store.ErrInvalidRecord)
	}
	return exchange.LedgerEntry{
		ID:   fmt.Sprintf("funding:%s:%d", fee.ExchangeSymbol, fee.SettledAt),
		Time: time.UnixMilli(fee.SettledAt).UTC(), Type: exchange.LedgerFundingFee, ExchangeType: "PAPER_FUNDING_FEE",
		Asset: fee.Asset, Amount: amount, Market: exchange.MarketTypeSwap, ExchangeSymbol: fee.ExchangeSymbol,
		Info: fmt.Sprintf("费率 %s，标记价格 %s，持仓 %s", fee.FundingRate, fee.MarkPrice, fee.Quantity),
	}, nil
}
