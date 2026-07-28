package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
)

var (
	ErrExecutorConfig  = errors.New("trade target: executor is not configured")
	ErrInvalidTarget   = errors.New("trade target: invalid target")
	ErrExecutionPaused = errors.New("trade target: execution is paused")
)

const (
	StatusRunning   = "RUNNING"
	StatusCompleted = "COMPLETED"
	StatusExpired   = "EXPIRED"
	StatusFailed    = "FAILED"
	StatusPaused    = "PAUSED"
)

type Quote struct {
	Price     shared.Decimal
	UpdatedAt time.Time
}

type PriceSource interface {
	LatestPrice(context.Context, string, string) (Quote, error)
}

type StateStore interface {
	LockTargetBinding(string, string) func()
	GetTargetExecutionByBinding(context.Context, string, string) (store.TargetExecutionRecord, error)
	GetExchangeAccountByID(context.Context, string) (store.ExchangeAccountRecord, error)
	GetInstrument(context.Context, string, string, string) (store.InstrumentRecord, error)
	GetPosition(context.Context, string, string, string, string) (store.PositionRecord, bool, error)
	ListOrdersForLane(context.Context, string, string, string) ([]store.OrderRecord, error)
	UpdateTargetExecutionState(context.Context, store.TargetExecutionRecord) (bool, error)
}

type OrderService interface {
	Place(context.Context, string, orderdomain.OrderSpec) (orderdomain.Order, error)
	Submit(context.Context, string, string) (orderdomain.Order, error)
	Cancel(context.Context, string, string) (orderdomain.Order, error)
	DiscardPending(context.Context, string, string) (orderdomain.Order, error)
	ResolveUnknown(context.Context, string, string) (orderdomain.Order, error)
}

type Executor struct {
	Store            StateStore
	Orders           OrderService
	Prices           PriceSource
	Now              func() time.Time
	MaxChildNotional shared.Decimal
}

type SymbolProgress struct {
	Target        string `json:"target"`
	Confirmed     string `json:"confirmed"`
	Effective     string `json:"effective"`
	Residual      string `json:"residual"`
	ActiveOrderID string `json:"active_order_id,omitempty"`
}

type Progress struct {
	Symbols map[string]SymbolProgress `json:"symbols"`
}

type Result struct {
	Status   string
	Progress Progress
}

type laneResult struct {
	progress SymbolProgress
	complete bool
	residual shared.Decimal
}

func (e *Executor) Converge(
	ctx context.Context,
	spaceID string,
	executionBindingID string,
) (Result, error) {
	if e == nil || e.Store == nil || e.Orders == nil || e.Prices == nil {
		return Result{}, ErrExecutorConfig
	}
	unlock := e.Store.LockTargetBinding(spaceID, executionBindingID)
	defer unlock()
	record, err := e.Store.GetTargetExecutionByBinding(ctx, spaceID, executionBindingID)
	if err != nil {
		return Result{}, err
	}
	now := e.now()
	progress := Progress{Symbols: make(map[string]SymbolProgress, len(record.Targets))}
	allComplete := true
	totalResidual := shared.Zero()
	for _, target := range record.Targets {
		result, convergeErr := e.convergeLane(ctx, record, target, now)
		if convergeErr != nil {
			if errors.Is(convergeErr, ErrExecutionPaused) {
				previousStatus := record.Status
				record.Status = StatusPaused
				record.LastError = convergeErr.Error()
				record.Progress = encodeProgress(progress)
				updated, updateErr := e.Store.UpdateTargetExecutionState(ctx, record)
				if updateErr != nil {
					return Result{}, updateErr
				}
				recordTargetTransition(previousStatus, StatusPaused, updated)
				return Result{Status: StatusPaused, Progress: progress}, nil
			}
			if errors.Is(convergeErr, ErrInvalidTarget) {
				previousStatus := record.Status
				record.Status = StatusFailed
				record.LastError = convergeErr.Error()
				record.Progress = encodeProgress(progress)
				updated, updateErr := e.Store.UpdateTargetExecutionState(ctx, record)
				if updateErr != nil {
					return Result{}, errors.Join(convergeErr, updateErr)
				}
				recordTargetTransition(previousStatus, StatusFailed, updated)
				return Result{Status: StatusFailed, Progress: progress}, convergeErr
			}
			return Result{Status: record.Status, Progress: progress}, convergeErr
		}
		progress.Symbols[target.Symbol] = result.progress
		allComplete = allComplete && result.complete
		totalResidual = totalResidual.Add(result.residual.Abs())
	}
	status := StatusRunning
	if allComplete {
		status = StatusCompleted
	}
	if now.UnixMilli() >= record.NotAfter && allComplete {
		status = StatusExpired
	}
	previousStatus := record.Status
	record.Status = status
	record.Progress = encodeProgress(progress)
	record.ResidualQuantity = totalResidual.String()
	record.LastError = ""
	updated, err := e.Store.UpdateTargetExecutionState(ctx, record)
	if err != nil {
		return Result{}, err
	}
	if !updated {
		return Result{}, nil
	}
	recordTargetTransition(previousStatus, status, true)
	return Result{Status: status, Progress: progress}, nil
}

func (e *Executor) convergeLane(
	ctx context.Context,
	execution store.TargetExecutionRecord,
	target store.TargetPosition,
	now time.Time,
) (laneResult, error) {
	desired, err := shared.ParseDecimal(target.TargetQuantity)
	if err != nil {
		return laneResult{}, fmt.Errorf("%w: target quantity for %s", ErrInvalidTarget, target.Symbol)
	}
	account, err := e.Store.GetExchangeAccountByID(ctx, execution.ExchangeAccountID)
	if err != nil {
		return laneResult{}, err
	}
	if account.SpaceID != execution.SpaceID {
		return laneResult{}, fmt.Errorf("%w: Exchange account ownership", ErrInvalidTarget)
	}
	instrument, err := e.Store.GetInstrument(
		ctx,
		account.Exchange,
		account.MarketType,
		target.Symbol,
	)
	if err != nil {
		return laneResult{}, err
	}
	if instrument.InstrumentID != target.InstrumentID {
		return laneResult{}, fmt.Errorf("%w: instrument identity for %s", ErrInvalidTarget, target.Symbol)
	}
	confirmed, err := e.confirmedQuantity(ctx, account, instrument)
	if err != nil {
		return laneResult{}, err
	}
	if account.MarketType == string(exchange.MarketTypeSpot) && desired.IsNegative() {
		return laneResult{}, fmt.Errorf("%w: SPOT target cannot be negative", ErrInvalidTarget)
	}
	orders, err := e.Store.ListOrdersForLane(
		ctx,
		execution.SpaceID,
		execution.ExchangeAccountID,
		target.Symbol,
	)
	if err != nil {
		return laneResult{}, err
	}

	if now.UnixMilli() >= execution.NotAfter {
		progress := SymbolProgress{
			Target: desired.String(), Confirmed: confirmed.String(),
			Effective: confirmed.String(), Residual: desired.Sub(confirmed).String(),
		}
		for _, current := range orders {
			if !activeOrder(current.State) ||
				current.StrategyExecutionID != execution.ExecutionID {
				continue
			}
			progress.ActiveOrderID = current.OrderID
			switch orderdomain.State(current.State) {
			case orderdomain.Pending:
				if _, err := e.Orders.DiscardPending(
					ctx,
					execution.SpaceID,
					current.OrderID,
				); err != nil {
					return laneResult{}, err
				}
			case orderdomain.Submitting, orderdomain.SubmitUnknown:
				if _, err := e.Orders.ResolveUnknown(
					ctx,
					execution.SpaceID,
					current.OrderID,
				); err != nil {
					return laneResult{}, err
				}
			}
			return laneResult{progress: progress}, nil
		}
		return laneResult{
			progress: progress, complete: true, residual: desired.Sub(confirmed),
		}, nil
	}

	effective := confirmed
	rawRemaining := desired.Sub(confirmed)
	expectedAction, expectedReduceOnly := childAction(
		exchange.MarketType(account.MarketType),
		confirmed,
		desired,
	)
	var compatible *store.OrderRecord
	conflicting := make([]store.OrderRecord, 0)
	for i := range orders {
		current := orders[i]
		if !activeOrder(current.State) {
			continue
		}
		remaining, parseErr := orderRemaining(current)
		if parseErr != nil {
			return laneResult{}, parseErr
		}
		if remaining.IsZero() {
			continue
		}
		delta := signedOrderQuantity(current.Side, remaining)
		if !rawRemaining.IsZero() &&
			sameSign(delta, expectedAction) &&
			delta.Abs().Cmp(expectedAction.Abs()) <= 0 &&
			current.ReduceOnly == expectedReduceOnly &&
			compatible == nil {
			effective = effective.Add(delta)
			compatible = &current
			continue
		}
		conflicting = append(conflicting, current)
	}
	progress := SymbolProgress{
		Target: desired.String(), Confirmed: confirmed.String(),
		Effective: effective.String(), Residual: desired.Sub(effective).String(),
	}
	if len(conflicting) > 0 {
		for _, current := range conflicting {
			if err := e.stopConflictingOrder(ctx, execution.SpaceID, current); err != nil {
				return laneResult{}, err
			}
			progress.ActiveOrderID = current.OrderID
		}
		return laneResult{progress: progress}, nil
	}
	if compatible != nil {
		progress.ActiveOrderID = compatible.OrderID
		switch orderdomain.State(compatible.State) {
		case orderdomain.Pending:
			if _, err := e.Orders.Submit(
				ctx,
				execution.SpaceID,
				compatible.OrderID,
			); err != nil {
				return laneResult{}, err
			}
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			if _, err := e.Orders.ResolveUnknown(
				ctx,
				execution.SpaceID,
				compatible.OrderID,
			); err != nil {
				return laneResult{}, err
			}
		}
		return laneResult{progress: progress}, nil
	}

	remaining := desired.Sub(confirmed)
	progress.Effective = confirmed.String()
	progress.Residual = remaining.String()
	if remaining.IsZero() {
		return laneResult{progress: progress, complete: true, residual: shared.Zero()}, nil
	}
	if account.Status != "ENABLED" || account.Paused || !account.Ready {
		return laneResult{}, ErrExecutionPaused
	}

	action, reduceOnly := expectedAction, expectedReduceOnly
	step, minimum, err := baseQuantityRules(instrument)
	if err != nil {
		return laneResult{}, err
	}
	quote, err := e.Prices.LatestPrice(
		ctx,
		execution.ExchangeAccountID,
		target.Symbol,
	)
	if err != nil {
		return laneResult{}, err
	}
	if quote.Price.Cmp(shared.Zero()) <= 0 || quote.UpdatedAt.IsZero() {
		return laneResult{}, fmt.Errorf("%w: reference price for %s", ErrInvalidTarget, target.Symbol)
	}
	childQuantity := floorToStep(action.Abs(), step)
	if childQuantity.IsZero() || childQuantity.Cmp(minimum) < 0 ||
		nonzeroBelowMinNotional(childQuantity, quote.Price, instrument.MinNotional) {
		return laneResult{progress: progress, complete: true, residual: remaining}, nil
	}
	if e.MaxChildNotional.Cmp(shared.Zero()) > 0 {
		capQuantity := floorToStep(e.MaxChildNotional.Div(quote.Price), step)
		if capQuantity.IsZero() || capQuantity.Cmp(minimum) < 0 ||
			nonzeroBelowMinNotional(capQuantity, quote.Price, instrument.MinNotional) {
			return laneResult{}, fmt.Errorf(
				"%w: max child notional is below Exchange minimum for %s",
				ErrInvalidTarget,
				target.Symbol,
			)
		}
		if capQuantity.Cmp(childQuantity) < 0 {
			childQuantity = capQuantity
		}
	}

	latest, err := e.Store.GetTargetExecutionByBinding(
		ctx,
		execution.SpaceID,
		execution.ExecutionBindingID,
	)
	if err != nil {
		return laneResult{}, err
	}
	if latest.ExecutionID != execution.ExecutionID ||
		latest.CommandSequence != execution.CommandSequence {
		return laneResult{progress: progress}, nil
	}
	spec := orderdomain.OrderSpec{
		ExchangeAccountID: execution.ExchangeAccountID,
		ClientOrderID: childClientOrderID(
			execution.ExecutionID,
			target.Symbol,
			execution.CommandSequence,
			countTargetOrders(orders)+1,
		),
		Symbol:              target.Symbol,
		OrderType:           exchange.OrderTypeMarket,
		TimeInForce:         exchange.TimeInForceUnspecified,
		Side:                sideForDelta(action),
		PositionSide:        exchange.PositionSideUnspecified,
		Quantity:            childQuantity,
		ReferencePrice:      quote.Price,
		ReferencePriceAt:    quote.UpdatedAt,
		ReduceOnly:          reduceOnly,
		Source:              "TARGET",
		StrategyExecutionID: execution.ExecutionID,
	}
	if account.MarketType == string(exchange.MarketTypeSwap) {
		spec.PositionSide = exchange.PositionSideNet
	}
	placed, err := e.Orders.Place(ctx, execution.SpaceID, spec)
	if err != nil {
		return laneResult{}, err
	}
	progress.ActiveOrderID = string(placed.ID)
	if _, err := e.Orders.Submit(ctx, execution.SpaceID, string(placed.ID)); err != nil {
		return laneResult{}, err
	}
	return laneResult{progress: progress}, nil
}

func (e *Executor) confirmedQuantity(
	ctx context.Context,
	account store.ExchangeAccountRecord,
	instrument store.InstrumentRecord,
) (shared.Decimal, error) {
	if account.MarketType == string(exchange.MarketTypeSpot) {
		for _, balance := range account.Snapshot.Balances {
			if balance.Asset == instrument.BaseAsset {
				return shared.ParseDecimal(balance.Total)
			}
		}
		return shared.Zero(), nil
	}
	position, found, err := e.Store.GetPosition(
		ctx,
		account.SpaceID,
		account.ExchangeAccountID,
		instrument.Symbol,
		string(exchange.PositionSideNet),
	)
	if err != nil || !found {
		return shared.Zero(), err
	}
	return shared.ParseDecimal(position.SignedQuantity)
}

func (e *Executor) stopConflictingOrder(
	ctx context.Context,
	spaceID string,
	current store.OrderRecord,
) error {
	switch orderdomain.State(current.State) {
	case orderdomain.Submitting, orderdomain.SubmitUnknown:
		_, err := e.Orders.ResolveUnknown(ctx, spaceID, current.OrderID)
		return err
	case orderdomain.Canceling, orderdomain.CancelUnknown:
		return nil
	case orderdomain.Pending:
		_, err := e.Orders.DiscardPending(ctx, spaceID, current.OrderID)
		return err
	default:
		_, err := e.Orders.Cancel(ctx, spaceID, current.OrderID)
		return err
	}
}

func childAction(
	market exchange.MarketType,
	confirmed shared.Decimal,
	desired shared.Decimal,
) (shared.Decimal, bool) {
	remaining := desired.Sub(confirmed)
	if market != exchange.MarketTypeSwap || confirmed.IsZero() {
		return remaining, false
	}
	if sameSign(confirmed, remaining) {
		return remaining, false
	}
	closeQuantity := remaining.Abs()
	if confirmed.Abs().Cmp(closeQuantity) < 0 {
		closeQuantity = confirmed.Abs()
	}
	if confirmed.Cmp(shared.Zero()) > 0 {
		return closeQuantity.Neg(), true
	}
	return closeQuantity, true
}

func baseQuantityRules(
	instrument store.InstrumentRecord,
) (shared.Decimal, shared.Decimal, error) {
	step, err := shared.ParseDecimal(instrument.ExchangeQuantityStep)
	if err != nil || step.Cmp(shared.Zero()) <= 0 {
		return shared.Decimal{}, shared.Decimal{}, fmt.Errorf("%w: quantity step", ErrInvalidTarget)
	}
	minimum, err := shared.ParseDecimal(instrument.MinExchangeQuantity)
	if err != nil || minimum.IsNegative() {
		return shared.Decimal{}, shared.Decimal{}, fmt.Errorf("%w: minimum quantity", ErrInvalidTarget)
	}
	if instrument.MarketType == string(exchange.MarketTypeSwap) {
		contractValue, parseErr := shared.ParseDecimal(instrument.ContractValue)
		if parseErr != nil || contractValue.Cmp(shared.Zero()) <= 0 {
			return shared.Decimal{}, shared.Decimal{}, fmt.Errorf("%w: contract value", ErrInvalidTarget)
		}
		step = step.Mul(contractValue)
		minimum = minimum.Mul(contractValue)
	}
	return step, minimum, nil
}

func floorToStep(value, step shared.Decimal) shared.Decimal {
	if value.Cmp(shared.Zero()) <= 0 || step.Cmp(shared.Zero()) <= 0 {
		return shared.Zero()
	}
	units := value.Div(step)
	integer := new(big.Int).Quo(unitsRat(units).Num(), unitsRat(units).Denom())
	raw, err := shared.ParseDecimal(integer.String())
	if err != nil {
		return shared.Zero()
	}
	return raw.Mul(step)
}

func unitsRat(value shared.Decimal) *big.Rat {
	rat := new(big.Rat)
	rat.SetString(value.String())
	return rat
}

func nonzeroBelowMinNotional(
	quantity shared.Decimal,
	price shared.Decimal,
	rawMinimum string,
) bool {
	minimum, err := shared.ParseDecimal(rawMinimum)
	return err == nil && minimum.Cmp(shared.Zero()) > 0 &&
		quantity.Mul(price).Cmp(minimum) < 0
}

func orderRemaining(record store.OrderRecord) (shared.Decimal, error) {
	quantity, err := shared.ParseDecimal(record.Quantity)
	if err != nil {
		return shared.Decimal{}, fmt.Errorf("trade target: corrupted order quantity")
	}
	filled, err := shared.ParseDecimal(record.FilledQuantity)
	if err != nil {
		return shared.Decimal{}, fmt.Errorf("trade target: corrupted filled quantity")
	}
	return quantity.Sub(filled), nil
}

func activeOrder(raw string) bool {
	return !orderdomain.State(raw).Terminal()
}

func signedOrderQuantity(side string, quantity shared.Decimal) shared.Decimal {
	if exchange.Side(side) == exchange.SideSell {
		return quantity.Neg()
	}
	return quantity
}

func sameSign(left, right shared.Decimal) bool {
	return !left.IsZero() && !right.IsZero() && left.IsNegative() == right.IsNegative()
}

func sideForDelta(delta shared.Decimal) exchange.Side {
	if delta.IsNegative() {
		return exchange.SideSell
	}
	return exchange.SideBuy
}

func countTargetOrders(orders []store.OrderRecord) int {
	count := 0
	for _, current := range orders {
		if current.Source == "TARGET" {
			count++
		}
	}
	return count
}

func childClientOrderID(
	executionID string,
	symbol string,
	commandSequence uint64,
	attempt int,
) string {
	sum := sha256.Sum256([]byte(
		executionID + "\x00" + symbol + "\x00" +
			strconv.FormatUint(commandSequence, 10) + "\x00" + strconv.Itoa(attempt),
	))
	return "mt-" + hex.EncodeToString(sum[:12])
}

func encodeProgress(progress Progress) string {
	data, err := json.Marshal(progress)
	if err != nil {
		return `{"symbols":{}}`
	}
	return string(data)
}

func recordTargetTransition(previous string, next string, updated bool) {
	if updated && previous != next {
		telemetry.TargetExecutions.WithLabelValues(strings.ToLower(next)).Inc()
	}
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

var _ OrderService = (*orderapp.Service)(nil)

func ParseProgress(raw string) (Progress, error) {
	if strings.TrimSpace(raw) == "" {
		return Progress{Symbols: map[string]SymbolProgress{}}, nil
	}
	var progress Progress
	if err := json.Unmarshal([]byte(raw), &progress); err != nil {
		return Progress{}, err
	}
	if progress.Symbols == nil {
		progress.Symbols = map[string]SymbolProgress{}
	}
	return progress, nil
}
