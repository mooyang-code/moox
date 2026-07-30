package target

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/rs/xid"
)

var (
	ErrExecutorConfig = errors.New("trade target: executor is not configured")
	ErrInvalidTarget  = errors.New("trade target: invalid target")
)

const (
	StatusPending    = "PENDING"
	StatusConverging = "CONVERGING"
	StatusConverged  = "CONVERGED"
	StatusBlocked    = "BLOCKED"
	StatusPaused     = "PAUSED"
)

type Quote struct {
	Price     shared.Decimal
	UpdatedAt time.Time
}

type PriceSource interface {
	LatestPrice(context.Context, string, string) (Quote, error)
}

type OrderService interface {
	Place(context.Context, string, orderdomain.OrderSpec) (orderdomain.Order, error)
	Submit(context.Context, string, string) (orderdomain.Order, error)
	Cancel(context.Context, string, string) (orderdomain.Order, error)
	DiscardPending(context.Context, string, string) (orderdomain.Order, error)
	ResolveUnknown(context.Context, string, string) (orderdomain.Order, error)
}

type Executor struct {
	Store            *store.Store
	Orders           OrderService
	Prices           PriceSource
	Now              func() time.Time
	MaxChildNotional shared.Decimal
}

type Result struct {
	Status string
	Action string
}

type memberState struct {
	member      store.LogicalAccountMemberRecord
	account     store.ExchangeAccountRecord
	instruments map[string]store.InstrumentRecord
	positions   map[string]shared.Decimal
	blocked     []store.BlockedTarget
}

type laneAction struct {
	member     memberState
	instrument store.InstrumentRecord
	delta      shared.Decimal
	reducing   bool
}

func (e *Executor) Converge(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
) (Result, error) {
	if e == nil || e.Store == nil || e.Orders == nil || e.Prices == nil {
		return Result{}, ErrExecutorConfig
	}
	unlock := e.Store.LockLogicalAccount(spaceID, logicalAccountID)
	defer unlock()

	logicalAccount, err := e.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	if err != nil {
		return Result{}, err
	}
	target, err := e.Store.GetLogicalAccountTarget(ctx, spaceID, logicalAccountID)
	if err != nil {
		return Result{}, err
	}
	if logicalAccount.AutomationState != "ACTIVE" {
		orders, _, listErr := e.Store.ListOrders(
			ctx,
			spaceID,
			store.OrderQuery{
				LogicalAccountID: logicalAccountID,
				OnlyOpen:         true,
				Limit:            1000,
			},
		)
		if listErr != nil {
			return Result{}, listErr
		}
		for _, current := range orders {
			if current.OwnerType != string(orderdomain.OwnerTarget) {
				continue
			}
			if err := e.stopOrder(ctx, current); err != nil {
				return Result{}, err
			}
			target.LastError = logicalAccount.PauseReason
			if err := e.updateTarget(ctx, &target, StatusPending); err != nil {
				return Result{}, err
			}
			return Result{Status: StatusPaused, Action: "cancel"}, nil
		}
		return Result{Status: StatusPaused}, nil
	}
	if logicalAccount.OwnerRunnerID != target.RunnerID {
		return Result{}, fmt.Errorf("%w: target runner no longer owns logical account", ErrInvalidTarget)
	}

	members, err := e.loadMembers(ctx, spaceID, logicalAccountID)
	if err != nil {
		return Result{}, err
	}
	for _, member := range members {
		if member.account.Status != "ENABLED" || !member.account.Ready {
			target.LastError = "member " + member.account.ExchangeAccountID + " is not ready"
			if err := e.updateTarget(ctx, &target, StatusPending); err != nil {
				return Result{}, err
			}
			return Result{Status: StatusPaused}, nil
		}
	}

	orders, err := e.activeOrders(ctx, spaceID, members)
	if err != nil {
		return Result{}, err
	}
	for _, current := range orders {
		switch {
		case current.OwnerType == string(orderdomain.OwnerExternal),
			current.OwnerType == string(orderdomain.OwnerOperator),
			current.OwnerType == string(orderdomain.OwnerTarget) &&
				current.RunnerID != target.RunnerID:
			reason := fmt.Sprintf(
				"%s order %s conflicts with automatic target execution",
				current.OwnerType, current.OrderID,
			)
			if err := e.pauseLogicalAccount(ctx, logicalAccount, target, reason); err != nil {
				return Result{}, err
			}
			return Result{Status: StatusPaused, Action: "pause"}, nil
		case current.OwnerType == string(orderdomain.OwnerTarget) &&
			current.OwnerID != target.TargetID:
			if err := e.stopOrder(ctx, current); err != nil {
				return Result{}, err
			}
			if err := e.updateTarget(ctx, &target, StatusConverging); err != nil {
				return Result{}, err
			}
			return Result{Status: StatusConverging, Action: "cancel"}, nil
		}
	}

	blocked := blockedExposures(members)
	if len(blocked) > 0 {
		for _, current := range orders {
			if current.OwnerType != string(orderdomain.OwnerTarget) ||
				current.OwnerID != target.TargetID {
				continue
			}
			if err := e.stopOrder(ctx, current); err != nil {
				return Result{}, err
			}
			target.BlockedTargets = blocked
			target.LastError = ""
			if err := e.updateTarget(ctx, &target, StatusBlocked); err != nil {
				return Result{}, err
			}
			return Result{Status: StatusBlocked, Action: "cancel"}, nil
		}
		target.BlockedTargets = blocked
		target.LastError = ""
		if err := e.updateTarget(ctx, &target, StatusBlocked); err != nil {
			return Result{}, err
		}
		return Result{Status: StatusBlocked}, nil
	}

	for _, current := range orders {
		if current.OwnerType != string(orderdomain.OwnerTarget) ||
			current.OwnerID != target.TargetID {
			continue
		}
		switch orderdomain.State(current.State) {
		case orderdomain.Pending:
			if _, err := e.Orders.Submit(ctx, spaceID, current.OrderID); err != nil {
				if targetSubmitConflict(err) {
					if _, discardErr := e.Orders.DiscardPending(
						ctx, spaceID, current.OrderID,
					); discardErr != nil {
						return Result{}, errors.Join(err, discardErr)
					}
					if errors.Is(err, orderapp.ErrAccountNotReady) {
						target.LastError = err.Error()
						if updateErr := e.updateTarget(
							ctx, &target, StatusPending,
						); updateErr != nil {
							return Result{}, updateErr
						}
					} else {
						if pauseErr := e.pauseLogicalAccount(
							ctx, logicalAccount, target, err.Error(),
						); pauseErr != nil {
							return Result{}, pauseErr
						}
					}
					return Result{Status: StatusPaused, Action: "pause"}, nil
				}
				return Result{}, err
			}
			_ = e.updateTarget(ctx, &target, StatusConverging)
			return Result{Status: StatusConverging, Action: "submit"}, nil
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			if _, err := e.Orders.ResolveUnknown(ctx, spaceID, current.OrderID); err != nil {
				return Result{}, err
			}
			_ = e.updateTarget(ctx, &target, StatusConverging)
			return Result{Status: StatusConverging, Action: "resolve"}, nil
		default:
			_ = e.updateTarget(ctx, &target, StatusConverging)
			return Result{Status: StatusConverging}, nil
		}
	}

	desired, err := desiredTargets(target)
	if err != nil {
		return Result{}, err
	}
	lanes := laneIDs(desired, members)
	blocked = nil
	for _, instrumentID := range lanes {
		action, complete, reason, actionErr := nextLaneAction(
			instrumentID, desired[instrumentID], members,
		)
		if actionErr != nil {
			return Result{}, actionErr
		}
		if reason != "" {
			blocked = append(blocked, store.BlockedTarget{
				InstrumentID: instrumentID,
				Quantity:     desired[instrumentID].String(),
				Reason:       reason,
			})
			continue
		}
		if complete {
			continue
		}
		placed, placeReason, placeErr := e.placeAction(
			ctx, spaceID, target, action, members,
		)
		if placeErr != nil {
			if targetSubmitConflict(placeErr) {
				if errors.Is(placeErr, orderapp.ErrAccountNotReady) {
					target.LastError = placeErr.Error()
					if updateErr := e.updateTarget(
						ctx, &target, StatusPending,
					); updateErr != nil {
						return Result{}, updateErr
					}
				} else {
					if pauseErr := e.pauseLogicalAccount(
						ctx, logicalAccount, target, placeErr.Error(),
					); pauseErr != nil {
						return Result{}, pauseErr
					}
				}
				return Result{Status: StatusPaused, Action: "pause"}, nil
			}
			return Result{}, placeErr
		}
		if placeReason != "" {
			blocked = append(blocked, store.BlockedTarget{
				InstrumentID: instrumentID,
				Quantity:     action.delta.String(),
				Reason:       placeReason,
			})
			continue
		}
		if placed {
			target.BlockedTargets = blocked
			target.LastError = ""
			if err := e.updateTarget(ctx, &target, StatusConverging); err != nil {
				return Result{}, err
			}
			return Result{Status: StatusConverging, Action: "place"}, nil
		}
	}
	target.BlockedTargets = blocked
	target.LastError = ""
	if len(blocked) > 0 {
		if err := e.updateTarget(ctx, &target, StatusBlocked); err != nil {
			return Result{}, err
		}
		return Result{Status: StatusBlocked}, nil
	}
	if err := e.updateTarget(ctx, &target, StatusConverged); err != nil {
		return Result{}, err
	}
	return Result{Status: StatusConverged}, nil
}

func (e *Executor) loadMembers(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
) ([]memberState, error) {
	records, err := e.Store.ListLogicalAccountMembers(
		ctx, spaceID, logicalAccountID, false,
	)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: logical account has no enabled members", ErrInvalidTarget)
	}
	members := make([]memberState, 0, len(records))
	for _, record := range records {
		account, err := e.Store.GetExchangeAccountByID(ctx, record.ExchangeAccountID)
		if err != nil {
			return nil, err
		}
		instrumentRecords, err := e.Store.ListInstruments(
			ctx, account.Exchange, account.MarketType,
		)
		if err != nil {
			return nil, err
		}
		instruments := make(map[string]store.InstrumentRecord)
		symbols := make(map[string]string)
		for _, instrument := range instrumentRecords {
			if instrument.Status != "TRADING" && instrument.Status != "live" {
				continue
			}
			if instrument.SettlementAsset != "" &&
				instrument.SettlementAsset != account.SettlementAsset {
				continue
			}
			instruments[instrument.InstrumentID] = instrument
			symbols[instrument.Symbol] = instrument.InstrumentID
		}
		positions := make(map[string]shared.Decimal)
		blocked := make([]store.BlockedTarget, 0)
		if account.MarketType == string(exchange.MarketTypeSpot) {
			for _, balance := range account.Snapshot.Balances {
				if balance.Asset == account.SettlementAsset {
					continue
				}
				quantity, parseErr := shared.ParseDecimal(balance.Total)
				if parseErr != nil {
					return nil, parseErr
				}
				if quantity.Cmp(shared.Zero()) <= 0 {
					continue
				}
				instrumentID, mapped := spotInstrumentID(
					balance.Asset,
					instruments,
				)
				if !mapped {
					blocked = append(blocked, store.BlockedTarget{
						InstrumentID: balance.Asset,
						Quantity:     quantity.String(),
						Reason: account.ExchangeAccountID +
							": asset has no unique tradable instrument mapping",
					})
					continue
				}
				positions[instrumentID] = positions[instrumentID].Add(quantity)
			}
		} else {
			positionRecords, listErr := e.Store.ListPositions(
				ctx, spaceID, account.ExchangeAccountID, "",
			)
			if listErr != nil {
				return nil, listErr
			}
			for _, position := range positionRecords {
				quantity, parseErr := shared.ParseDecimal(position.SignedQuantity)
				if parseErr != nil {
					return nil, parseErr
				}
				if quantity.IsZero() {
					continue
				}
				instrumentID := symbols[position.Symbol]
				if instrumentID == "" {
					blocked = append(blocked, store.BlockedTarget{
						InstrumentID: position.Symbol,
						Quantity:     quantity.String(),
						Reason: account.ExchangeAccountID +
							": position has no tradable instrument mapping",
					})
					continue
				}
				positions[instrumentID] = positions[instrumentID].Add(quantity)
			}
		}
		members = append(members, memberState{
			member: record, account: account,
			instruments: instruments, positions: positions, blocked: blocked,
		})
	}
	return members, nil
}

func spotInstrumentID(
	asset string,
	instruments map[string]store.InstrumentRecord,
) (string, bool) {
	instrumentID := ""
	for candidateID, instrument := range instruments {
		if instrument.BaseAsset != asset {
			continue
		}
		if instrumentID != "" && instrumentID != candidateID {
			return "", false
		}
		instrumentID = candidateID
	}
	return instrumentID, instrumentID != ""
}

func blockedExposures(members []memberState) []store.BlockedTarget {
	var blocked []store.BlockedTarget
	for _, member := range members {
		blocked = append(blocked, member.blocked...)
	}
	return blocked
}

func (e *Executor) activeOrders(
	ctx context.Context,
	spaceID string,
	members []memberState,
) ([]store.OrderRecord, error) {
	var active []store.OrderRecord
	for _, member := range members {
		records, err := e.Store.ListOrdersForAccount(
			ctx, spaceID, member.account.ExchangeAccountID, 1,
		)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if !orderdomain.State(record.State).Terminal() {
				active = append(active, record)
			}
		}
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].OrderID < active[j].OrderID
	})
	return active, nil
}

func desiredTargets(
	target store.LogicalAccountTargetRecord,
) (map[string]shared.Decimal, error) {
	desired := make(map[string]shared.Decimal, len(target.Targets))
	for _, current := range target.Targets {
		quantity, err := shared.ParseDecimal(current.Quantity)
		if err != nil {
			return nil, err
		}
		desired[current.InstrumentID] = quantity
	}
	return desired, nil
}

func laneIDs(
	desired map[string]shared.Decimal,
	members []memberState,
) []string {
	set := make(map[string]struct{}, len(desired))
	for instrumentID := range desired {
		set[instrumentID] = struct{}{}
	}
	for _, member := range members {
		for instrumentID, quantity := range member.positions {
			if !quantity.IsZero() {
				set[instrumentID] = struct{}{}
			}
		}
	}
	values := make([]string, 0, len(set))
	for instrumentID := range set {
		values = append(values, instrumentID)
	}
	sort.Strings(values)
	return values
}

func nextLaneAction(
	instrumentID string,
	desired shared.Decimal,
	members []memberState,
) (laneAction, bool, string, error) {
	confirmed := shared.Zero()
	for _, member := range members {
		confirmed = confirmed.Add(member.positions[instrumentID])
	}
	for _, member := range positionsByAbsoluteSize(members, instrumentID) {
		position := member.positions[instrumentID]
		if position.IsZero() {
			continue
		}
		if desired.IsZero() || !sameSign(position, desired) {
			instrument, ok := member.instruments[instrumentID]
			if !ok {
				return laneAction{}, false,
					"position has no tradable instrument mapping", nil
			}
			return laneAction{
				member: member, instrument: instrument,
				delta: position.Neg(), reducing: true,
			}, false, "", nil
		}
	}
	if confirmed.Cmp(desired) == 0 {
		return laneAction{}, true, "", nil
	}
	delta := desired.Sub(confirmed)
	if sameSign(confirmed, desired) && confirmed.Abs().Cmp(desired.Abs()) > 0 {
		excess := confirmed.Abs().Sub(desired.Abs())
		for _, member := range positionsByAbsoluteSize(members, instrumentID) {
			position := member.positions[instrumentID]
			if !sameSign(position, desired) {
				continue
			}
			quantity := excess
			if position.Abs().Cmp(quantity) < 0 {
				quantity = position.Abs()
			}
			if position.Cmp(shared.Zero()) > 0 {
				quantity = quantity.Neg()
			}
			instrument, ok := member.instruments[instrumentID]
			if !ok {
				return laneAction{}, false,
					"position has no tradable instrument mapping", nil
			}
			return laneAction{
				member: member, instrument: instrument,
				delta: quantity, reducing: true,
			}, false, "", nil
		}
	}
	for _, member := range members {
		if instrument, ok := member.instruments[instrumentID]; ok {
			return laneAction{
				member: member, instrument: instrument,
				delta: delta,
			}, false, "", nil
		}
	}
	return laneAction{}, false, "no enabled member supports instrument", nil
}

func positionsByAbsoluteSize(
	members []memberState,
	instrumentID string,
) []memberState {
	values := append([]memberState(nil), members...)
	sort.SliceStable(values, func(i, j int) bool {
		left := values[i].positions[instrumentID].Abs()
		right := values[j].positions[instrumentID].Abs()
		cmp := left.Cmp(right)
		if cmp != 0 {
			return cmp > 0
		}
		if values[i].member.Priority != values[j].member.Priority {
			return values[i].member.Priority < values[j].member.Priority
		}
		return values[i].account.ExchangeAccountID <
			values[j].account.ExchangeAccountID
	})
	return values
}

func (e *Executor) placeAction(
	ctx context.Context,
	spaceID string,
	target store.LogicalAccountTargetRecord,
	action laneAction,
	members []memberState,
) (bool, string, error) {
	candidates := []laneAction{action}
	if !action.reducing {
		candidates = candidates[:0]
		for _, member := range members {
			instrument, ok := member.instruments[action.instrument.InstrumentID]
			if !ok {
				continue
			}
			candidates = append(candidates, laneAction{
				member: member, instrument: instrument, delta: action.delta,
			})
		}
	}
	var capacityErrors []string
	for _, candidate := range candidates {
		quantity, reason, err := e.childQuantity(ctx, candidate)
		if err != nil {
			return false, "", err
		}
		if reason != "" {
			capacityErrors = append(capacityErrors, reason)
			continue
		}
		quote, err := e.Prices.LatestPrice(
			ctx,
			candidate.member.account.ExchangeAccountID,
			candidate.instrument.Symbol,
		)
		if err != nil {
			return false, "", err
		}
		if e.MaxChildNotional.Cmp(shared.Zero()) > 0 {
			maxQuantity := floorToStep(
				e.MaxChildNotional.Div(quote.Price),
				mustBaseStep(candidate.instrument),
			)
			if maxQuantity.Cmp(shared.Zero()) > 0 && quantity.Cmp(maxQuantity) > 0 {
				quantity = maxQuantity
			}
		}
		belowMinimum, err := belowMinimumNotional(
			quantity,
			quote.Price,
			candidate.instrument.MinNotional,
		)
		if err != nil {
			return false, "", err
		}
		if belowMinimum {
			capacityErrors = append(
				capacityErrors,
				candidate.member.account.ExchangeAccountID+
					": notional is below Exchange minimum",
			)
			continue
		}
		runnerID := target.RunnerID
		spec := orderdomain.OrderSpec{
			ClientOrderSpec: orderdomain.ClientOrderSpec{
				ExchangeAccountID: candidate.member.account.ExchangeAccountID,
				ClientOrderID:     childClientOrderID(),
				InstrumentID:      candidate.instrument.Symbol,
				Type:              exchange.OrderTypeMarket,
				Side:              sideForDelta(candidate.delta),
				Quantity:          quantity,
			},
			ReferencePrice: quote.Price, ReferencePriceAt: quote.UpdatedAt,
			ReducePositionOnly: candidate.reducing,
			Owner: orderdomain.OrderOwner{
				Type: orderdomain.OwnerTarget, OwnerID: target.TargetID,
				LogicalAccountID: target.LogicalAccountID, RunnerID: &runnerID,
			},
		}
		if candidate.member.account.MarketType == string(exchange.MarketTypeSwap) {
			spec.PositionSide = exchange.PositionSideNet
		}
		placed, err := e.Orders.Place(ctx, spaceID, spec)
		if err != nil {
			if capacityError(err) && !candidate.reducing {
				capacityErrors = append(
					capacityErrors,
					candidate.member.account.ExchangeAccountID+": "+err.Error(),
				)
				continue
			}
			return false, "", err
		}
		if _, err := e.Orders.Submit(ctx, spaceID, string(placed.ID)); err != nil {
			if targetSubmitConflict(err) {
				_, discardErr := e.Orders.DiscardPending(
					ctx, spaceID, string(placed.ID),
				)
				return false, "", errors.Join(err, discardErr)
			}
			return false, "", err
		}
		return true, "", nil
	}
	if len(capacityErrors) == 0 {
		return false, "quantity is below Exchange minimum", nil
	}
	return false, "insufficient member capacity: " +
		strings.Join(capacityErrors, "; "), nil
}

func belowMinimumNotional(
	quantity shared.Decimal,
	price shared.Decimal,
	rawMinimum string,
) (bool, error) {
	if strings.TrimSpace(rawMinimum) == "" {
		return false, nil
	}
	minimum, err := shared.ParseDecimal(rawMinimum)
	if err != nil || minimum.IsNegative() {
		return false, fmt.Errorf("%w: minimum notional", ErrInvalidTarget)
	}
	return minimum.Cmp(shared.Zero()) > 0 &&
		quantity.Mul(price).Cmp(minimum) < 0, nil
}

func (e *Executor) childQuantity(
	_ context.Context,
	action laneAction,
) (shared.Decimal, string, error) {
	step, minimum, err := baseQuantityRules(action.instrument)
	if err != nil {
		return shared.Decimal{}, "", err
	}
	quantity := floorToStep(action.delta.Abs(), step)
	if quantity.IsZero() || quantity.Cmp(minimum) < 0 {
		return shared.Zero(), "quantity is below Exchange minimum", nil
	}
	return quantity, "", nil
}

func (e *Executor) stopOrder(
	ctx context.Context,
	current store.OrderRecord,
) error {
	switch orderdomain.State(current.State) {
	case orderdomain.Pending:
		_, err := e.Orders.DiscardPending(ctx, current.SpaceID, current.OrderID)
		return err
	case orderdomain.Submitting, orderdomain.SubmitUnknown:
		_, err := e.Orders.ResolveUnknown(ctx, current.SpaceID, current.OrderID)
		return err
	case orderdomain.Canceling, orderdomain.CancelUnknown:
		return nil
	default:
		_, err := e.Orders.Cancel(ctx, current.SpaceID, current.OrderID)
		return err
	}
}

func (e *Executor) pauseLogicalAccount(
	ctx context.Context,
	logicalAccount store.LogicalAccountRecord,
	target store.LogicalAccountTargetRecord,
	reason string,
) error {
	if err := e.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation(
			logicalAccount.SpaceID,
			logicalAccount.LogicalAccountID,
			"PAUSED",
			reason,
		)
	}); err != nil {
		return err
	}
	target.LastError = reason
	return e.updateTarget(ctx, &target, StatusPending)
}

func (e *Executor) updateTarget(
	ctx context.Context,
	target *store.LogicalAccountTargetRecord,
	status string,
) error {
	target.Status = status
	updated, err := e.Store.UpdateLogicalAccountTargetState(ctx, *target)
	if err != nil {
		return err
	}
	if !updated {
		return store.ErrConflict
	}
	return nil
}

func capacityError(err error) bool {
	return errors.Is(err, orderapp.ErrInsufficientFunds) ||
		errors.Is(err, orderapp.ErrNotionalLimit) ||
		errors.Is(err, orderapp.ErrLeverageLimit)
}

func targetSubmitConflict(err error) bool {
	return errors.Is(err, orderapp.ErrExternalConflict) ||
		errors.Is(err, orderapp.ErrAutomationPaused) ||
		errors.Is(err, orderapp.ErrTargetOwnerConflict) ||
		errors.Is(err, orderapp.ErrAccountNotReady)
}

func baseQuantityRules(
	instrument store.InstrumentRecord,
) (shared.Decimal, shared.Decimal, error) {
	step, err := shared.ParseDecimal(instrument.ExchangeQuantityStep)
	if err != nil || step.Cmp(shared.Zero()) <= 0 {
		return shared.Decimal{}, shared.Decimal{},
			fmt.Errorf("%w: quantity step", ErrInvalidTarget)
	}
	minimum, err := shared.ParseDecimal(instrument.MinExchangeQuantity)
	if err != nil || minimum.IsNegative() {
		return shared.Decimal{}, shared.Decimal{},
			fmt.Errorf("%w: minimum quantity", ErrInvalidTarget)
	}
	if instrument.MarketType == string(exchange.MarketTypeSwap) {
		contractValue, parseErr := shared.ParseDecimal(instrument.ContractValue)
		if parseErr != nil || contractValue.Cmp(shared.Zero()) <= 0 {
			return shared.Decimal{}, shared.Decimal{},
				fmt.Errorf("%w: contract value", ErrInvalidTarget)
		}
		step = step.Mul(contractValue)
		minimum = minimum.Mul(contractValue)
	}
	return step, minimum, nil
}

func mustBaseStep(instrument store.InstrumentRecord) shared.Decimal {
	step, _, err := baseQuantityRules(instrument)
	if err != nil {
		return shared.Zero()
	}
	return step
}

func floorToStep(value, step shared.Decimal) shared.Decimal {
	if value.Cmp(shared.Zero()) <= 0 || step.Cmp(shared.Zero()) <= 0 {
		return shared.Zero()
	}
	units := value.Div(step)
	ratio := new(big.Rat)
	if _, ok := ratio.SetString(units.String()); !ok {
		return shared.Zero()
	}
	integer := new(big.Int).Quo(ratio.Num(), ratio.Denom())
	raw, err := shared.ParseDecimal(integer.String())
	if err != nil {
		return shared.Zero()
	}
	return raw.Mul(step)
}

func sameSign(left, right shared.Decimal) bool {
	return !left.IsZero() && !right.IsZero() &&
		left.IsNegative() == right.IsNegative()
}

func sideForDelta(delta shared.Decimal) exchange.Side {
	if delta.IsNegative() {
		return exchange.SideSell
	}
	return exchange.SideBuy
}

func childClientOrderID() string {
	return "mt" + xid.New().String()
}
