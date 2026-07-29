package order

import (
	"context"
	"errors"
	"strings"
	"time"

	gonanoid "github.com/matoous/go-nanoid/v2"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/rs/xid"
	"gorm.io/gorm"
)

var (
	ErrIdempotencyConflict = errors.New("trade order: idempotency conflict")
	ErrServiceConfig       = errors.New("trade order: service is not configured")
	ErrCrossZero           = errors.New("trade order: order cannot cross zero")
)

type AdapterSource interface {
	Adapter(exchangeAccountID string) (exchange.Adapter, error)
}

type AccountSyncer interface {
	SyncAccount(context.Context, string) error
}

type Service struct {
	Store               *store.Store
	Validator           Validator
	Adapters            AdapterSource
	Syncer              AccountSyncer
	NewOrderID          func() string
	Now                 func() time.Time
	UnknownLookupWindow time.Duration
}

func (s *Service) Get(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	if s == nil || s.Store == nil {
		return orderdomain.Order{}, ErrServiceConfig
	}
	record, err := s.Store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	return domainOrder(record)
}

func (s *Service) Place(
	ctx context.Context,
	spaceID string,
	spec orderdomain.OrderSpec,
) (orderdomain.Order, error) {
	if s == nil || s.Store == nil {
		return orderdomain.Order{}, ErrServiceConfig
	}
	if spec.ClientOrderID == "" {
		spec.ClientOrderID = xid.New().String()
		spec.ClientOrderSpec.ClientOrderID = spec.ClientOrderID
	}
	unlock := s.Store.LockExchangeAccount(spec.ExchangeAccountID)
	defer unlock()
	if existing, err := s.Store.GetOrderByClientID(
		ctx,
		spaceID,
		spec.ExchangeAccountID,
		spec.ClientOrderID,
	); err == nil {
		if !sameSpec(existing, spec) {
			return orderdomain.Order{}, ErrIdempotencyConflict
		}
		return domainOrder(existing)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return orderdomain.Order{}, err
	}
	spec, err := s.deriveReducePositionOnly(ctx, spec)
	if err != nil {
		return orderdomain.Order{}, err
	}
	validation, err := s.Validator.Validate(ctx, spaceID, spec)
	if err != nil {
		return orderdomain.Order{}, err
	}

	id := s.orderID()
	aggregate, _, err := orderdomain.New(shared.OrderID(id), spec)
	if err != nil {
		return orderdomain.Order{}, err
	}
	record := orderRecord(validation, *aggregate)
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if !validation.ReservedQuantity.IsZero() {
			unreflected, err := tx.GetUnreflectedReservation(
				validation.Account.SpaceID,
				spec.ExchangeAccountID,
				validation.ReservedAsset,
				validation.Account.LastSyncAt.UnixMilli(),
			)
			if err != nil {
				return err
			}
			available := availableBalance(
				validation.Account.Snapshot,
				validation.ReservedAsset,
			)
			if validation.Account.MarketType == exchange.MarketTypeSwap {
				available = validation.Account.Snapshot.AvailableFunds
			}
			required := unreflected.Add(validation.ReservedQuantity)
			if available.Cmp(required) < 0 {
				return ErrInsufficientFunds
			}
			if err := tx.PostLedger(store.LedgerTransactionRecord{
				SpaceID: validation.Account.SpaceID, TransactionID: "reserve:" + id,
				ExchangeAccountID: spec.ExchangeAccountID,
				TransactionType:   store.LedgerReservation,
				SourceType:        "ORDER_RESERVATION", SourceID: id,
				Entries: []store.LedgerEntryRecord{
					{
						Asset: validation.ReservedAsset, Bucket: "AVAILABLE",
						Amount: validation.ReservedQuantity.Neg(),
					},
					{
						Asset: validation.ReservedAsset, Bucket: "RESERVED",
						Amount: validation.ReservedQuantity,
					},
				},
			}); err != nil {
				return err
			}
		}
		return tx.CreateOrder(record)
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			existing, getErr := s.Store.GetOrderByClientID(
				ctx,
				validation.Account.SpaceID,
				spec.ExchangeAccountID,
				spec.ClientOrderID,
			)
			if getErr != nil {
				return orderdomain.Order{}, getErr
			}
			if !sameSpec(existing, spec) {
				return orderdomain.Order{}, ErrIdempotencyConflict
			}
			return domainOrder(existing)
		}
		return orderdomain.Order{}, err
	}
	return domainOrder(record)
}

func (s *Service) deriveReducePositionOnly(
	ctx context.Context,
	spec orderdomain.OrderSpec,
) (orderdomain.OrderSpec, error) {
	if s.Validator.Accounts == nil {
		return spec, ErrServiceConfig
	}
	account, err := s.Validator.Accounts.ExecutionEligibility(ctx, spec.ExchangeAccountID)
	if err != nil {
		return spec, err
	}
	spec.ReducePositionOnly = false
	if account.MarketType == exchange.MarketTypeSpot {
		return spec, nil
	}
	if s.Validator.Positions == nil {
		return spec, ErrServiceConfig
	}
	position, err := s.Validator.Positions.GetPosition(
		ctx,
		spec.ExchangeAccountID,
		spec.InstrumentID,
	)
	if err != nil {
		return spec, err
	}
	current := position.SignedQuantity
	if current.IsZero() {
		return spec, nil
	}
	reducing := (current.Cmp(shared.Zero()) > 0 && spec.Side == exchange.SideSell) ||
		(current.Cmp(shared.Zero()) < 0 && spec.Side == exchange.SideBuy)
	if !reducing {
		return spec, nil
	}
	if spec.Quantity.Cmp(current.Abs()) > 0 {
		if strings.EqualFold(spec.Owner.Type, "FLATTEN") {
			spec.ReducePositionOnly = true
			return spec, nil
		}
		if strings.EqualFold(spec.Owner.Type, "RPC") ||
			strings.EqualFold(spec.Owner.Type, "MANUAL") ||
			strings.EqualFold(spec.Owner.Type, "OPERATOR") ||
			strings.EqualFold(spec.Owner.Type, "TARGET") {
			return spec, ErrCrossZero
		}
		return spec, nil
	}
	spec.ReducePositionOnly = true
	return spec, nil
}

func (s *Service) Submit(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	if s == nil || s.Store == nil || s.Adapters == nil {
		return orderdomain.Order{}, ErrServiceConfig
	}
	record, err := s.Store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	unlock := s.Store.LockExchangeAccount(record.ExchangeAccountID)
	record, err = s.Store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		unlock()
		return orderdomain.Order{}, err
	}
	current, err := domainOrder(record)
	if err != nil {
		unlock()
		return orderdomain.Order{}, err
	}
	var result orderdomain.Order
	var synchronize bool
	switch current.State {
	case orderdomain.Pending:
		result, synchronize, err = s.submit(ctx, record)
	case orderdomain.Submitting, orderdomain.SubmitUnknown:
		result, err = s.resolveUnknown(ctx, record, current)
		synchronize = err == nil && result.State == orderdomain.Open
	case orderdomain.Rejected:
		result = current
		err = errors.New(record.RejectReason)
	default:
		result = current
	}
	unlock()
	if err != nil || !synchronize || s.Syncer == nil {
		return result, err
	}
	if err := s.Syncer.SyncAccount(ctx, record.ExchangeAccountID); err != nil {
		return result, err
	}
	return s.Get(ctx, record.SpaceID, record.OrderID)
}

func (s *Service) Cancel(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	if s == nil || s.Store == nil || s.Adapters == nil || s.Syncer == nil {
		return orderdomain.Order{}, ErrServiceConfig
	}
	record, err := s.Store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	adapter, err := s.Adapters.Adapter(record.ExchangeAccountID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	aggregate, err := domainOrder(record)
	if err != nil {
		return orderdomain.Order{}, err
	}
	expected := aggregate.Version
	if _, err = aggregate.BeginCancel(); err != nil {
		return orderdomain.Order{}, err
	}
	applyAggregate(&record, aggregate)
	if err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	}); err != nil {
		return orderdomain.Order{}, err
	}

	_, callErr := adapter.CancelOrder(ctx, record.Symbol, record.ClientOrderID)
	if callErr == nil {
		err = s.Syncer.SyncAccount(ctx, record.ExchangeAccountID)
		return aggregate, err
	}

	latest, getErr := s.Store.GetOrder(ctx, spaceID, orderID)
	if getErr != nil {
		return orderdomain.Order{}, getErr
	}
	current, getErr := domainOrder(latest)
	if getErr != nil {
		return orderdomain.Order{}, getErr
	}
	expected = current.Version
	if uncertainExchangeError(callErr) {
		_, err = current.MarkCancelUnknown()
	} else {
		_, err = current.CancelRejected()
	}
	if err != nil {
		return orderdomain.Order{}, err
	}
	applyAggregate(&latest, current)
	if err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(latest, expected)
	}); err != nil {
		return orderdomain.Order{}, err
	}
	return current, callErr
}

func (s *Service) RecoverCancel(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	if s == nil || s.Store == nil || s.Adapters == nil || s.Syncer == nil {
		return orderdomain.Order{}, ErrServiceConfig
	}
	record, err := s.Store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	current, err := domainOrder(record)
	if err != nil {
		return orderdomain.Order{}, err
	}
	if current.State != orderdomain.Canceling &&
		current.State != orderdomain.CancelUnknown {
		return current, orderdomain.ErrInvalidTransition
	}
	adapter, err := s.Adapters.Adapter(record.ExchangeAccountID)
	if err != nil {
		return current, err
	}
	_, callErr := adapter.CancelOrder(ctx, record.Symbol, record.ClientOrderID)
	if callErr == nil {
		if err := s.Syncer.SyncAccount(ctx, record.ExchangeAccountID); err != nil {
			return current, err
		}
		return s.Get(ctx, spaceID, orderID)
	}
	if !uncertainExchangeError(callErr) {
		if syncErr := s.Syncer.SyncAccount(ctx, record.ExchangeAccountID); syncErr == nil {
			latest, getErr := s.Get(ctx, spaceID, orderID)
			if getErr == nil {
				if latest.State.Terminal() ||
					(latest.State != orderdomain.Canceling &&
						latest.State != orderdomain.CancelUnknown) {
					return latest, nil
				}
				current = latest
				record, getErr = s.Store.GetOrder(ctx, spaceID, orderID)
				if getErr != nil {
					return orderdomain.Order{}, getErr
				}
			}
		}
	}
	expected := current.Version
	switch {
	case uncertainExchangeError(callErr) && current.State == orderdomain.Canceling:
		_, err = current.MarkCancelUnknown()
	case uncertainExchangeError(callErr):
		return current, callErr
	case current.State == orderdomain.Canceling:
		_, err = current.CancelRejected()
	default:
		_, err = current.CancelStillOpen()
	}
	if err != nil {
		return current, err
	}
	applyAggregate(&record, current)
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	}); err != nil {
		return orderdomain.Order{}, err
	}
	return current, callErr
}

func (s *Service) DiscardPending(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	if s == nil || s.Store == nil {
		return orderdomain.Order{}, ErrServiceConfig
	}
	record, err := s.Store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	unlock := s.Store.LockExchangeAccount(record.ExchangeAccountID)
	defer unlock()
	record, err = s.Store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	current, err := domainOrder(record)
	if err != nil {
		return orderdomain.Order{}, err
	}
	expected := current.Version
	if _, err := current.DiscardPending(); err != nil {
		return orderdomain.Order{}, err
	}
	releaseRecord := record
	applyAggregate(&record, current)
	record.RemainingReservedQuantity = "0"
	record.FinishedAt = s.now().UnixMilli()
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateOrder(record, expected); err != nil {
			return err
		}
		return releaseReservation(tx, releaseRecord)
	}); err != nil {
		return orderdomain.Order{}, err
	}
	return current, nil
}

func (s *Service) ResolveUnknown(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	if s == nil || s.Store == nil || s.Adapters == nil {
		return orderdomain.Order{}, ErrServiceConfig
	}
	record, err := s.Store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	unlock := s.Store.LockExchangeAccount(record.ExchangeAccountID)
	record, err = s.Store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		unlock()
		return orderdomain.Order{}, err
	}
	current, err := domainOrder(record)
	if err != nil {
		unlock()
		return current, err
	}
	wasUnknown := current.State == orderdomain.Submitting ||
		current.State == orderdomain.SubmitUnknown
	resolved, err := s.resolveUnknown(ctx, record, current)
	unlock()
	if err != nil || !wasUnknown || resolved.State != orderdomain.Open ||
		s.Syncer == nil {
		return resolved, err
	}
	if err := s.Syncer.SyncAccount(ctx, record.ExchangeAccountID); err != nil {
		return resolved, err
	}
	return s.Get(ctx, record.SpaceID, record.OrderID)
}

func (s *Service) resolveUnknown(
	ctx context.Context,
	record store.OrderRecord,
	current orderdomain.Order,
) (orderdomain.Order, error) {
	if current.State == orderdomain.Submitting {
		expected := current.Version
		if _, err := current.MarkSubmitUnknown(); err != nil {
			return orderdomain.Order{}, err
		}
		applyAggregate(&record, current)
		if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
			return tx.UpdateOrder(record, expected)
		}); err != nil {
			return orderdomain.Order{}, err
		}
	} else if current.State != orderdomain.SubmitUnknown {
		return current, nil
	}
	adapter, err := s.Adapters.Adapter(record.ExchangeAccountID)
	if err != nil {
		return current, err
	}
	found, lookupErr := adapter.GetOrder(ctx, record.Symbol, record.ClientOrderID)
	if lookupErr == nil {
		return s.resolveUnknownFound(ctx, record, current, found.ExchangeOrderID)
	}
	if !exchange.IsKind(lookupErr, exchange.ErrorOrderNotFound) {
		return current, lookupErr
	}
	fills, _, fillsErr := adapter.ListRecentFills(ctx, record.Symbol, "")
	if fillsErr != nil {
		return current, fillsErr
	}
	exchangeOrderID := ""
	for _, fill := range fills {
		if fill.ClientOrderID == record.ClientOrderID {
			if fill.ExchangeOrderID == "" {
				continue
			}
			if exchangeOrderID != "" && exchangeOrderID != fill.ExchangeOrderID {
				return current, nil
			}
			exchangeOrderID = fill.ExchangeOrderID
		}
	}
	if exchangeOrderID != "" {
		return s.resolveUnknownFound(ctx, record, current, exchangeOrderID)
	}
	window := s.UnknownLookupWindow
	if window <= 0 {
		window = 30 * time.Second
	}
	if record.SubmittedAt <= 0 ||
		s.now().Sub(time.UnixMilli(record.SubmittedAt)) < window {
		return current, nil
	}
	expected := current.Version
	if _, err := current.ReturnToPending(); err != nil {
		return orderdomain.Order{}, err
	}
	applyAggregate(&record, current)
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	}); err != nil {
		return orderdomain.Order{}, err
	}
	return current, nil
}

func (s *Service) resolveUnknownFound(
	ctx context.Context,
	record store.OrderRecord,
	current orderdomain.Order,
	exchangeOrderID string,
) (orderdomain.Order, error) {
	expected := current.Version
	if _, err := current.Acknowledge(exchangeOrderID); err != nil {
		return orderdomain.Order{}, err
	}
	applyAggregate(&record, current)
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	}); err != nil {
		return orderdomain.Order{}, err
	}
	return current, nil
}

func (s *Service) submit(
	ctx context.Context,
	record store.OrderRecord,
) (orderdomain.Order, bool, error) {
	aggregate, err := domainOrder(record)
	if err != nil {
		return orderdomain.Order{}, false, err
	}
	validator := s.Validator
	if record.SubmittedAt > 0 {
		// A confirmed-absent retry reuses the already validated server quote.
		// Revalidate account and instrument safety without making the unknown
		// lookup window itself turn every controlled retry into a stale quote.
		now := s.now()
		validator.Now = func() time.Time { return now }
		validator.MaxReferenceAge = now.Sub(aggregate.Spec.ReferencePriceAt) + time.Second
	}
	if _, err := validator.Validate(ctx, record.SpaceID, aggregate.Spec); err != nil {
		if permanentValidationError(err) {
			rejected, rejectErr := s.rejectPending(ctx, record, aggregate, err)
			return rejected, false, rejectErr
		}
		return orderdomain.Order{}, false, err
	}
	adapter, err := s.Adapters.Adapter(record.ExchangeAccountID)
	if err != nil {
		return orderdomain.Order{}, false, err
	}
	expected := aggregate.Version
	if _, err = aggregate.BeginSubmit(); err != nil {
		return orderdomain.Order{}, false, err
	}
	applyAggregate(&record, aggregate)
	record.SubmittedAt = s.now().UnixMilli()
	if err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	}); err != nil {
		return orderdomain.Order{}, false, err
	}

	response, callErr := adapter.PlaceOrder(ctx, exchange.OrderRequest{
		ClientOrderID: aggregate.Spec.ClientOrderID,
		Symbol:        aggregate.Spec.InstrumentID, OrderType: aggregate.Spec.Type,
		FillPolicy: aggregate.Spec.FillPolicy, Side: aggregate.Spec.Side,
		PositionSide: aggregate.Spec.PositionSide, Quantity: aggregate.Spec.Quantity,
		LimitPrice: aggregate.Spec.LimitPrice, ReferencePrice: aggregate.Spec.ReferencePrice,
		ReduceOnly: aggregate.Spec.ReducePositionOnly,
	})

	latest, getErr := s.Store.GetOrder(ctx, record.SpaceID, record.OrderID)
	if getErr != nil {
		return orderdomain.Order{}, false, getErr
	}
	current, getErr := domainOrder(latest)
	if getErr != nil {
		return orderdomain.Order{}, false, getErr
	}
	expected = current.Version
	switch {
	case callErr == nil:
		_, err = current.Acknowledge(response.ExchangeOrderID)
	case uncertainExchangeError(callErr):
		_, err = current.MarkSubmitUnknown()
	default:
		_, err = current.Reject()
		latest.RejectReason = callErr.Error()
	}
	if err != nil {
		return orderdomain.Order{}, false, err
	}
	applyAggregate(&latest, current)
	if callErr == nil {
		latest.ExchangeOrderID = response.ExchangeOrderID
		latest.ExchangeUpdatedAt = response.UpdatedAt.UnixMilli()
		if latest.ExchangeUpdatedAt <= 0 {
			latest.ExchangeUpdatedAt = response.CreatedAt.UnixMilli()
		}
	}
	releaseRecord := latest
	if current.State == orderdomain.Rejected {
		latest.RemainingReservedQuantity = "0"
		latest.FinishedAt = s.now().UnixMilli()
	}
	if err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateOrder(latest, expected); err != nil {
			return err
		}
		if current.State == orderdomain.Rejected {
			return releaseReservation(tx, releaseRecord)
		}
		return nil
	}); err != nil {
		return orderdomain.Order{}, false, err
	}
	return current, callErr == nil &&
		response.Status != "" &&
		response.Status != exchange.OrderStatusOpen, callErr
}

func uncertainExchangeError(err error) bool {
	return exchange.IsKind(err, exchange.ErrorTransportUnknown) ||
		exchange.IsKind(err, exchange.ErrorRateLimited)
}

func (s *Service) rejectPending(
	ctx context.Context,
	record store.OrderRecord,
	current orderdomain.Order,
	cause error,
) (orderdomain.Order, error) {
	expected := current.Version
	if _, err := current.Reject(); err != nil {
		return orderdomain.Order{}, err
	}
	releaseRecord := record
	applyAggregate(&record, current)
	record.RejectReason = cause.Error()
	record.RemainingReservedQuantity = "0"
	record.FinishedAt = s.now().UnixMilli()
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateOrder(record, expected); err != nil {
			return err
		}
		return releaseReservation(tx, releaseRecord)
	}); err != nil {
		return orderdomain.Order{}, err
	}
	return current, cause
}

func permanentValidationError(err error) bool {
	for _, target := range []error{
		orderdomain.ErrInvalidSpec,
		ErrAccountOwnership,
		ErrInstrumentDisabled,
		ErrQuantityRule,
		ErrNotionalLimit,
		ErrInsufficientFunds,
		ErrLeverageLimit,
		ErrReduceOnly,
		ErrPaperLimit,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func releaseReservation(tx *store.Tx, record store.OrderRecord) error {
	remaining, err := shared.ParseDecimal(record.RemainingReservedQuantity)
	if err != nil {
		return err
	}
	if remaining.IsZero() {
		return nil
	}
	return tx.PostLedger(store.LedgerTransactionRecord{
		SpaceID: record.SpaceID, TransactionID: "release:" + record.OrderID,
		ExchangeAccountID: record.ExchangeAccountID,
		TransactionType:   store.LedgerReservationRelease,
		SourceType:        "ORDER_RESERVATION_RELEASE", SourceID: record.OrderID,
		Entries: []store.LedgerEntryRecord{
			{Asset: record.ReservedAsset, Bucket: "RESERVED", Amount: remaining.Neg()},
			{Asset: record.ReservedAsset, Bucket: "AVAILABLE", Amount: remaining},
		},
	})
}

func orderRecord(validation Validation, value orderdomain.Order) store.OrderRecord {
	var limitPrice *string
	if value.Spec.LimitPrice != nil {
		raw := value.Spec.LimitPrice.String()
		limitPrice = &raw
	}
	return store.OrderRecord{
		SpaceID: validation.Account.SpaceID, OrderID: string(value.ID),
		ExchangeAccountID: value.Spec.ExchangeAccountID,
		ClientOrderID:     value.Spec.ClientOrderID,
		Exchange:          string(validation.Account.Exchange),
		MarketType:        string(validation.Account.MarketType), Symbol: value.Spec.InstrumentID,
		OrderType: string(value.Spec.Type), TimeInForce: string(value.Spec.FillPolicy),
		Side: string(value.Spec.Side), PositionSide: string(value.Spec.PositionSide),
		Quantity: value.Spec.Quantity.String(), LimitPrice: limitPrice,
		ReferencePrice:   value.Spec.ReferencePrice.String(),
		ReferencePriceAt: value.Spec.ReferencePriceAt.UnixMilli(),
		ReduceOnly:       value.Spec.ReducePositionOnly, Source: value.Spec.Owner.Type,
		StrategyExecutionID: value.Spec.Owner.StrategyExecutionID,
		State:               string(value.State), FilledQuantity: "0", AveragePrice: "0",
		ReservedAsset:             validation.ReservedAsset,
		ReservedQuantity:          validation.ReservedQuantity.String(),
		RemainingReservedQuantity: validation.ReservedQuantity.String(),
		Version:                   value.Version,
	}
}

func domainOrder(record store.OrderRecord) (orderdomain.Order, error) {
	quantity, err := shared.ParseDecimal(record.Quantity)
	if err != nil {
		return orderdomain.Order{}, err
	}
	referencePrice, err := shared.ParseDecimal(record.ReferencePrice)
	if err != nil {
		return orderdomain.Order{}, err
	}
	filled, err := shared.ParseDecimal(record.FilledQuantity)
	if err != nil {
		return orderdomain.Order{}, err
	}
	average, err := shared.ParseDecimal(record.AveragePrice)
	if err != nil {
		return orderdomain.Order{}, err
	}
	var limitPrice *shared.Decimal
	if record.LimitPrice != nil {
		value, parseErr := shared.ParseDecimal(*record.LimitPrice)
		if parseErr != nil {
			return orderdomain.Order{}, parseErr
		}
		limitPrice = &value
	}
	return orderdomain.Order{
		ID: shared.OrderID(record.OrderID),
		Spec: orderdomain.OrderSpec{
			ClientOrderSpec: orderdomain.ClientOrderSpec{
				ExchangeAccountID: record.ExchangeAccountID,
				ClientOrderID:     record.ClientOrderID,
				InstrumentID:      record.Symbol,
				Type:              exchange.OrderType(record.OrderType),
				FillPolicy:        exchange.FillPolicy(record.TimeInForce),
				Side:              exchange.Side(record.Side),
				PositionSide:      exchange.PositionSide(record.PositionSide),
				Quantity:          quantity,
				LimitPrice:        limitPrice,
			},
			ReferencePrice:     referencePrice,
			ReferencePriceAt:   time.UnixMilli(record.ReferencePriceAt),
			ReducePositionOnly: record.ReduceOnly,
			Owner: orderdomain.OrderOwner{
				Type: record.Source, StrategyExecutionID: record.StrategyExecutionID,
			},
		},
		ExchangeOrderID: record.ExchangeOrderID,
		FilledQuantity:  filled, AverageFillPrice: average,
		AppliedFills: make(map[shared.FillID]shared.Decimal),
		State:        orderdomain.State(record.State), Version: record.Version,
	}, nil
}

func applyAggregate(record *store.OrderRecord, aggregate orderdomain.Order) {
	record.ExchangeOrderID = aggregate.ExchangeOrderID
	record.State = string(aggregate.State)
	record.FilledQuantity = aggregate.FilledQuantity.String()
	record.AveragePrice = aggregate.AverageFillPrice.String()
	record.Version = aggregate.Version
}

func sameSpec(record store.OrderRecord, spec orderdomain.OrderSpec) bool {
	stored, err := domainOrder(record)
	if err != nil {
		return false
	}
	return stored.Spec.ExchangeAccountID == spec.ExchangeAccountID &&
		stored.Spec.ClientOrderID == spec.ClientOrderID &&
		stored.Spec.InstrumentID == spec.InstrumentID &&
		stored.Spec.Type == spec.Type &&
		stored.Spec.FillPolicy == spec.FillPolicy &&
		stored.Spec.Side == spec.Side &&
		stored.Spec.PositionSide == spec.PositionSide &&
		stored.Spec.Quantity.Cmp(spec.Quantity) == 0 &&
		equalOptionalDecimal(stored.Spec.LimitPrice, spec.LimitPrice)
}

func equalOptionalDecimal(left, right *shared.Decimal) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Cmp(*right) == 0
}

func (s *Service) orderID() string {
	if s.NewOrderID != nil {
		return s.NewOrderID()
	}
	return gonanoid.Must(21)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) String() string {
	return "trade order service"
}
