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
		return nil, fmt.Errorf("holding query: service is not configured")
	}
	account, err := q.Store.GetTradingAccount(ctx, spaceID, accountID)
	if err != nil {
		return nil, err
	}
	if account.MarketType != string(exchange.MarketTypeSpot) {
		return nil, fmt.Errorf("holding query: account is not SPOT")
	}
	var quoteSource execution.ReferencePriceSource
	var instrumentSource interface {
		LoadInstruments(context.Context) ([]exchange.Instrument, error)
	}
	var instruments []exchange.Instrument
	if q.Adapters != nil {
		if adapter, adapterErr := q.Adapters.Adapter(accountID); adapterErr == nil {
			quoteSource, _ = adapter.(execution.ReferencePriceSource)
			instrumentSource, _ = adapter.(interface {
				LoadInstruments(context.Context) ([]exchange.Instrument, error)
			})
			if instrumentSource != nil {
				instruments, err = instrumentSource.LoadInstruments(ctx)
			}
			if err != nil && account.ExecutionMode != string(exchange.ExecutionModePaper) {
				return nil, err
			}
		}
	}
	// A closed Paper session has no runtime adapter. Use the last persisted
	// instrument metadata so historical holdings remain queryable.
	if len(instruments) == 0 {
		records, recordErr := q.Store.ListInstrumentsForAccount(ctx, accountID)
		if recordErr != nil && account.ExecutionMode != string(exchange.ExecutionModePaper) {
			return nil, recordErr
		}
		for _, record := range records {
			instruments = append(instruments, exchange.Instrument{
				Exchange: exchange.Exchange(record.Exchange), MarketType: exchange.MarketType(record.MarketType),
				ExchangeSymbol: record.ExchangeSymbol, InstrumentID: record.InstrumentID,
				BaseAsset: record.BaseAsset, QuoteAsset: record.QuoteAsset, SettlementAsset: record.SettlementAsset,
			})
		}
	}
	byAsset := make(map[string]exchange.Instrument)
	for _, instrument := range instruments {
		if instrument.MarketType == exchange.MarketTypeSpot &&
			strings.EqualFold(instrument.QuoteAsset, account.SettlementAsset) {
			byAsset[strings.ToUpper(instrument.BaseAsset)] = instrument
		}
	}
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
		instrument, found := byAsset[strings.ToUpper(balance.Asset)]
		if !found {
			return nil, fmt.Errorf("holding query: no %s/%s Spot instrument", balance.Asset, account.SettlementAsset)
		}
		item.InstrumentID, item.ExchangeSymbol = instrument.InstrumentID, holdingQuoteSymbol(instrument)
		if account.ExecutionMode == string(exchange.ExecutionModePaper) {
			averageCost, costErr := q.paperAverageCost(ctx, spaceID, accountID, item.ExchangeSymbol)
			if costErr != nil {
				return nil, costErr
			}
			item.AverageCost = averageCost
		}
		if quoteSource != nil {
			quote, quoteErr := quoteSource.GetReferencePrice(ctx, holdingQuoteSymbol(instrument))
			if quoteErr != nil {
				return nil, quoteErr
			}
			item.MarkPrice = quote.Price
			item.MarketValue = quantity.Mul(quote.Price)
			item.SourceTime = quote.UpdatedAt
		} else if account.ExecutionMode == string(exchange.ExecutionModePaper) {
			// Closed Paper accounts have no live quote stream. Preserve a useful
			// historical mark from the latest fill when one exists; otherwise
			// return identity and quantity without fabricating a valuation.
			fills, _, fillErr := q.Store.ListFills(ctx, spaceID, store.FillQuery{
				TradingAccountID: accountID, ExchangeSymbol: holdingQuoteSymbol(instrument), Limit: 1,
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

func (q *HoldingQuery) paperAverageCost(ctx context.Context, spaceID, accountID, symbol string) (shared.Decimal, error) {
	const pageSize = 1000
	allFills := make([]store.FillRecord, 0, pageSize)
	for offset := 0; ; offset += pageSize {
		fills, total, err := q.Store.ListFills(ctx, spaceID, store.FillQuery{
			TradingAccountID: accountID, ExchangeSymbol: symbol, Offset: offset, Limit: pageSize,
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
