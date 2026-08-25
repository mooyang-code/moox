package order

import (
	"context"
	"errors"
	"time"

	gonanoid "github.com/matoous/go-nanoid/v2"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/rs/xid"
	"gorm.io/gorm"
)

var (
	ErrIdempotencyConflict = errors.New("trade order: idempotency conflict")
	ErrServiceConfig       = errors.New("trade order: service is not configured")
	ErrCrossZero           = errors.New("trade order: order cannot cross zero")
	ErrExternalConflict    = errors.New("trade order: active external order conflicts with target")
	ErrAutomationPaused    = errors.New("trade order: logical account automation is paused")
	ErrTargetOwnerConflict = errors.New("trade order: target runner no longer owns logical account")
	ErrAccountNotReady     = errors.New("trade order: exchange account is not ready")
)

type AdapterSource interface {
	Adapter(tradingAccountID string) (execution.ExecutionAdapter, error)
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
	unlock := s.Store.LockTradingAccount(spec.TradingAccountID)
	defer unlock()
	if existing, err := s.Store.GetOrderByClientID(
		ctx,
		spaceID,
		spec.TradingAccountID,
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
	// Paper reservations and the persisted MARKET execution price must use the
	// side-executable quote, not a Last-only reference. Revalidate once with the
	// fresh bid/ask so the reservation and matcher consume the same price.
	if validation.Account.ExecutionMode == exchange.ExecutionModePaper {
		if s.Adapters == nil {
			return orderdomain.Order{}, ErrServiceConfig
		}
		adapter, adapterErr := s.Adapters.Adapter(spec.TradingAccountID)
		if adapterErr != nil {
			return orderdomain.Order{}, adapterErr
		}
		marketData, ok := adapter.(execution.MarketDataSource)
		if !ok {
			return orderdomain.Order{}, errors.New("trade order: paper market data source is unavailable")
		}
		quote, quoteErr := marketData.GetQuote(ctx, shared.ExchangeSymbol(validation.Instrument.ExchangeSymbol))
		if quoteErr != nil {
			return orderdomain.Order{}, quoteErr
		}
		if !paperQuoteFresh(quote, s.now(), 10*time.Second) {
			return orderdomain.Order{}, errors.New("trade order: paper quote is stale")
		}
		executable, executableErr := paperExecutablePrice(spec.Side, quote)
		if executableErr != nil {
			return orderdomain.Order{}, executableErr
		}
		spec.ReferencePrice = executable
		spec.ReferencePriceAt = quote.SourceTime
		validation, err = s.Validator.Validate(ctx, spaceID, spec)
		if err != nil {
			return orderdomain.Order{}, err
		}
	}

	id := s.orderID()
	aggregate, _, err := orderdomain.New(shared.OrderID(id), spec)
	if err != nil {
		return orderdomain.Order{}, err
	}
	record := orderRecord(validation, *aggregate)
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if validation.Instrument.InstrumentID != "" &&
			validation.Account.MarketType == exchange.MarketTypeSwap &&
			spec.ReducePositionOnly {
			accountRecord, err := tx.GetTradingAccount(validation.Account.SpaceID, spec.TradingAccountID)
			if err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				accountRecord = store.TradingAccountRecord{}
			}
			instrumentRecord, err := tx.GetInstrumentByIDForAccount(
				validation.Account.SpaceID, spec.TradingAccountID, validation.Instrument.InstrumentID,
			)
			if err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				instrumentRecord = store.InstrumentRecord{}
			}
			if accountRecord.TradingAccountID != "" && instrumentRecord.InstrumentID != "" {
				facts, err := tx.LoadReservationFacts(accountRecord, instrumentRecord, spec)
				if err != nil {
					return err
				}
				if spec.ReducePositionOnly && spec.Quantity.Cmp(facts.AvailableReducibleQuantity) > 0 {
					return ErrReduceOnly
				}
			}
		}
		if !validation.ReservedQuantity.IsZero() {
			unreflected, err := tx.GetUnreflectedReservation(
				validation.Account.SpaceID,
				spec.TradingAccountID,
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
		}
		if err := tx.CreateOrder(record); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			existing, getErr := s.Store.GetOrderByClientID(
				ctx,
				validation.Account.SpaceID,
				spec.TradingAccountID,
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
	account, err := s.Validator.Accounts.ExecutionEligibility(ctx, spec.TradingAccountID)
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
	var position exchange.Position
	var positionErr error
	if scoped, ok := s.Validator.Positions.(AccountPositionSource); ok {
		position, positionErr = scoped.GetPositionForAccount(ctx, spec.TradingAccountID, spec.InstrumentID)
	} else {
		position, positionErr = s.Validator.Positions.GetPosition(ctx, spec.TradingAccountID, spec.InstrumentID)
	}
	if positionErr != nil {
		return spec, positionErr
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
		if spec.Owner.Type == orderdomain.OwnerOperator ||
			spec.Owner.Type == orderdomain.OwnerTarget {
			return spec, ErrCrossZero
		}
		return spec, nil
	}
	spec.ReducePositionOnly = true
	return spec, nil
}

func (s *Service) refreshReducePositionOnly(
	ctx context.Context,
	spec orderdomain.OrderSpec,
) (orderdomain.OrderSpec, error) {
	previous := spec.ReducePositionOnly
	refreshed, err := s.deriveReducePositionOnly(ctx, spec)
	if err != nil {
		return spec, err
	}
	if previous && !refreshed.ReducePositionOnly {
		return spec, ErrReduceOnly
	}
	return refreshed, nil
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
	var result orderdomain.Order
	var synchronize bool
	err = func() error {
		unlockExecution := func() {}
		if record.OwnerType == string(orderdomain.OwnerTarget) &&
			record.LogicalAccountID != "" {
			unlockExecution = s.Store.LockLogicalAccountExecution(
				record.SpaceID,
				record.LogicalAccountID,
			)
		}
		defer unlockExecution()

		unlockAccount := s.Store.LockTradingAccount(record.TradingAccountID)
		defer unlockAccount()
		currentRecord, getErr := s.Store.GetOrder(ctx, spaceID, orderID)
		if getErr != nil {
			return getErr
		}
		current, domainErr := domainOrder(currentRecord)
		if domainErr != nil {
			return domainErr
		}
		switch current.State {
		case orderdomain.Pending:
			if conflictErr := s.rejectTargetExternalConflict(ctx, currentRecord); conflictErr != nil {
				result = current
				return conflictErr
			}
			result, synchronize, domainErr = s.submit(ctx, currentRecord)
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			result, domainErr = s.resolveUnknown(ctx, currentRecord, current)
			synchronize = domainErr == nil && result.State == orderdomain.Open
		case orderdomain.Rejected:
			result = current
			domainErr = errors.New(currentRecord.RejectReason)
		default:
			result = current
		}
		return domainErr
	}()
	if err != nil || !synchronize || s.Syncer == nil {
		return result, err
	}
	if err := s.Syncer.SyncAccount(ctx, record.TradingAccountID); err != nil {
		return result, err
	}
	return s.Get(ctx, record.SpaceID, record.OrderID)
}

func (s *Service) rejectTargetExternalConflict(
	ctx context.Context,
	record store.OrderRecord,
) error {
	if record.OwnerType != string(orderdomain.OwnerTarget) ||
		record.LogicalAccountID == "" {
		return nil
	}
	account, err := s.Store.GetTradingAccountByID(
		ctx,
		record.TradingAccountID,
	)
	if err != nil {
		return err
	}
	if account.Status != string(exchange.AccountStatusEnabled) || !account.Ready {
		return ErrAccountNotReady
	}
	logicalAccount, err := s.Store.GetLogicalAccount(
		ctx,
		record.SpaceID,
		record.LogicalAccountID,
	)
	if err != nil {
		return err
	}
	if logicalAccount.AutomationState != "ACTIVE" {
		return ErrAutomationPaused
	}
	if logicalAccount.OwnerRunnerID == "" ||
		logicalAccount.OwnerRunnerID != record.RunnerID {
		return ErrTargetOwnerConflict
	}
	records, _, err := s.Store.ListOrders(ctx, record.SpaceID, store.OrderQuery{
		LogicalAccountID: record.LogicalAccountID,
		OnlyOpen:         true,
		Limit:            1000,
	})
	if err != nil {
		return err
	}
	for _, current := range records {
		if current.OrderID != record.OrderID &&
			current.OwnerType == string(orderdomain.OwnerExternal) {
			return ErrExternalConflict
		}
	}
	return nil
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
	adapter, err := s.Adapters.Adapter(record.TradingAccountID)
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

	_, callErr := adapter.CancelOrder(ctx, shared.ExchangeSymbol(record.Symbol), record.ClientOrderID)
	if callErr == nil {
		err = s.Syncer.SyncAccount(ctx, record.TradingAccountID)
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
	adapter, err := s.Adapters.Adapter(record.TradingAccountID)
	if err != nil {
		return current, err
	}
	_, callErr := adapter.CancelOrder(ctx, shared.ExchangeSymbol(record.Symbol), record.ClientOrderID)
	if callErr == nil {
		if err := s.Syncer.SyncAccount(ctx, record.TradingAccountID); err != nil {
			return current, err
		}
		return s.Get(ctx, spaceID, orderID)
	}
	if !uncertainExchangeError(callErr) {
		if syncErr := s.Syncer.SyncAccount(ctx, record.TradingAccountID); syncErr == nil {
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
	unlock := s.Store.LockTradingAccount(record.TradingAccountID)
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
	applyAggregate(&record, current)
	record.RemainingReservedQuantity = "0"
	record.FinishedAt = s.now().UnixMilli()
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
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
	unlock := s.Store.LockTradingAccount(record.TradingAccountID)
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
	if err := s.Syncer.SyncAccount(ctx, record.TradingAccountID); err != nil {
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
	adapter, err := s.Adapters.Adapter(record.TradingAccountID)
	if err != nil {
		return current, err
	}
	found, lookupErr := adapter.GetOrder(ctx, shared.ExchangeSymbol(record.Symbol), record.ClientOrderID)
	if lookupErr == nil {
		return s.resolveUnknownFound(ctx, record, current, found.ExchangeOrderID)
	}
	if !exchange.IsKind(lookupErr, exchange.ErrorOrderNotFound) {
		return current, lookupErr
	}
	fills, _, fillsErr := adapter.ListRecentFills(ctx, shared.ExchangeSymbol(record.Symbol), "")
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
	aggregate.Spec, err = s.refreshReducePositionOnly(ctx, aggregate.Spec)
	if err != nil {
		if permanentValidationError(err) {
			rejected, rejectErr := s.rejectPending(ctx, record, aggregate, err)
			return rejected, false, rejectErr
		}
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
	validation, err := validator.Validate(ctx, record.SpaceID, aggregate.Spec)
	if err != nil {
		if permanentValidationError(err) {
			rejected, rejectErr := s.rejectPending(ctx, record, aggregate, err)
			return rejected, false, rejectErr
		}
		return orderdomain.Order{}, false, err
	}
	adapter, err := s.Adapters.Adapter(record.TradingAccountID)
	if err != nil {
		return orderdomain.Order{}, false, err
	}
	expected := aggregate.Version
	if _, err = aggregate.BeginSubmit(); err != nil {
		return orderdomain.Order{}, false, err
	}
	releaseReservationForReduction := !record.ReduceOnly &&
		aggregate.Spec.ReducePositionOnly
	record.ReduceOnly = aggregate.Spec.ReducePositionOnly
	if releaseReservationForReduction {
		record.ReservedAsset = validation.ReservedAsset
		record.ReservedQuantity = validation.ReservedQuantity.String()
		record.RemainingReservedQuantity = validation.ReservedQuantity.String()
	}
	applyAggregate(&record, aggregate)
	record.SubmittedAt = s.now().UnixMilli()
	if err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	}); err != nil {
		return orderdomain.Order{}, false, err
	}

	exchangeSymbol := validation.Instrument.ExchangeSymbol
	if exchangeSymbol == "" {
		exchangeSymbol = aggregate.Spec.InstrumentID
	}
	response, callErr := adapter.PlaceOrder(ctx, exchange.OrderRequest{
		ClientOrderID:  aggregate.Spec.ClientOrderID,
		ExchangeSymbol: exchangeSymbol,
		Symbol:         exchangeSymbol, OrderType: aggregate.Spec.Type,
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
	if current.State == orderdomain.Rejected {
		latest.RemainingReservedQuantity = "0"
		latest.FinishedAt = s.now().UnixMilli()
	}
	if err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateOrder(latest, expected); err != nil {
			return err
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
	applyAggregate(&record, current)
	record.RejectReason = cause.Error()
	record.RemainingReservedQuantity = "0"
	record.FinishedAt = s.now().UnixMilli()
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
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
		ErrCrossZero,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func orderRecord(validation Validation, value orderdomain.Order) store.OrderRecord {
	instrumentID := validation.Instrument.InstrumentID
	exchangeSymbol := validation.Instrument.ExchangeSymbol
	if exchangeSymbol == "" {
		exchangeSymbol = value.Spec.InstrumentID
	}
	var limitPrice *string
	if value.Spec.LimitPrice != nil {
		raw := value.Spec.LimitPrice.String()
		limitPrice = &raw
	}
	record := store.OrderRecord{
		SpaceID: validation.Account.SpaceID, OrderID: string(value.ID),
		TradingAccountID: value.Spec.TradingAccountID,
		ClientOrderID:    value.Spec.ClientOrderID,
		Exchange:         string(validation.Account.Exchange),
		MarketType:       string(validation.Account.MarketType),
		InstrumentID:     instrumentID,
		ExchangeSymbol:   exchangeSymbol,
		Symbol:           exchangeSymbol,
		OrderType:        string(value.Spec.Type), TimeInForce: string(value.Spec.FillPolicy),
		Side: string(value.Spec.Side), PositionSide: string(value.Spec.PositionSide),
		Quantity: value.Spec.Quantity.String(), LimitPrice: limitPrice,
		ReferencePrice:   value.Spec.ReferencePrice.String(),
		ReferencePriceAt: value.Spec.ReferencePriceAt.UnixMilli(),
		ReduceOnly:       value.Spec.ReducePositionOnly,
		OwnerType:        string(value.Spec.Owner.Type), OwnerID: value.Spec.Owner.OwnerID,
		LogicalAccountID: value.Spec.Owner.LogicalAccountID,
		RunnerID:         ownerRunnerID(value.Spec.Owner),
		State:            string(value.State), FilledQuantity: "0", AveragePrice: "0",
		ReservedAsset:             validation.ReservedAsset,
		ReservedQuantity:          validation.ReservedQuantity.String(),
		RemainingReservedQuantity: validation.ReservedQuantity.String(),
		Version:                   value.Version,
	}
	if validation.Account.ExecutionMode == exchange.ExecutionModePaper {
		if value.Spec.Type == exchange.OrderTypeMarket {
			price := value.Spec.ReferencePrice
			if validation.Account.Paper != nil {
				price = paperExecutionPrice(price, value.Spec.Side, validation.Account.Paper.SlippageBPS)
			}
			raw := price.String()
			record.PaperExecutionPrice = &raw
		}
		record.FirstMatchPending = value.Spec.Type == exchange.OrderTypeMarket ||
			value.Spec.Type == exchange.OrderTypeLimit
	}
	return record
}

func paperExecutionPrice(reference shared.Decimal, side exchange.Side, slippage shared.Decimal) shared.Decimal {
	if slippage.Cmp(shared.Zero()) <= 0 {
		return reference
	}
	factor := shared.MustDecimal("1").Add(slippage.Div(shared.MustDecimal("10000")))
	if side == exchange.SideSell {
		factor = shared.MustDecimal("1").Sub(slippage.Div(shared.MustDecimal("10000")))
	}
	return reference.Mul(factor)
}

func paperExecutablePrice(side exchange.Side, quote execution.MarketQuote) (shared.Decimal, error) {
	if side != exchange.SideBuy && side != exchange.SideSell {
		return shared.Decimal{}, errors.New("trade order: paper order side is required")
	}
	price := quote.Ask
	if side == exchange.SideSell {
		price = quote.Bid
	}
	if price.IsZero() {
		price = quote.Last
	}
	if price.Cmp(shared.Zero()) <= 0 {
		return shared.Decimal{}, errors.New("trade order: paper executable quote is empty")
	}
	return price, nil
}

func paperQuoteFresh(quote execution.MarketQuote, now time.Time, maxAge time.Duration) bool {
	return !quote.SourceTime.IsZero() && !quote.SourceTime.After(now) && now.Sub(quote.SourceTime) <= maxAge
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
	instrumentID := record.InstrumentID
	if instrumentID == "" {
		instrumentID = record.ExchangeSymbol
	}
	return orderdomain.Order{
		ID: shared.OrderID(record.OrderID),
		Spec: orderdomain.OrderSpec{
			ClientOrderSpec: orderdomain.ClientOrderSpec{
				TradingAccountID: record.TradingAccountID,
				ClientOrderID:    record.ClientOrderID,
				InstrumentID:     instrumentID,
				Type:             exchange.OrderType(record.OrderType),
				FillPolicy:       exchange.FillPolicy(record.TimeInForce),
				Side:             exchange.Side(record.Side),
				PositionSide:     exchange.PositionSide(record.PositionSide),
				Quantity:         quantity,
				LimitPrice:       limitPrice,
			},
			ReferencePrice:     referencePrice,
			ReferencePriceAt:   time.UnixMilli(record.ReferencePriceAt),
			ReducePositionOnly: record.ReduceOnly,
			Owner: orderdomain.OrderOwner{
				Type: orderdomain.OwnerType(record.OwnerType), OwnerID: record.OwnerID,
				LogicalAccountID: record.LogicalAccountID,
				RunnerID:         optionalString(record.RunnerID),
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
	instrumentMatches := stored.Spec.InstrumentID == spec.InstrumentID ||
		record.InstrumentID == spec.InstrumentID ||
		record.ExchangeSymbol == spec.InstrumentID
	return stored.Spec.TradingAccountID == spec.TradingAccountID &&
		stored.Spec.ClientOrderID == spec.ClientOrderID &&
		instrumentMatches &&
		stored.Spec.Type == spec.Type &&
		stored.Spec.FillPolicy == spec.FillPolicy &&
		stored.Spec.Side == spec.Side &&
		stored.Spec.PositionSide == spec.PositionSide &&
		stored.Spec.Quantity.Cmp(spec.Quantity) == 0 &&
		equalOptionalDecimal(stored.Spec.LimitPrice, spec.LimitPrice) &&
		stored.Spec.Owner.Type == spec.Owner.Type &&
		stored.Spec.Owner.OwnerID == spec.Owner.OwnerID &&
		stored.Spec.Owner.LogicalAccountID == spec.Owner.LogicalAccountID &&
		equalOptionalString(stored.Spec.Owner.RunnerID, spec.Owner.RunnerID)
}

func equalOptionalDecimal(left, right *shared.Decimal) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Cmp(*right) == 0
}

func ownerRunnerID(owner orderdomain.OrderOwner) string {
	if owner.RunnerID == nil {
		return ""
	}
	return *owner.RunnerID
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
