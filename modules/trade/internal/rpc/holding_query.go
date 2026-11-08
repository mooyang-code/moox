package rpc

import (
	"context"
	"fmt"
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
		return nil, fmt.Errorf("持有者查询服务未配置")
	}
	account, err := q.Store.GetTradingAccount(ctx, spaceID, accountID)
	if err != nil {
		return nil, err
	}
	if !account.HasBalanceMarket() {
		return nil, fmt.Errorf("holding query: account is not SPOT")
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
	result := make([]holdingapp.Holding, 0, len(account.Snapshot.Balances))
	for _, balance := range account.Snapshot.Balances {
		quantity, err := shared.ParseDecimal(balance.Total)
		if err != nil || quantity.IsZero() {
			continue
		}
		item := holdingapp.Holding{
			TradingAccountID: accountID, Asset: balance.Asset, Quantity: quantity,
			SourceTime: unixMillis(account.Snapshot.ExchangeUpdatedAt),
		}
		if strings.EqualFold(balance.Asset, account.SettlementAsset) {
			item.MarkPrice = shared.MustDecimal("1")
			item.MarketValue = quantity
			result = append(result, item)
			continue
		}
		instrument, found := balanceInstrument(index, account.Markets, balance.Asset)
		if !found {
			// 无法估值的资产也要展示，价格留空，页面显示“-”。
			result = append(result, item)
			continue
		}
		item.InstrumentID, item.ExchangeSymbol = instrument.InstrumentID, holdingQuoteSymbol(instrument)
		if account.ExecutionMode == string(exchange.ExecutionModePaper) {
			averageCost, costErr := q.paperAverageCost(ctx, spaceID, accountID, exchange.NewSymbolKey(instrument.MarketType, item.ExchangeSymbol))
			if costErr != nil {
				return nil, costErr
			}
			item.AverageCost = averageCost
		}
		if quoteSource != nil {
			if quote, quoteErr := quoteSource.GetReferencePrice(ctx, exchange.NewSymbolKey(instrument.MarketType, holdingQuoteSymbol(instrument))); quoteErr == nil {
				item.MarkPrice = quote.Price
				item.MarketValue = quantity.Mul(quote.Price)
				item.SourceTime = quote.UpdatedAt
			}
		} else if account.ExecutionMode == string(exchange.ExecutionModePaper) {
			// Closed Paper accounts have no live quote stream. Preserve a useful
			// historical mark from the latest fill when one exists; otherwise
			// return identity and quantity without fabricating a valuation.
			fills, _, fillErr := q.Store.ListFills(ctx, spaceID, store.FillQuery{
				TradingAccountID: accountID, Market: string(instrument.MarketType), ExchangeSymbol: holdingQuoteSymbol(instrument), Limit: 1,
			})
			if fillErr != nil {
				return nil, fillErr
			}
			if len(fills) > 0 {
				if price, parseErr := shared.ParseDecimal(fills[0].Price); parseErr == nil {
					item.MarkPrice, item.MarketValue = price, quantity.Mul(price)
					item.SourceTime = time.UnixMilli(fills[0].TradedAt).UTC()
				}
			}
		}
		if account.ExecutionMode == string(exchange.ExecutionModePaper) && !item.AverageCost.IsZero() && !item.MarkPrice.IsZero() {
			pnl := item.MarkPrice.Sub(item.AverageCost).Mul(quantity)
			item.UnrealizedPnL = &pnl
		}
		result = append(result, item)
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

func (q *HoldingQuery) paperAverageCost(ctx context.Context, spaceID, accountID string, key exchange.SymbolKey) (shared.Decimal, error) {
	const pageSize = 1000
	allFills := make([]store.FillRecord, 0, pageSize)
	for offset := 0; ; offset += pageSize {
		fills, total, err := q.Store.ListFills(ctx, spaceID, store.FillQuery{
			TradingAccountID: accountID, Market: string(key.Market), ExchangeSymbol: key.Symbol, Offset: offset, Limit: pageSize,
		})
		if err != nil {
			return shared.Zero(), err
		}
		allFills = append(allFills, fills...)
		if len(fills) == 0 || int64(offset+len(fills)) >= total {
			break
		}
	}
	return paperAverageCostFromFills(allFills), nil
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

func holdingQuoteSymbol(instrument exchange.Instrument) string {
	return instrument.ExchangeSymbol
}

func unixMillis(value int64) (resultTime time.Time) {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
