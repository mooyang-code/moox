package rpc

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	holdingapp "github.com/mooyang-code/moox/modules/trade/internal/application/holding"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// HoldingQuery is the production read path for Spot holdings. It derives
// non-settlement market value from the same public instrument and quote
// adapter used by execution; the console never fabricates a value from raw
// quantity when a mark is unavailable.
type HoldingQuery struct {
	Store    *store.Store
	Adapters interface {
		Adapter(string) (execution.ExecutionAdapter, error)
	}
}

func (q *HoldingQuery) List(ctx context.Context, spaceID, accountID string) ([]holdingapp.Holding, error) {
	if q == nil || q.Store == nil || q.Adapters == nil {
		return nil, errors.New("持币查询服务未配置")
	}
	account, err := q.Store.GetTradingAccount(ctx, spaceID, accountID)
	if err != nil {
		return nil, err
	}
	var quoteSource execution.ReferencePriceSource
	if adapter, adapterErr := q.Adapters.Adapter(accountID); adapterErr == nil {
		quoteSource = adapter
	}
	records, err := q.Store.ListInstrumentsForAccount(ctx, accountID)
	if err != nil && account.ExecutionMode != string(exchange.ExecutionModePaper) {
		return nil, err
	}
	index := holdingInstrumentIndex(records, account.SettlementAsset)
	paper := account.ExecutionMode == string(exchange.ExecutionModePaper)
	// 只看放着现货的钱包：普通账户合约钱包里的币是保证金，不是持币。
	balances := account.SpotBalances()
	result := make([]holdingapp.Holding, 0, len(balances))
	// 第一遍只解析持币和标的，记下要估值的行（在 result 里的下标）和标的，第二遍再逐只估值。
	valued := make([]int, 0, len(balances))
	keys := make([]exchange.SymbolKey, 0, len(balances))
	for _, balance := range balances {
		quantity, err := shared.ParseDecimal(balance.Total)
		if err != nil || quantity.IsZero() {
			continue
		}
		item := holdingapp.Holding{
			TradingAccountID: accountID, Asset: balance.Asset, Quantity: quantity,
			SourceTime: unixMillis(account.Snapshot.ExchangeUpdatedAt),
		}
		if available, parseErr := shared.ParseDecimal(balance.Available); parseErr == nil {
			item.Sellable = available
		}
		if strings.EqualFold(balance.Asset, account.SettlementAsset) {
			item.MarkPrice = shared.MustDecimal("1")
			item.MarketValue = quantity
		} else if instrument, found := balanceInstrument(index, account.Markets, balance.Asset); found {
			item.InstrumentID, item.ExchangeSymbol = instrument.InstrumentID, instrument.ExchangeSymbol
			valued = append(valued, len(result))
			keys = append(keys, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
		}
		// 找不到标的、无法估值的资产也要展示，价格留空，页面显示“-”。
		result = append(result, item)
	}
	// 批量预取报价（A 股腾讯快照一次 60 只），之后逐只取价命中缓存；来源不支持预取时什么都不做。
	execution.PrefetchQuotes(ctx, quoteSource, keys)
	for i, at := range valued {
		item, key := &result[at], keys[i]
		if paper {
			averageCost, costErr := q.paperAverageCost(ctx, spaceID, accountID, key)
			if costErr != nil {
				return nil, costErr
			}
			item.AverageCost = averageCost
		}
		if quoteSource != nil {
			if quote, quoteErr := quoteSource.GetReferencePrice(ctx, key); quoteErr == nil {
				item.MarkPrice = quote.Price
				item.MarketValue = item.Quantity.Mul(quote.Price)
				item.SourceTime = quote.UpdatedAt
			}
		} else if paper {
			// Closed Paper accounts have no live quote stream. Preserve a useful
			// historical mark from the latest fill when one exists; otherwise
			// return identity and quantity without fabricating a valuation.
			fills, _, fillErr := q.Store.ListFills(ctx, spaceID, store.FillQuery{
				TradingAccountID: accountID, Market: string(key.Market), ExchangeSymbol: key.Symbol, Limit: 1,
			})
			if fillErr != nil {
				return nil, fillErr
			}
			if len(fills) > 0 {
				if price, parseErr := shared.ParseDecimal(fills[0].Price); parseErr == nil {
					item.MarkPrice, item.MarketValue = price, item.Quantity.Mul(price)
					item.SourceTime = time.UnixMilli(fills[0].TradedAt).UTC()
				}
			}
		}
		if paper && !item.AverageCost.IsZero() && !item.MarkPrice.IsZero() {
			pnl := item.MarkPrice.Sub(item.AverageCost).Mul(item.Quantity)
			item.UnrealizedPnL = &pnl
		}
	}
	return result, nil
}

// holdingKey 是（市场，基础资产）：同一个币的现货和永续是两个标的，余额只能对应持仓来自余额的市场。
type holdingKey struct {
	market exchange.MarketType
	asset  string
}

// holdingInstrumentIndex 按（市场，基础资产）建标的索引，只收计价币等于结算资产的标的。
func holdingInstrumentIndex(records []store.InstrumentRecord, settlementAsset string) map[holdingKey]exchange.Instrument {
	index := make(map[holdingKey]exchange.Instrument)
	for _, record := range records {
		if !strings.EqualFold(record.QuoteAsset, settlementAsset) {
			continue
		}
		market := exchange.MarketType(record.MarketType)
		index[holdingKey{market: market, asset: strings.ToUpper(record.BaseAsset)}] = exchange.Instrument{
			Exchange: exchange.Exchange(record.Exchange), MarketType: market,
			ExchangeSymbol: record.ExchangeSymbol, InstrumentID: record.InstrumentID,
			BaseAsset: record.BaseAsset, QuoteAsset: record.QuoteAsset, SettlementAsset: record.SettlementAsset,
		}
	}
	return index
}

// balanceInstrument 找余额资产对应的标的：只在账户开通的、持仓来自余额的市场里找，不会配上永续标的。
func balanceInstrument(index map[holdingKey]exchange.Instrument, markets []string, asset string) (exchange.Instrument, bool) {
	for _, market := range markets {
		if !exchange.RuleFor(market).PositionFromBalance {
			continue
		}
		if instrument, found := index[holdingKey{market: exchange.MarketType(market), asset: strings.ToUpper(asset)}]; found {
			return instrument, true
		}
	}
	return exchange.Instrument{}, false
}

// paperAverageCost 一次读完这个标的的全部成交（按时间升序，不数总数、不分页）算均价。
func (q *HoldingQuery) paperAverageCost(ctx context.Context, spaceID, accountID string, key exchange.SymbolKey) (shared.Decimal, error) {
	fills, err := q.Store.ListSymbolFillsAscending(ctx, spaceID, accountID, key)
	if err != nil {
		return shared.Zero(), err
	}
	return paperAverageCostFromFills(fills), nil
}

func paperAverageCostFromFills(fills []store.FillRecord) shared.Decimal {
	sort.SliceStable(fills, func(i, j int) bool { return fills[i].TradedAt < fills[j].TradedAt })
	quantity := shared.Zero()
	cost := shared.Zero()
	for _, fill := range fills {
		fillQuantity, quantityErr := shared.ParseDecimal(fill.Quantity)
		fillPrice, priceErr := shared.ParseDecimal(fill.Price)
		if quantityErr != nil || priceErr != nil || fillQuantity.IsZero() {
			continue
		}
		if strings.EqualFold(fill.Side, string(exchange.SideBuy)) {
			quantity = quantity.Add(fillQuantity)
			cost = cost.Add(fillQuantity.Mul(fillPrice))
			continue
		}
		if !strings.EqualFold(fill.Side, string(exchange.SideSell)) || quantity.IsZero() {
			continue
		}
		reduce := fillQuantity
		if reduce.Cmp(quantity) > 0 {
			reduce = quantity
		}
		average := cost.Div(quantity)
		quantity = quantity.Sub(reduce)
		cost = cost.Sub(average.Mul(reduce))
	}
	if quantity.IsZero() {
		return shared.Zero()
	}
	return cost.Div(quantity)
}

func unixMillis(value int64) (resultTime time.Time) {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
