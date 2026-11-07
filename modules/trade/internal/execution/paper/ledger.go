package paper

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// LedgerSupported：模拟盘由本地成交生成流水（手续费、已实现盈亏），没有资金费和划转。
func (a *Adapter) LedgerSupported() bool { return true }

// ListLedger 返回 [Start, End] 内最新的至多 Limit 条流水，按时间倒序。每笔成交至多生成手续费和已实现盈亏两条，
// 每条都带成交所属的市场（现货、永续同名标的靠它区分）。
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
	exchange.SortLedgerNewestFirst(entries)
	hasMore := total > int64(len(fills)) || len(entries) > query.Limit
	if len(entries) > query.Limit {
		entries = entries[:query.Limit]
	}
	return exchange.LedgerPage{Entries: entries, HasMore: hasMore}, nil
}
