package consumer

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
)

type SourceKind string

const (
	SourcePrivateStream SourceKind = "private_stream"
	SourceRESTSync      SourceKind = "rest_sync"
)

type Source struct {
	SpaceID           string
	ExchangeAccountID string
	Kind              SourceKind
}

type Reducer struct {
	Store *store.Store
	Now   func() time.Time
}

func (r *Reducer) ConfirmCancel(
	ctx context.Context,
	spaceID string,
	orderID string,
) error {
	if r == nil || r.Store == nil || strings.TrimSpace(spaceID) == "" ||
		strings.TrimSpace(orderID) == "" {
		return errors.New("trade: invalid cancel confirmation")
	}
	return r.Store.Transaction(ctx, func(tx *store.Tx) error {
		record, err := tx.GetOrder(spaceID, orderID)
		if err != nil {
			return err
		}
		state := order.State(record.State)
		if state == order.Canceled || state == order.PartiallyCanceled {
			return nil
		}
		quantity, err := shared.ParseDecimal(record.Quantity)
		if err != nil {
			return fmt.Errorf("trade: corrupted order quantity")
		}
		filled, err := shared.ParseDecimal(record.FilledQuantity)
		if err != nil {
			return fmt.Errorf("trade: corrupted filled quantity")
		}
		aggregate := order.Order{
			ID:             shared.OrderID(record.OrderID),
			Spec:           order.OrderSpec{Quantity: quantity},
			FilledQuantity: filled, State: state, Version: record.Version,
		}
		expectedVersion := aggregate.Version
		if _, err := aggregate.ConfirmCancel(); err != nil {
			return err
		}
		remaining, err := shared.ParseDecimal(record.RemainingReservedQuantity)
		if err != nil {
			return fmt.Errorf("trade: corrupted remaining reservation")
		}
		record.State = string(aggregate.State)
		record.Version = aggregate.Version
		record.RemainingReservedQuantity = "0"
		record.FinishedAt = r.now().UnixMilli()
		if !remaining.IsZero() {
			if err := tx.PostLedger(store.LedgerTransactionRecord{
				SpaceID: record.SpaceID, TransactionID: "cancel-release:" + record.OrderID,
				ExchangeAccountID: record.ExchangeAccountID,
				TransactionType:   store.LedgerReservationRelease,
				SourceType:        "ORDER_CANCEL", SourceID: record.OrderID,
				Entries: []store.LedgerEntryRecord{
					ledgerEntry(record.ReservedAsset, "RESERVED", remaining.Neg()),
					ledgerEntry(record.ReservedAsset, "AVAILABLE", remaining),
				},
			}); err != nil {
				return err
			}
		}
		return tx.UpdateOrder(record, expectedVersion)
	})
}

func (r *Reducer) ApplyFill(
	ctx context.Context,
	fill exchange.Fill,
	source Source,
) (bool, error) {
	if err := validateFillInput(r, fill, source); err != nil {
		recordFillResult(source.Kind, false, err)
		return false, err
	}

	applied := false
	err := r.Store.Transaction(ctx, func(tx *store.Tx) error {
		record, err := tx.FindOrderForFill(
			source.SpaceID,
			source.ExchangeAccountID,
			fill.ClientOrderID,
			fill.ExchangeOrderID,
			fill.Symbol,
		)
		if err != nil {
			return err
		}
		if fill.ExchangeOrderID != "" && record.ExchangeOrderID == "" {
			record.ExchangeOrderID = fill.ExchangeOrderID
		}
		fillID := canonicalFillID(source.ExchangeAccountID, fill)
		inserted, err := tx.InsertFill(store.FillRecord{
			SpaceID: source.SpaceID, FillID: fillID,
			ExchangeTradeID: fill.ExchangeTradeID, OrderID: record.OrderID,
			ExchangeOrderID:   fill.ExchangeOrderID,
			ExchangeAccountID: source.ExchangeAccountID,
			Exchange:          record.Exchange, MarketType: record.MarketType,
			Symbol: fill.Symbol, Side: string(fill.Side),
			PositionSide: string(fill.PositionSide), Price: fill.Price.String(),
			Quantity: fill.Quantity.String(), Fee: fill.Fee.String(),
			FeeAsset: fill.FeeAsset, SettlementAsset: fill.SettlementAsset,
			RealizedPnL: fill.RealizedPnL.String(), Role: fill.LiquidityRole,
			TradedAt: fill.TradedAt.UnixMilli(),
		})
		if err != nil || !inserted {
			return err
		}

		instrument, err := tx.GetInstrument(record.Exchange, record.MarketType, record.Symbol)
		if err != nil {
			return err
		}
		expectedVersion := record.Version
		priceTick, err := shared.ParseDecimal(instrument.PriceTick)
		if err != nil {
			return fmt.Errorf("trade: corrupted instrument price tick")
		}
		if err := applyOrderFill(&record, fillID, fill, priceTick.Scale()); err != nil {
			return err
		}
		if order.State(record.State).Terminal() && record.FinishedAt == 0 {
			record.FinishedAt = fill.TradedAt.UnixMilli()
		}
		usedReservation, err := consumeReservation(&record, fill)
		if err != nil {
			return err
		}
		unusedReservation, err := takeUnusedReservation(&record)
		if err != nil {
			return err
		}
		if err := postFillLedger(
			tx, record, instrument, fillID, fill, usedReservation, unusedReservation,
		); err != nil {
			return err
		}
		if record.MarketType == string(exchange.MarketTypeSwap) {
			if err := projectSwapPosition(tx, record, fill, priceTick.Scale()); err != nil {
				return err
			}
		}
		if err := tx.UpdateOrder(record, expectedVersion); err != nil {
			return err
		}
		applied = true
		return nil
	})
	recordFillResult(source.Kind, applied, err)
	return applied, err
}

func validateFillInput(r *Reducer, fill exchange.Fill, source Source) error {
	if r == nil || r.Store == nil {
		return errors.New("trade: Fill reducer store is required")
	}
	if strings.TrimSpace(source.SpaceID) == "" ||
		strings.TrimSpace(source.ExchangeAccountID) == "" ||
		(source.Kind != SourcePrivateStream && source.Kind != SourceRESTSync) {
		return errors.New("trade: invalid Fill source")
	}
	if strings.TrimSpace(fill.ExchangeTradeID) == "" ||
		strings.TrimSpace(fill.Symbol) == "" ||
		(strings.TrimSpace(fill.ClientOrderID) == "" &&
			strings.TrimSpace(fill.ExchangeOrderID) == "") ||
		!fill.Side.Valid() ||
		fill.Quantity.Cmp(shared.Zero()) <= 0 ||
		fill.Price.Cmp(shared.Zero()) <= 0 ||
		fill.Fee.IsNegative() {
		return errors.New("trade: incomplete normalized Fill")
	}
	if fill.TradedAt.IsZero() || fill.TradedAt.UnixMilli() <= 0 {
		return errors.New("trade: Fill traded time is required")
	}
	if fill.Fee.Cmp(shared.Zero()) > 0 && strings.TrimSpace(fill.FeeAsset) == "" {
		return errors.New("trade: Fill fee asset is required")
	}
	return nil
}

func canonicalFillID(accountID string, fill exchange.Fill) string {
	return accountID + ":" + fill.Symbol + ":" + fill.ExchangeTradeID
}

func applyOrderFill(
	record *store.OrderRecord,
	fillID string,
	fill exchange.Fill,
	priceScale int,
) error {
	quantity, err := shared.ParseDecimal(record.Quantity)
	if err != nil || quantity.Cmp(shared.Zero()) <= 0 {
		return fmt.Errorf("trade: corrupted order quantity")
	}
	filled, err := shared.ParseDecimal(record.FilledQuantity)
	if err != nil {
		return fmt.Errorf("trade: corrupted filled quantity")
	}
	averagePrice, err := shared.ParseDecimal(record.AveragePrice)
	if err != nil {
		return fmt.Errorf("trade: corrupted average price")
	}
	aggregate := order.Order{
		ID: shared.OrderID(record.OrderID),
		Spec: order.OrderSpec{
			Quantity: quantity,
		},
		FilledQuantity:   filled,
		AverageFillPrice: averagePrice,
		AppliedFills:     make(map[shared.FillID]shared.Decimal),
		State:            order.State(record.State),
		Version:          record.Version,
	}
	if _, err := aggregate.ApplyFill(order.Fill{
		ID: shared.FillID(fillID), Quantity: fill.Quantity,
	}); err != nil {
		return err
	}
	aggregate.AverageFillPrice, err = divideRounded(
		averagePrice.Mul(filled).Add(fill.Price.Mul(fill.Quantity)),
		aggregate.FilledQuantity,
		priceScale,
	)
	if err != nil {
		return err
	}
	record.FilledQuantity = aggregate.FilledQuantity.String()
	record.AveragePrice = aggregate.AverageFillPrice.String()
	record.State = string(aggregate.State)
	record.Version = aggregate.Version
	return nil
}

func consumeReservation(
	record *store.OrderRecord,
	fill exchange.Fill,
) (shared.Decimal, error) {
	remaining, err := shared.ParseDecimal(record.RemainingReservedQuantity)
	if err != nil {
		return shared.Decimal{}, fmt.Errorf("trade: corrupted remaining reservation")
	}
	reserved, err := shared.ParseDecimal(record.ReservedQuantity)
	if err != nil {
		return shared.Decimal{}, fmt.Errorf("trade: corrupted reservation")
	}
	orderQuantity, err := shared.ParseDecimal(record.Quantity)
	if err != nil || orderQuantity.IsZero() {
		return shared.Decimal{}, fmt.Errorf("trade: corrupted order quantity")
	}

	used := fill.Quantity
	switch exchange.MarketType(record.MarketType) {
	case exchange.MarketTypeSpot:
		if fill.Side == exchange.SideBuy {
			used = fill.Quantity.Mul(fill.Price)
		}
	case exchange.MarketTypeSwap:
		if record.ReduceOnly {
			used = shared.Zero()
		} else {
			used = reserved.Mul(fill.Quantity).Div(orderQuantity)
		}
	default:
		return shared.Decimal{}, errors.New("trade: unsupported Fill market type")
	}
	if used.Cmp(remaining) > 0 {
		used = remaining
	}
	record.RemainingReservedQuantity = remaining.Sub(used).String()
	return used, nil
}

func postFillLedger(
	tx *store.Tx,
	record store.OrderRecord,
	instrument store.InstrumentRecord,
	fillID string,
	fill exchange.Fill,
	usedReservation shared.Decimal,
	unusedReservation shared.Decimal,
) error {
	entries := make([]store.LedgerEntryRecord, 0, 8)
	if record.MarketType == string(exchange.MarketTypeSpot) {
		amount := fill.Quantity.Mul(fill.Price)
		if fill.Side == exchange.SideBuy {
			entries = append(entries, settlementDebitEntries(
				instrument.QuoteAsset, amount, usedReservation,
			)...)
			entries = append(entries,
				ledgerEntry(instrument.QuoteAsset, "CLEARING", amount),
				ledgerEntry(instrument.BaseAsset, "CLEARING", fill.Quantity.Neg()),
				ledgerEntry(instrument.BaseAsset, "AVAILABLE", fill.Quantity),
			)
		} else {
			entries = append(entries, settlementDebitEntries(
				instrument.BaseAsset, fill.Quantity, usedReservation,
			)...)
			entries = append(entries,
				ledgerEntry(instrument.BaseAsset, "CLEARING", fill.Quantity),
				ledgerEntry(instrument.QuoteAsset, "CLEARING", amount.Neg()),
				ledgerEntry(instrument.QuoteAsset, "AVAILABLE", amount),
			)
		}
	} else if !record.ReduceOnly && !usedReservation.IsZero() {
		entries = append(entries,
			ledgerEntry(record.ReservedAsset, "RESERVED", usedReservation.Neg()),
			ledgerEntry(record.ReservedAsset, "AVAILABLE", usedReservation),
		)
	}
	if !fill.RealizedPnL.IsZero() {
		asset := fill.SettlementAsset
		if asset == "" {
			asset = instrument.SettlementAsset
		}
		entries = append(entries,
			ledgerEntry(asset, "CLEARING", fill.RealizedPnL.Neg()),
			ledgerEntry(asset, "AVAILABLE", fill.RealizedPnL),
		)
	}
	if !fill.Fee.IsZero() {
		entries = append(entries,
			ledgerEntry(fill.FeeAsset, "AVAILABLE", fill.Fee.Neg()),
			ledgerEntry(fill.FeeAsset, "FEES", fill.Fee),
		)
	}
	if !unusedReservation.IsZero() {
		entries = append(entries,
			ledgerEntry(record.ReservedAsset, "RESERVED", unusedReservation.Neg()),
			ledgerEntry(record.ReservedAsset, "AVAILABLE", unusedReservation),
		)
	}
	if len(entries) == 0 {
		return nil
	}
	return tx.PostLedger(store.LedgerTransactionRecord{
		SpaceID: record.SpaceID, TransactionID: "fill:" + fillID,
		ExchangeAccountID: record.ExchangeAccountID,
		TransactionType:   store.LedgerFillSettlement,
		SourceType:        "fill", SourceID: fillID, Entries: entries,
	})
}

func ledgerEntry(asset string, bucket string, amount shared.Decimal) store.LedgerEntryRecord {
	return store.LedgerEntryRecord{Asset: asset, Bucket: bucket, Amount: amount}
}

func settlementDebitEntries(
	asset string,
	total shared.Decimal,
	fromFrozen shared.Decimal,
) []store.LedgerEntryRecord {
	entries := make([]store.LedgerEntryRecord, 0, 2)
	if !fromFrozen.IsZero() {
		entries = append(entries, ledgerEntry(asset, "RESERVED", fromFrozen.Neg()))
	}
	if remainder := total.Sub(fromFrozen); !remainder.IsZero() {
		entries = append(entries, ledgerEntry(asset, "AVAILABLE", remainder.Neg()))
	}
	return entries
}

func takeUnusedReservation(record *store.OrderRecord) (shared.Decimal, error) {
	if record.State != string(order.Filled) {
		return shared.Zero(), nil
	}
	remaining, err := shared.ParseDecimal(record.RemainingReservedQuantity)
	if err != nil {
		return shared.Decimal{}, fmt.Errorf("trade: corrupted remaining reservation")
	}
	record.RemainingReservedQuantity = shared.Zero().String()
	return remaining, nil
}

func projectSwapPosition(
	tx *store.Tx,
	record store.OrderRecord,
	fill exchange.Fill,
	priceScale int,
) error {
	positionRecord, found, err := tx.GetPosition(
		record.SpaceID,
		record.ExchangeAccountID,
		record.Symbol,
		string(exchange.PositionSideNet),
	)
	if err != nil {
		return err
	}
	if !found {
		account, err := tx.GetExchangeAccount(record.SpaceID, record.ExchangeAccountID)
		if err != nil {
			return err
		}
		leverage := account.LeverageSettings[record.Symbol]
		if leverage == "" {
			return errors.New("trade: missing leverage for estimated Position")
		}
		positionRecord = store.PositionRecord{
			SpaceID: record.SpaceID, ExchangeAccountID: record.ExchangeAccountID,
			Symbol: record.Symbol, PositionSide: string(exchange.PositionSideNet),
			SignedQuantity: "0", EntryPrice: "0", MarkPrice: "0",
			Leverage: leverage, MarginMode: account.MarginMode,
			UsedMargin: "0", LiquidationPrice: "0",
			UnrealizedPnL: "0", RealizedPnL: "0",
		}
	}
	current, err := shared.ParseDecimal(positionRecord.SignedQuantity)
	if err != nil {
		return fmt.Errorf("trade: corrupted Position quantity")
	}
	entryPrice, err := shared.ParseDecimal(positionRecord.EntryPrice)
	if err != nil {
		return fmt.Errorf("trade: corrupted Position entry price")
	}
	realizedPnL, err := shared.ParseDecimal(positionRecord.RealizedPnL)
	if err != nil {
		return fmt.Errorf("trade: corrupted Position realized PnL")
	}
	delta := fill.Quantity
	if fill.Side == exchange.SideSell {
		delta = delta.Neg()
	}
	next := current.Add(delta)
	estimatedEntry, err := estimatedEntryPrice(
		current, entryPrice, delta, fill.Price, next, priceScale,
	)
	if err != nil {
		return err
	}
	positionRecord.EntryPrice = estimatedEntry.String()
	positionRecord.SignedQuantity = next.String()
	positionRecord.MarkPrice = fill.Price.String()
	positionRecord.RealizedPnL = realizedPnL.Add(fill.RealizedPnL).String()
	if fill.TradedAt.UnixMilli() > positionRecord.ExchangeUpdatedAt {
		positionRecord.ExchangeUpdatedAt = fill.TradedAt.UnixMilli()
	}
	return tx.UpsertPosition(positionRecord)
}

func estimatedEntryPrice(
	current shared.Decimal,
	currentEntry shared.Decimal,
	delta shared.Decimal,
	fillPrice shared.Decimal,
	next shared.Decimal,
	priceScale int,
) (shared.Decimal, error) {
	if next.IsZero() {
		return shared.Zero(), nil
	}
	if current.IsZero() || current.IsNegative() != next.IsNegative() {
		return fillPrice, nil
	}
	if current.IsNegative() != delta.IsNegative() {
		return currentEntry, nil
	}
	return divideRounded(
		current.Abs().Mul(currentEntry).Add(delta.Abs().Mul(fillPrice)),
		next.Abs(),
		priceScale,
	)
}

func divideRounded(
	numerator shared.Decimal,
	denominator shared.Decimal,
	scale int,
) (shared.Decimal, error) {
	if denominator.Cmp(shared.Zero()) <= 0 || scale < 0 {
		return shared.Decimal{}, errors.New("trade: invalid decimal division")
	}
	n := new(big.Rat)
	d := new(big.Rat)
	if _, ok := n.SetString(numerator.String()); !ok {
		return shared.Decimal{}, errors.New("trade: invalid decimal numerator")
	}
	if _, ok := d.SetString(denominator.String()); !ok {
		return shared.Decimal{}, errors.New("trade: invalid decimal denominator")
	}
	value := new(big.Rat).Quo(n, d)
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	scaledNumerator := new(big.Int).Mul(value.Num(), factor)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(scaledNumerator, value.Denom(), remainder)
	if new(big.Int).Mul(remainder, big.NewInt(2)).Cmp(value.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	raw := quotient.String()
	if scale > 0 {
		for len(raw) <= scale {
			raw = "0" + raw
		}
		raw = raw[:len(raw)-scale] + "." + raw[len(raw)-scale:]
		raw = strings.TrimRight(strings.TrimRight(raw, "0"), ".")
	}
	return shared.ParseDecimal(raw)
}

func recordFillResult(source SourceKind, applied bool, err error) {
	result := "duplicate"
	if applied {
		result = "applied"
	}
	if err != nil {
		result = "error"
	}
	telemetry.Fills.WithLabelValues(string(source), result).Inc()
}

func (r *Reducer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
