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
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/rs/xid"
)

var (
	ErrExecutorConfig     = errors.New("trade target: executor is not configured")
	ErrInvalidTarget      = errors.New("trade target: invalid target")
	ErrTargetExpired      = errors.New("trade target: target validity window elapsed")
	ErrTargetSession      = errors.New("trade target: target session authorization changed")
	errTargetNotEffective = errors.New("trade target: target is not effective yet")
)

const (
	StatusPending    = "PENDING"
	StatusConverging = "CONVERGING"
	StatusConverged  = "CONVERGED"
	StatusBlocked    = "BLOCKED"
	StatusPaused     = "PAUSED"
	StatusExpired    = "EXPIRED"
	StatusSuperseded = "SUPERSEDED"
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
	account     store.TradingAccountRecord
	instruments map[string]store.InstrumentRecord
	positions   map[string]shared.Decimal
	blocked     []store.BlockedTarget
}

type laneAction struct {
	member     memberState
	instrument store.InstrumentRecord
	delta      shared.Decimal
	reducing   bool
	pinned     bool
}

func (e *Executor) Converge(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
) (result Result, resultErr error) {
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
	// Validity may lapse during quote/prepare as well as before the scan. All
	// expiry exits persist the original target identity, never a replacement.
	defer func() {
		switch {
		case singleCauseIs(resultErr, ErrTargetExpired) || singleCauseIs(resultErr, orderapp.ErrTargetExpired):
			target.Status = StatusExpired
			target.LastError = ""
			persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			updated, err := e.Store.UpdateLogicalAccountTargetState(persistCtx, target)
			if err != nil {
				result, resultErr = Result{}, err
				return
			}
			result, resultErr = Result{Status: StatusExpired}, nil
			if !updated {
				result.Status = StatusSuperseded
			}
		case singleCauseIs(resultErr, errTargetNotEffective):
			result, resultErr = Result{Status: StatusPaused}, nil
		default:
			resultErr = accountExecutionError(resultErr)
		}
	}()
	if target.Status == StatusExpired {
		return Result{Status: StatusExpired}, nil
	}
	if err := e.checkTargetExecutable(ctx, logicalAccount, target); err != nil {
		// Expiry is a normal terminal condition for this target, not a reason to
		// cancel existing orders or flatten positions. Trade will converge only
		// after a newer valid target arrives.
		return Result{Status: StatusPaused}, err
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
			if err := e.stopOrder(ctx, target, current); err != nil {
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
	if target.InstanceID == "" {
		if logicalAccount.OwnerRunnerID != target.RunnerID {
			return Result{}, fmt.Errorf("%w: target runner no longer owns logical account", ErrInvalidTarget)
		}
	} else if logicalAccount.OwnerInstanceID != target.InstanceID || logicalAccount.OwnerSessionID != target.SessionID {
		return Result{}, ErrTargetSession
	}

	members, err := e.loadMembers(ctx, spaceID, logicalAccountID)
	if err != nil {
		return Result{}, err
	}
	for _, member := range members {
		if member.account.Status != "ENABLED" || !member.account.Ready {
			target.LastError = "member " + member.account.TradingAccountID + " is not ready"
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
			if err := e.stopOrder(ctx, target, current); err != nil {
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
			if err := e.stopOrder(ctx, target, current); err != nil {
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
			if err := e.checkTargetExecutable(ctx, logicalAccount, target); err != nil {
				return Result{Status: StatusPaused}, err
			}
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
			if err := e.updateTarget(ctx, &target, StatusConverging); err != nil {
				return Result{}, err
			}
			return Result{Status: StatusConverging, Action: "submit"}, nil
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			if _, err := e.Orders.ResolveUnknown(ctx, spaceID, current.OrderID); err != nil {
				return Result{}, err
			}
			if err := e.updateTarget(ctx, &target, StatusConverging); err != nil {
				return Result{}, err
			}
			return Result{Status: StatusConverging, Action: "resolve"}, nil
		default:
			if err := e.updateTarget(ctx, &target, StatusConverging); err != nil {
				return Result{}, err
			}
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
				Quantity:     desired[instrumentID].Quantity.String(),
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
		account, err := e.Store.GetTradingAccountByID(ctx, record.TradingAccountID)
		if err != nil {
			return nil, err
		}
		instrumentRecords, err := e.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
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
			symbols[instrument.ExchangeSymbol] = instrument.InstrumentID
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
						Reason: account.TradingAccountID +
							": asset has no unique tradable instrument mapping",
					})
					continue
				}
				positions[instrumentID] = positions[instrumentID].Add(quantity)
			}
		} else {
			positionRecords, listErr := e.Store.ListPositions(
				ctx, spaceID, account.TradingAccountID, "",
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
				instrumentID := symbols[position.ExchangeSymbol]
				if instrumentID == "" {
					blocked = append(blocked, store.BlockedTarget{
						InstrumentID: position.ExchangeSymbol,
						Quantity:     quantity.String(),
						Reason: account.TradingAccountID +
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
			ctx, spaceID, member.account.TradingAccountID, 1,
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

type desiredTarget struct {
	Quantity         shared.Decimal
	TradingAccountID string
	ExchangeSymbol   string
}

func desiredTargets(
	target store.LogicalAccountTargetRecord,
) (map[string]desiredTarget, error) {
	desired := make(map[string]desiredTarget, len(target.Targets))
	for _, current := range target.Targets {
		quantity, err := shared.ParseDecimal(current.Quantity)
		if err != nil {
			return nil, err
		}
		desired[current.InstrumentID] = desiredTarget{
			Quantity:         quantity,
			TradingAccountID: current.TradingAccountID,
			ExchangeSymbol:   current.ExchangeSymbol,
		}
	}
	return desired, nil
}

func laneIDs(
	desired map[string]desiredTarget,
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
	desired desiredTarget,
	members []memberState,
) (laneAction, bool, string, error) {
	if desired.TradingAccountID != "" {
		var pinnedMember *memberState
		for i := range members {
			if members[i].account.TradingAccountID == desired.TradingAccountID {
				pinnedMember = &members[i]
				break
			}
		}
		if pinnedMember == nil {
			return laneAction{}, false, "frozen target member is no longer available", nil
		}
		pinnedInstrument, ok := pinnedMember.instruments[instrumentID]
		if !ok || (desired.ExchangeSymbol != "" && pinnedInstrument.ExchangeSymbol != desired.ExchangeSymbol) {
			return laneAction{}, false, "frozen target member no longer supports instrument", nil
		}
		// A frozen conversion is an executable venue decision, not merely a
		// preferred opening venue. Drain any existing exposure on other members
		// first; otherwise aggregate convergence could silently leave the target
		// on a different venue (or skip the frozen member entirely).
		for _, member := range positionsByAbsoluteSize(members, instrumentID) {
			if member.account.TradingAccountID == desired.TradingAccountID {
				continue
			}
			position := member.positions[instrumentID]
			if position.IsZero() {
				continue
			}
			instrument, mapped := member.instruments[instrumentID]
			if !mapped {
				return laneAction{}, false, "position has no tradable instrument mapping", nil
			}
			return laneAction{member: member, instrument: instrument, delta: position.Neg(), reducing: true}, false, "", nil
		}
		pinnedPosition := pinnedMember.positions[instrumentID]
		if desired.Quantity.IsZero() || !sameSign(pinnedPosition, desired.Quantity) {
			if !pinnedPosition.IsZero() {
				return laneAction{member: *pinnedMember, instrument: pinnedInstrument, delta: pinnedPosition.Neg(), reducing: true}, false, "", nil
			}
			if desired.Quantity.IsZero() {
				return laneAction{}, true, "", nil
			}
		}
		if sameSign(pinnedPosition, desired.Quantity) && pinnedPosition.Abs().Cmp(desired.Quantity.Abs()) > 0 {
			excess := pinnedPosition.Abs().Sub(desired.Quantity.Abs())
			if pinnedPosition.Cmp(shared.Zero()) > 0 {
				excess = excess.Neg()
			}
			return laneAction{member: *pinnedMember, instrument: pinnedInstrument, delta: excess, reducing: true}, false, "", nil
		}
		if pinnedPosition.Cmp(desired.Quantity) == 0 {
			return laneAction{}, true, "", nil
		}
		return laneAction{member: *pinnedMember, instrument: pinnedInstrument, delta: desired.Quantity.Sub(pinnedPosition), pinned: true}, false, "", nil
	}
	confirmed := shared.Zero()
	for _, member := range members {
		confirmed = confirmed.Add(member.positions[instrumentID])
	}
	for _, member := range positionsByAbsoluteSize(members, instrumentID) {
		position := member.positions[instrumentID]
		if position.IsZero() {
			continue
		}
		if desired.Quantity.IsZero() || !sameSign(position, desired.Quantity) {
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
	if confirmed.Cmp(desired.Quantity) == 0 {
		return laneAction{}, true, "", nil
	}
	delta := desired.Quantity.Sub(confirmed)
	if sameSign(confirmed, desired.Quantity) && confirmed.Abs().Cmp(desired.Quantity.Abs()) > 0 {
		excess := confirmed.Abs().Sub(desired.Quantity.Abs())
		for _, member := range positionsByAbsoluteSize(members, instrumentID) {
			position := member.positions[instrumentID]
			if !sameSign(position, desired.Quantity) {
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
	if desired.TradingAccountID != "" {
		for _, member := range members {
			if member.account.TradingAccountID != desired.TradingAccountID {
				continue
			}
			instrument, ok := member.instruments[instrumentID]
			if !ok || (desired.ExchangeSymbol != "" && instrument.ExchangeSymbol != desired.ExchangeSymbol) {
				return laneAction{}, false, "frozen target member no longer supports instrument", nil
			}
			return laneAction{member: member, instrument: instrument, delta: delta, pinned: true}, false, "", nil
		}
		return laneAction{}, false, "frozen target member is no longer available", nil
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
		return values[i].account.TradingAccountID <
			values[j].account.TradingAccountID
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
	if !action.reducing && !action.pinned {
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
			candidate.member.account.TradingAccountID,
			candidate.instrument.ExchangeSymbol,
		)
		if err != nil {
			if paper.IsInfrastructureError(err) {
				return false, "", err
			}
			return false, "", &AccountError{TradingAccountID: candidate.member.account.TradingAccountID, Err: err}
		}
		if quote.Price.Cmp(shared.Zero()) <= 0 {
			return false, "", &AccountError{TradingAccountID: candidate.member.account.TradingAccountID, Err: orderdomain.ErrInvalidSpec}
		}
		if target.InstanceID != "" {
			if err := e.checkTargetExecutable(ctx, store.LogicalAccountRecord{
				SpaceID: spaceID, LogicalAccountID: target.LogicalAccountID,
				OwnerInstanceID: target.InstanceID, OwnerSessionID: target.SessionID,
			}, target); err != nil {
				return false, "", err
			}
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
				candidate.member.account.TradingAccountID+
					": notional is below Exchange minimum",
			)
			continue
		}
		runnerID := target.RunnerID
		spec := orderdomain.OrderSpec{
			ClientOrderSpec: orderdomain.ClientOrderSpec{
				TradingAccountID: candidate.member.account.TradingAccountID,
				ClientOrderID:    childClientOrderID(),
				InstrumentID:     candidate.instrument.InstrumentID,
				Type:             exchange.OrderTypeMarket,
				Side:             sideForDelta(candidate.delta),
				Quantity:         quantity,
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
					candidate.member.account.TradingAccountID+": "+err.Error(),
				)
				if action.pinned {
					return false, "frozen target member capacity: " + strings.Join(capacityErrors, "; "), nil
				}
				continue
			}
			return false, "", err
		}
		if err := e.checkTargetExecutable(ctx, store.LogicalAccountRecord{SpaceID: spaceID, LogicalAccountID: target.LogicalAccountID}, target); err != nil {
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

func (e *Executor) checkTargetExecutable(
	ctx context.Context,
	logicalAccount store.LogicalAccountRecord,
	target store.LogicalAccountTargetRecord,
) error {
	if target.InstanceID == "" && target.SessionID == "" && target.ValidUntil == 0 {
		if logicalAccount.OwnerRunnerID != "" && target.RunnerID != logicalAccount.OwnerRunnerID {
			return ErrTargetSession
		}
		return nil
	}
	if target.InstanceID == "" || target.SessionID == "" || target.StrategyID == "" ||
		target.BarEndTime <= 0 || target.EffectiveAt != target.BarEndTime || target.ValidUntil <= target.EffectiveAt {
		return ErrInvalidTarget
	}
	now := time.Now().UTC()
	if e.Now != nil {
		now = e.Now().UTC()
	}
	if !now.Before(time.UnixMilli(target.ValidUntil).UTC()) {
		return ErrTargetExpired
	}
	if now.Before(time.UnixMilli(target.EffectiveAt).UTC()) {
		return errTargetNotEffective
	}
	// Always re-read the authorization immediately before an order can be
	// submitted. The initial Converge snapshot protects the read path; this
	// second read closes the authorization-change window during quote/prepare.
	fresh, err := e.Store.GetLogicalAccount(ctx, logicalAccount.SpaceID, logicalAccount.LogicalAccountID)
	if err != nil {
		return err
	}
	logicalAccount = fresh
	if logicalAccount.OwnerInstanceID != target.InstanceID || logicalAccount.OwnerSessionID != target.SessionID {
		return ErrTargetSession
	}
	return nil
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
	target store.LogicalAccountTargetRecord,
	current store.OrderRecord,
) error {
	if err := e.checkTargetExecutable(ctx, store.LogicalAccountRecord{SpaceID: current.SpaceID, LogicalAccountID: target.LogicalAccountID}, target); err != nil {
		return err
	}
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
	if err := e.checkTargetExecutable(ctx, logicalAccount, target); err != nil {
		return err
	}
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
	// Finishing a completed action must not fail merely because its external
	// call consumed the candidate budget. No new order is authorized here.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := e.checkTargetExecutable(ctx, store.LogicalAccountRecord{SpaceID: target.SpaceID, LogicalAccountID: target.LogicalAccountID}, *target); err != nil {
		return err
	}
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
