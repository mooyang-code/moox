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

type FillOrigin string

var ErrDuplicateFill = errors.New("trade: duplicate fill")

const (
	OriginPrivateSocket FillOrigin = "private_stream"
	OriginRESTSnapshot  FillOrigin = "rest_sync"
	OriginPaperMatcher  FillOrigin = "paper_matcher"
)

type Source struct {
	SpaceID          string
	TradingAccountID string
	Kind             FillOrigin
}

type Reducer struct {
	Store   *store.Store
	Now     func() time.Time
	Enqueue func(string)
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
			ID: shared.OrderID(record.OrderID),
			Spec: order.OrderSpec{
				ClientOrderSpec: order.ClientOrderSpec{Quantity: quantity},
			},
			FilledQuantity: filled, State: state, Version: record.Version,
		}
		expectedVersion := aggregate.Version
		if _, err := aggregate.ConfirmCancel(); err != nil {
			return err
		}
		record.State = string(aggregate.State)
		record.Version = aggregate.Version
		record.RemainingReservedQuantity = "0"
		record.FinishedAt = r.now().UnixMilli()
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
	duplicate := false
	err := r.Store.Transaction(ctx, func(tx *store.Tx) error {
		record, err := tx.FindOrderForFill(
			source.SpaceID,
			source.TradingAccountID,
			fill.ClientOrderID,
			fill.ExchangeOrderID,
			fill.Symbol,
		)
		if err != nil {
			return err
		}
		if err := r.applyFillTx(ctx, tx, record, fill, source); err != nil {
			if errors.Is(err, ErrDuplicateFill) {
				duplicate = true
				return nil
			}
			return err
		}
		applied = !duplicate
		return nil
	})
	recordFillResult(source.Kind, applied, err)
	if err == nil && applied && r.Enqueue != nil {
		r.Enqueue(source.TradingAccountID)
	}
	return applied, err
}

// ApplyFillToOrderTx is the transaction-aware entry point used by the paper
// matcher. The matcher owns the SQLite transaction and therefore must not
// call ApplyFill, which would attempt to open a nested transaction on the
// single-connection store.
func (r *Reducer) ApplyFillToOrderTx(
	ctx context.Context,
	tx *store.Tx,
	record store.OrderRecord,
	fill exchange.Fill,
	source Source,
) error {
	if err := validateFillInput(r, fill, source); err != nil {
		return err
	}
	return r.applyFillTx(ctx, tx, record, fill, source)
}

func (r *Reducer) applyFillTx(
	_ context.Context,
	tx *store.Tx,
	record store.OrderRecord,
	fill exchange.Fill,
	source Source,
) error {
	if tx == nil {
		return errors.New("trade: Fill reducer transaction is required")
	}
	if fill.ExchangeOrderID != "" && record.ExchangeOrderID == "" {
		record.ExchangeOrderID = fill.ExchangeOrderID
	}
	fillID := canonicalFillID(source.TradingAccountID, fill)
	inserted, err := tx.InsertFill(store.FillRecord{
		SpaceID: source.SpaceID, FillID: fillID,
		ExchangeTradeID: fill.ExchangeTradeID, OrderID: record.OrderID,
		ExchangeOrderID:  fill.ExchangeOrderID,
		TradingAccountID: source.TradingAccountID,
		Exchange:         record.Exchange, MarketType: record.MarketType,
		InstrumentID: record.InstrumentID, ExchangeSymbol: record.ExchangeSymbol,
		Symbol: fill.Symbol, Side: string(fill.Side),
		PositionSide: string(fill.PositionSide), Price: fill.Price.String(),
		Quantity: fill.Quantity.String(), Fee: fill.Fee.String(),
		FeeAsset: fill.FeeAsset, SettlementAsset: fill.SettlementAsset,
		RealizedPnL: fill.RealizedPnL.String(), Role: fill.LiquidityRole,
		TradedAt: fill.TradedAt.UnixMilli(),
	})
	if err != nil || !inserted {
		if err == nil && !inserted {
			return ErrDuplicateFill
		}
		return err
	}
	instrument, err := tx.GetInstrumentByIDForAccount(source.SpaceID, source.TradingAccountID, record.InstrumentID)
	if err != nil {
		return err
	}
	priceTick, err := shared.ParseDecimal(instrument.PriceTick)
	if err != nil {
		return fmt.Errorf("trade: corrupted instrument price tick")
	}
	expectedVersion := record.Version
	if source.Kind == OriginPaperMatcher {
		executionPrice := fill.Price.String()
		record.PaperExecutionPrice = &executionPrice
	}
	if err := applyOrderFill(&record, fillID, fill, priceTick.Scale()); err != nil {
		return err
	}
	if order.State(record.State).Terminal() && record.FinishedAt == 0 {
		record.FinishedAt = fill.TradedAt.UnixMilli()
	}
	if _, err := consumeReservation(&record, fill); err != nil {
		return err
	}
	if _, err := takeUnusedReservation(&record); err != nil {
		return err
	}
	if record.MarketType == string(exchange.MarketTypeSwap) {
		if err := projectSwapPosition(tx, record, fill, priceTick.Scale()); err != nil {
			return err
		}
	}
	return tx.UpdateOrder(record, expectedVersion)
}

func validateFillInput(r *Reducer, fill exchange.Fill, source Source) error {
	if r == nil || r.Store == nil {
		return errors.New("trade: Fill reducer store is required")
	}
	if strings.TrimSpace(source.SpaceID) == "" ||
		strings.TrimSpace(source.TradingAccountID) == "" ||
		(source.Kind != OriginPrivateSocket && source.Kind != OriginRESTSnapshot && source.Kind != OriginPaperMatcher) {
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
			ClientOrderSpec: order.ClientOrderSpec{Quantity: quantity},
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
	exchangeSymbol := record.ExchangeSymbol
	if exchangeSymbol == "" {
		exchangeSymbol = record.Symbol
	}
	positionRecord, found, err := tx.GetPosition(
		record.SpaceID, record.TradingAccountID, exchangeSymbol, string(exchange.PositionSideNet),
	)
	if err != nil {
		return err
	}
	if !found {
		account, err := tx.GetTradingAccount(record.SpaceID, record.TradingAccountID)
		if err != nil {
			return err
		}
		leverage := account.LeverageSettings[record.InstrumentID]
		if leverage == "" {
			leverage = account.LeverageSettings[exchangeSymbol]
		}
		if leverage == "" {
			leverage = account.LeverageSettings[record.Symbol]
		}
		if leverage == "" {
			return errors.New("trade: missing leverage for estimated Position")
		}
		positionRecord = store.PositionRecord{
			SpaceID: record.SpaceID, TradingAccountID: record.TradingAccountID,
			InstrumentID: record.InstrumentID, ExchangeSymbol: exchangeSymbol, Symbol: exchangeSymbol,
			PositionSide:   string(exchange.PositionSideNet),
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

func recordFillResult(source FillOrigin, applied bool, err error) {
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
