// Package position 提供组合账户与执行账户的实际持仓统一查询，
// 把永续合约持仓（t_trading_positions）与余额类市场持币（现货、股票快照余额）收成同一种行。
package position

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// ErrNotLogicalMember 是同时传了组合与执行账户、但执行账户不属于该组合时的参数错误。
var ErrNotLogicalMember = errors.New("交易账户不是该组合账户的成员")

// AdapterSource 按执行账户取适配器，用于余额类市场查询实时参考价。
type AdapterSource interface {
	Adapter(tradingAccountID string) (execution.ExecutionAdapter, error)
}

// ListRequest 按组合账户或执行账户查询实际持仓。
type ListRequest struct {
	SpaceID          string
	LogicalAccountID string
	TradingAccountID string
	InstrumentID     string
}

// Position 是统一后的实际持仓行：永续合约与现货/股票余额共用同一结构。
type Position struct {
	MarketType        string
	TradingAccountID  string
	InstrumentID      string
	ExchangeSymbol    string
	Asset             string
	PositionSide      string
	SignedQuantity    string
	EntryPrice        string
	MarkPrice         string
	MarketValue       string
	Leverage          string
	MarginMode        string
	UsedMargin        string
	LiquidationPrice  string
	UnrealizedPnL     *string
	RealizedPnL       string
	Sellable          string
	ExchangeUpdatedAt int64
	UpdatedAt         int64
}

// Query 查询组合或执行账户的实际持仓：永续读库内持仓表，余额类市场结合余额快照与实时参考价（或模拟盘最后成交价）估值。
// 列表页的批量持仓计数仍走 store.BatchLogicalAccountSummaries，两边共用 exchange.CoinOf 按币归一。
type Query struct {
	Store    *store.Store
	Adapters AdapterSource
}

// List 返回请求范围内各执行账户的实际持仓（永续非零持仓 + 余额类市场非零持币）。
func (q *Query) List(ctx context.Context, req ListRequest) ([]Position, error) {
	if q == nil || q.Store == nil {
		return nil, errors.New("持仓查询服务未配置")
	}
	st := q.Store.ReadOnly()
	accounts, err := resolveAccounts(ctx, st, req)
	if err != nil {
		return nil, err
	}
	instrumentID, err := normalizeQueryInstrument(ctx, st, req)
	if err != nil {
		return nil, err
	}
	result := make([]Position, 0)
	for _, account := range accounts {
		if account.HasPositionMarket() {
			swapRows, listErr := listSwapPositions(ctx, st, req.SpaceID, account.TradingAccountID, instrumentID)
			if listErr != nil {
				return nil, listErr
			}
			result = append(result, swapRows...)
		}
		if account.HasBalanceMarket() {
			balanceRows, listErr := q.listBalancePositions(ctx, st, account, instrumentID)
			if listErr != nil {
				return nil, listErr
			}
			result = append(result, balanceRows...)
		}
	}
	return result, nil
}

func resolveAccounts(ctx context.Context, st *store.Store, req ListRequest) ([]store.TradingAccountRecord, error) {
	if req.LogicalAccountID == "" {
		account, err := st.GetTradingAccount(ctx, req.SpaceID, req.TradingAccountID)
		if err != nil {
			return nil, err
		}
		return []store.TradingAccountRecord{account}, nil
	}
	if _, err := st.GetLogicalAccount(ctx, req.SpaceID, req.LogicalAccountID); err != nil {
		return nil, err
	}
	// 按组合查询时包含停用成员，确保停用成员仍持有的永续仓位与现货余额可见；
	// 显式指定了成员执行账户时同样校验其属于该组合。
	members, err := st.ListLogicalAccountMembers(ctx, req.SpaceID, req.LogicalAccountID, true)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(members))
	for _, member := range members {
		if req.TradingAccountID != "" && member.TradingAccountID != req.TradingAccountID {
			continue
		}
		ids = append(ids, member.TradingAccountID)
	}
	if req.TradingAccountID != "" && len(ids) == 0 {
		return nil, fmt.Errorf("%w：交易账户 %s，组合账户 %s", ErrNotLogicalMember, req.TradingAccountID, req.LogicalAccountID)
	}
	accounts := make([]store.TradingAccountRecord, 0, len(ids))
	for _, id := range ids {
		account, err := st.GetTradingAccount(ctx, req.SpaceID, id)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, nil
}

func normalizeQueryInstrument(ctx context.Context, st *store.Store, req ListRequest) (string, error) {
	if req.InstrumentID == "" {
		return "", nil
	}
	if req.TradingAccountID != "" {
		account, err := st.GetTradingAccount(ctx, req.SpaceID, req.TradingAccountID)
		if err != nil {
			return "", err
		}
		return normalizeForMarkets(req.InstrumentID, account.Markets)
	}
	markets, err := st.LogicalAccountMarkets(ctx, req.SpaceID, req.LogicalAccountID)
	if err != nil {
		return "", err
	}
	return normalizeForMarkets(req.InstrumentID, markets)
}

func normalizeForMarkets(raw string, markets []string) (string, error) {
	switch len(markets) {
	case 0:
		return "", fmt.Errorf("%w: %q 没有可交易的市场", exchange.ErrInvalidInstrumentID, raw)
	case 1:
		return exchange.NormalizeInstrumentID(raw, exchange.MarketType(markets[0]))
	}
	for _, market := range markets {
		if id, err := exchange.NormalizeInstrumentID(raw, exchange.MarketType(market)); err == nil &&
			strings.HasSuffix(strings.ToUpper(strings.TrimSpace(raw)), "-"+exchange.InstrumentSuffix(market)) {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: 开通了多个市场 %v，标的 %q 必须带市场后缀", exchange.ErrInvalidInstrumentID, markets, raw)
}

func listSwapPositions(
	ctx context.Context,
	st *store.Store,
	spaceID string,
	accountID string,
	instrumentID string,
) ([]Position, error) {
	records, err := st.ListPositionsByInstrument(ctx, spaceID, accountID, instrumentID)
	if err != nil {
		return nil, err
	}
	rows := make([]Position, 0, len(records))
	for _, record := range records {
		qty, parseErr := shared.ParseDecimal(record.SignedQuantity)
		if parseErr == nil && qty.IsZero() {
			continue
		}
		marketValue := ""
		if parseErr == nil {
			if mark, markErr := shared.ParseDecimal(record.MarkPrice); markErr == nil && mark.Cmp(shared.Zero()) > 0 {
				marketValue = qty.Abs().Mul(mark).String()
			}
		}
		var pnl *string
		if strings.TrimSpace(record.UnrealizedPnL) != "" {
			value := record.UnrealizedPnL
			pnl = &value
		}
		rows = append(rows, Position{
			MarketType:        record.MarketType,
			TradingAccountID:  record.TradingAccountID,
			InstrumentID:      record.InstrumentID,
			ExchangeSymbol:    record.ExchangeSymbol,
			Asset:             exchange.CoinOf(record.InstrumentID),
			PositionSide:      record.PositionSide,
			SignedQuantity:    record.SignedQuantity,
			EntryPrice:        record.EntryPrice,
			MarkPrice:         record.MarkPrice,
			MarketValue:       marketValue,
			Leverage:          record.Leverage,
			MarginMode:        record.MarginMode,
			UsedMargin:        record.UsedMargin,
			LiquidationPrice:  record.LiquidationPrice,
			UnrealizedPnL:     pnl,
			RealizedPnL:       record.RealizedPnL,
			ExchangeUpdatedAt: record.ExchangeUpdatedAt,
			UpdatedAt:         unixMilli(record.UpdatedAt),
		})
	}
	return rows, nil
}

func (q *Query) listBalancePositions(
	ctx context.Context,
	st *store.Store,
	account store.TradingAccountRecord,
	instrumentFilter string,
) ([]Position, error) {
	var quoteSource execution.ReferencePriceSource
	if q.Adapters != nil {
		if adapter, adapterErr := q.Adapters.Adapter(account.TradingAccountID); adapterErr == nil {
			quoteSource = adapter
		}
	}
	records, err := st.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil && account.ExecutionMode != string(exchange.ExecutionModePaper) {
		return nil, err
	}
	index := balanceInstrumentIndex(records, account.SettlementAsset)
	paper := account.ExecutionMode == string(exchange.ExecutionModePaper)
	defaultMarket := balanceMarketOf(account.Markets)
	// 只看放着现货/股票的钱包：普通账户合约钱包里的币是保证金，不是持币。
	balances := account.SpotBalances()
	result := make([]Position, 0, len(balances))
	valued := make([]int, 0, len(balances))
	valuedQty := make([]shared.Decimal, 0, len(balances))
	keys := make([]exchange.SymbolKey, 0, len(balances))
	for _, balance := range balances {
		quantity, parseErr := shared.ParseDecimal(balance.Total)
		if parseErr != nil || quantity.IsZero() {
			continue
		}
		item := Position{
			MarketType:        defaultMarket,
			TradingAccountID:  account.TradingAccountID,
			Asset:             balance.Asset,
			SignedQuantity:    quantity.String(),
			ExchangeUpdatedAt: account.Snapshot.ExchangeUpdatedAt,
			UpdatedAt:         unixMilli(account.UpdatedAt),
		}
		if available, availErr := shared.ParseDecimal(balance.Available); availErr == nil {
			item.Sellable = available.String()
		}
		var matched exchange.Instrument
		var found bool
		if strings.EqualFold(balance.Asset, account.SettlementAsset) {
			item.MarkPrice = "1"
			item.MarketValue = quantity.String()
		} else if matched, found = balanceInstrument(index, account.Markets, balance.Asset); found {
			item.MarketType = string(matched.MarketType)
			item.InstrumentID = matched.InstrumentID
			item.ExchangeSymbol = matched.ExchangeSymbol
		}
		if instrumentFilter != "" && item.InstrumentID != instrumentFilter {
			continue
		}
		if found {
			valued = append(valued, len(result))
			valuedQty = append(valuedQty, quantity)
			keys = append(keys, exchange.NewSymbolKey(matched.MarketType, matched.ExchangeSymbol))
		}
		// 找不到标的、无法估值的资产也要展示（未指定 instrumentFilter 时），价格留空，页面显示“-”。
		result = append(result, item)
	}
	// 批量预取报价（A 股腾讯快照一次 60 只），之后逐只取价命中缓存；来源不支持预取时什么都不做。
	execution.PrefetchQuotes(ctx, quoteSource, keys)
	for i, at := range valued {
		item, quantity, key := &result[at], valuedQty[i], keys[i]
		var summary store.InstrumentSummaryRecord
		var traded bool
		var avgCost shared.Decimal
		if paper {
			var summaryErr error
			summary, traded, summaryErr = st.GetInstrumentSummary(ctx, account.SpaceID, account.TradingAccountID, key)
			if summaryErr != nil {
				return nil, summaryErr
			}
			avgCost = summary.AverageCost()
			if !avgCost.IsZero() {
				item.EntryPrice = avgCost.String()
			}
		}
		var markPrice shared.Decimal
		if quoteSource != nil {
			if quote, quoteErr := quoteSource.GetReferencePrice(ctx, key); quoteErr == nil {
				markPrice = quote.Price
				item.MarkPrice = quote.Price.String()
				item.MarketValue = quantity.Mul(quote.Price).String()
				item.ExchangeUpdatedAt = quote.UpdatedAt.UnixMilli()
			}
		} else if paper && traded {
			// 已关闭的模拟账户没有行情：有成交过时用标的汇总里的最近成交价估值，没有就只给标的和数量，不编造估值。
			markPrice = summary.LastPrice
			item.MarkPrice = summary.LastPrice.String()
			item.MarketValue = quantity.Mul(summary.LastPrice).String()
			item.ExchangeUpdatedAt = summary.LastTradedAt
		}
		if paper && !avgCost.IsZero() && !markPrice.IsZero() {
			pnl := markPrice.Sub(avgCost).Mul(quantity).String()
			item.UnrealizedPnL = &pnl
		}
	}
	return result, nil
}

type balanceKey struct {
	market exchange.MarketType
	asset  string
}

func balanceInstrumentIndex(records []store.InstrumentRecord, settlementAsset string) map[balanceKey]exchange.Instrument {
	index := make(map[balanceKey]exchange.Instrument)
	for _, record := range records {
		if !strings.EqualFold(record.QuoteAsset, settlementAsset) {
			continue
		}
		market := exchange.MarketType(record.MarketType)
		index[balanceKey{market: market, asset: strings.ToUpper(record.BaseAsset)}] = exchange.Instrument{
			Exchange: exchange.Exchange(record.Exchange), MarketType: market,
			ExchangeSymbol: record.ExchangeSymbol, InstrumentID: record.InstrumentID,
			BaseAsset: record.BaseAsset, QuoteAsset: record.QuoteAsset, SettlementAsset: record.SettlementAsset,
		}
	}
	return index
}

func balanceInstrument(index map[balanceKey]exchange.Instrument, markets []string, asset string) (exchange.Instrument, bool) {
	for _, market := range markets {
		if !exchange.RuleFor(market).PositionFromBalance {
			continue
		}
		if instrument, found := index[balanceKey{market: exchange.MarketType(market), asset: strings.ToUpper(asset)}]; found {
			return instrument, true
		}
	}
	return exchange.Instrument{}, false
}

func balanceMarketOf(markets []string) string {
	for _, market := range markets {
		if exchange.RuleFor(market).PositionFromBalance {
			return market
		}
	}
	return ""
}

func unixMilli(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}
