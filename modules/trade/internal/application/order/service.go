package order

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	gonanoid "github.com/matoous/go-nanoid/v2"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/rs/xid"
	"gorm.io/gorm"
)

var (
	ErrIdempotencyConflict = errors.New("trade order: idempotency conflict")
	ErrServiceConfig       = errors.New("trade order: service is not configured")
	ErrCrossZero           = errors.New("订单不能跨过零持仓")
	ErrExternalConflict    = errors.New("存在外部活动订单")
	ErrAutomationPaused    = errors.New("组合账户自动执行已暂停")
	ErrTargetOwnerConflict = errors.New("trade order: target runner no longer owns logical account")
	ErrTargetExpired       = errors.New("trade order: target validity window elapsed")
	ErrAccountNotReady     = errors.New("账户未就绪")
)

type AdapterSource interface {
	Adapter(tradingAccountID string) (execution.ExecutionAdapter, error)
}

type AccountSyncer interface {
	SyncAccount(context.Context, string) error
}

// LightSyncer 只读持仓和账户恢复准入。同步器实现时，下单之后和余额拒单之后用它，不读全部挂单。
type LightSyncer interface {
	SyncAccountLight(context.Context, string) error
}

func (s *Service) syncLight(ctx context.Context, tradingAccountID string) error {
	if light, ok := s.Syncer.(LightSyncer); ok {
		return light.SyncAccountLight(ctx, tradingAccountID)
	}
	return s.Syncer.SyncAccount(ctx, tradingAccountID)
}

type CancelConfirmer interface {
	ConfirmCancel(context.Context, string, string) error
}

// SymbolConfigurer 在首次开仓前把杠杆设置到交易所。
type SymbolConfigurer interface {
	EnsureSymbolConfig(ctx context.Context, tradingAccountID, symbol string, leverage shared.Decimal) error
}

type Service struct {
	SymbolConfig        SymbolConfigurer
	Store               *store.Store
	Validator           Validator
	Adapters            AdapterSource
	Syncer              AccountSyncer
	NewOrderID          func() string
	Now                 func() time.Time
	UnknownLookupWindow time.Duration
	// RequestSync 请求账户会话做一次防抖轻量同步，返回 false 时由服务自己同步。
	RequestSync func(tradingAccountID string) bool

	// lookups 记录批量解析未知订单时每笔最近一次查询的时间，用于限制查询次数。
	lookupMu sync.Mutex
	lookups  map[string]time.Time
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
	unlock, err := s.Store.LockTradingAccountContext(ctx, spec.TradingAccountID)
	if err != nil {
		return orderdomain.Order{}, accountExecutionError(spec.TradingAccountID, "place_lock", err)
	}
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
	spec, err = s.deriveReducePositionOnly(ctx, spec)
	if err != nil {
		return orderdomain.Order{}, err
	}
	validation, err := s.Validator.validate(ctx, spaceID, spec, true)
	if err != nil {
		return orderdomain.Order{}, err
	}
	// Paper reservations and the persisted MARKET execution price must use the
	// side-executable quote, not a Last-only reference. Revalidate once with the
	// fresh bid/ask so the reservation and matcher consume the same price.
	if validation.Account.ExecutionMode == exchange.ExecutionModePaper {
		spec, err = s.paperReference(ctx, spec, validation)
		if err != nil {
			return orderdomain.Order{}, err
		}
		validation, err = s.Validator.validate(ctx, spaceID, spec, true)
		if err != nil {
			return orderdomain.Order{}, err
		}
	}

	marginAdjustment, err := s.fundsMarginAdjustment(ctx, validation)
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
		if validation.Instrument.InstrumentID != "" &&
			exchange.RuleFor(validation.Account.MarketType).ReduceOnly &&
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
			if validation.Account.ExecutionMode == exchange.ExecutionModePaper {
				if err := checkPaperFunds(tx, validation, marginAdjustment, validation.ReservedQuantity, "", ""); err != nil {
					return err
				}
				return tx.CreateOrder(record)
			}
			available, err := availableReservationFunds(tx, validation, marginAdjustment)
			if err != nil {
				return err
			}
			if available.Cmp(validation.ReservedQuantity) < 0 {
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

func (s *Service) oppositeDust(
	ctx context.Context,
	account tradingaccount.Account,
	spec orderdomain.OrderSpec,
	current shared.Decimal,
) bool {
	if s.Store == nil {
		return false
	}
	instrument, err := s.Store.GetInstrumentByIDForAccount(ctx, account.SpaceID, account.ID, spec.InstrumentID)
	if err != nil {
		return false
	}
	step, stepErr := shared.ParseDecimal(instrument.ExchangeQuantityStep)
	minimum, minimumErr := shared.ParseDecimal(instrument.MinExchangeQuantity)
	contractValue, valueErr := shared.ParseDecimal(instrument.ContractValue)
	if stepErr != nil || minimumErr != nil || valueErr != nil || contractValue.Cmp(shared.Zero()) <= 0 {
		return false
	}
	contracts := current.Abs().Div(contractValue)
	return contracts.Cmp(step) < 0 || contracts.Cmp(minimum) < 0
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
	if !exchange.RuleFor(account.MarketType).ReduceOnly {
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
		// 下不了单的反向零头不算持仓，目标要能越过它加仓。
		if spec.Owner.Type == orderdomain.OwnerTarget && s.oppositeDust(ctx, account, spec, current) {
			return spec, nil
		}
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
			// 完整同步可能长时间占着执行锁：等待必须响应调用方的期限，超时后返回可重试错误，
			// 否则串行的目标 worker 会被一个账户卡住，后面的健康账户也得不到处理。
			unlock, lockErr := s.Store.LockLogicalAccountExecutionContext(
				ctx,
				record.SpaceID,
				record.LogicalAccountID,
			)
			if lockErr != nil {
				return &AccountExecutionError{TradingAccountID: record.TradingAccountID, Operation: "submit_execution_lock", Err: lockErr}
			}
			unlockExecution = unlock
		}
		defer unlockExecution()

		unlockAccount, lockErr := s.Store.LockTradingAccountContext(ctx, record.TradingAccountID)
		if lockErr != nil {
			return &AccountExecutionError{TradingAccountID: record.TradingAccountID, Operation: "submit_lock", Err: lockErr}
		}
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
	if result.State == orderdomain.Rejected && exchange.IsKind(err, exchange.ErrorInsufficientBalance) && s.Syncer != nil {
		// A definitive balance rejection invalidates the local capacity estimate.
		// Refresh outside execution locks; the next convergence may choose another
		// member, but this call must not submit a second child or mask the rejection.
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		return result, errors.Join(err, s.syncLight(refreshCtx, record.TradingAccountID))
	}
	if err != nil || !synchronize || s.Syncer == nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := s.syncLight(ctx, record.TradingAccountID); err != nil {
		return result, err
	}
	return s.Get(ctx, record.SpaceID, record.OrderID)
}

func (s *Service) rejectTargetExternalConflict(
	ctx context.Context,
	record store.OrderRecord,
) error {
	if err := s.authorizeTargetSubmit(ctx, record); err != nil {
		return err
	}
	if record.OwnerType != string(orderdomain.OwnerTarget) ||
		record.LogicalAccountID == "" {
		return nil
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

type authorizationReuseKey struct{}

type authorizationReuse struct {
	mu      sync.Mutex
	results map[string]error
}

// withAuthorizationReuse 让批量提交的一个阶段内，同一执行账户、同一目标的授权检查只做一次。
// 准备阶段和发送前各用一份：准备期间目标变了，发送前那次新检查仍会拦住，不会有子单发出。
func withAuthorizationReuse(ctx context.Context) context.Context {
	return context.WithValue(ctx, authorizationReuseKey{}, &authorizationReuse{results: make(map[string]error)})
}

func (s *Service) authorizeTargetSubmit(
	ctx context.Context,
	record store.OrderRecord,
) error {
	if record.OwnerType != string(orderdomain.OwnerTarget) ||
		record.LogicalAccountID == "" {
		return nil
	}
	key := strings.Join([]string{record.SpaceID, record.TradingAccountID, record.LogicalAccountID, record.RunnerID, record.OwnerID}, "\x00")
	return reuseAuthorization(ctx, key, func() error { return s.checkTargetAuthorization(ctx, record) })
}

// reuseAuthorization 在 ctx 带有复用范围时，同一 key 只调用一次 check 并复用结果（包括错误）。
func reuseAuthorization(ctx context.Context, key string, check func() error) error {
	reuse, _ := ctx.Value(authorizationReuseKey{}).(*authorizationReuse)
	if reuse == nil {
		return check()
	}
	reuse.mu.Lock()
	defer reuse.mu.Unlock()
	if err, ok := reuse.results[key]; ok {
		return err
	}
	err := check()
	reuse.results[key] = err
	return err
}

func (s *Service) checkTargetAuthorization(
	ctx context.Context,
	record store.OrderRecord,
) error {
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
	if logicalAccount.ControlMode != "STRATEGY" {
		return ErrTargetOwnerConflict
	}
	if logicalAccount.AutomationState != "ACTIVE" {
		return ErrAutomationPaused
	}
	if logicalAccount.OwnerInstanceID == "" || logicalAccount.OwnerInstanceID != record.RunnerID {
		return ErrTargetOwnerConflict
	}
	// A target order may be prepared long before Submit. Resolve the current
	// target and re-check its session/validity immediately before the exchange
	// call so a stale order cannot cross a Strategy lifecycle boundary.
	currentTarget, targetErr := s.Store.GetLogicalAccountTarget(ctx, record.SpaceID, record.LogicalAccountID)
	if targetErr != nil {
		if errors.Is(targetErr, gorm.ErrRecordNotFound) {
			if logicalAccount.OwnerSessionID != "" {
				return ErrTargetOwnerConflict
			}
		} else {
			return targetErr
		}
	}
	if targetErr == nil && currentTarget.TargetID != record.OwnerID {
		return ErrTargetOwnerConflict
	}
	if targetErr == nil && currentTarget.InstanceID != "" {
		if logicalAccount.OwnerInstanceID != currentTarget.InstanceID || logicalAccount.OwnerSessionID != currentTarget.SessionID {
			return ErrTargetOwnerConflict
		}
		now := s.now().UTC().UnixMilli()
		if now < currentTarget.EffectiveAt || now >= currentTarget.ValidUntil {
			return ErrTargetExpired
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
		return orderdomain.Order{}, accountExecutionError(record.TradingAccountID, "adapter", err)
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

	response, callErr := adapter.CancelOrder(ctx, shared.ExchangeSymbol(record.ExchangeSymbol), record.ClientOrderID)
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer persistCancel()
	ctx = persistCtx
	if callErr == nil {
		telemetry.OrderCancels.WithLabelValues(record.TradingAccountID).Inc()
		account, accountErr := s.Store.GetTradingAccountByID(ctx, record.TradingAccountID)
		if accountErr != nil {
			return orderdomain.Order{}, accountErr
		}
		if account.ExecutionMode == string(exchange.ExecutionModePaper) &&
			response.Status == exchange.OrderStatusCanceled {
			if confirmer, ok := s.Syncer.(CancelConfirmer); ok {
				if confirmErr := confirmer.ConfirmCancel(ctx, record.SpaceID, record.OrderID); confirmErr != nil {
					return orderdomain.Order{}, confirmErr
				}
				if syncErr := s.Syncer.SyncAccount(ctx, record.TradingAccountID); syncErr != nil {
					return orderdomain.Order{}, syncErr
				}
				return s.Get(ctx, record.SpaceID, record.OrderID)
			}
		}
		err = s.Syncer.SyncAccount(ctx, record.TradingAccountID)
		return aggregate, err
	}

	latest, getErr := s.Store.GetOrder(persistCtx, spaceID, orderID)
	if getErr != nil {
		return orderdomain.Order{}, getErr
	}
	current, getErr := domainOrder(latest)
	if getErr != nil {
		return orderdomain.Order{}, getErr
	}
	// A private execution update can settle the order while CancelOrder is in
	// flight. The persisted terminal state is authoritative over its late error.
	if current.State.Terminal() {
		return current, nil
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
	if err = s.Store.Transaction(persistCtx, func(tx *store.Tx) error {
		return tx.UpdateOrder(latest, expected)
	}); err != nil {
		return s.cancelUpdateError(persistCtx, spaceID, orderID, err)
	}
	return current, accountExecutionError(record.TradingAccountID, "cancel", callErr)
}

func (s *Service) RecoverCancel(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
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
		return current, accountExecutionError(record.TradingAccountID, "adapter", err)
	}
	found, lookupErr := adapter.GetOrder(ctx, shared.ExchangeSymbol(record.ExchangeSymbol), record.ClientOrderID)
	if lookupErr != nil {
		return current, accountExecutionError(record.TradingAccountID, "cancel_lookup", lookupErr)
	}
	if found.Status != exchange.OrderStatusOpen && found.Status != exchange.OrderStatusPartiallyFilled {
		// Only the sync/reducer pipeline can import fills and release resources.
		// A terminal lookup suppresses another cancel even while those facts lag.
		if err := ctx.Err(); err != nil {
			return current, accountExecutionError(record.TradingAccountID, "cancel_sync", err)
		}
		if err := s.Syncer.SyncAccount(ctx, record.TradingAccountID); err != nil {
			return current, err
		}
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		return s.Get(readCtx, spaceID, orderID)
	}
	latest, err := s.Get(ctx, spaceID, orderID)
	if err != nil {
		return current, err
	}
	if latest.State.Terminal() {
		return latest, nil
	}
	if err := ctx.Err(); err != nil {
		return latest, accountExecutionError(record.TradingAccountID, "cancel_lookup", err)
	}
	ioCtx := ctx
	_, callErr := adapter.CancelOrder(ctx, shared.ExchangeSymbol(record.ExchangeSymbol), record.ClientOrderID)
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer persistCancel()
	if callErr == nil {
		if err := ioCtx.Err(); err != nil {
			return current, accountExecutionError(record.TradingAccountID, "cancel_sync", err)
		}
		if err := s.Syncer.SyncAccount(ioCtx, record.TradingAccountID); err != nil {
			return current, err
		}
		return s.Get(persistCtx, spaceID, orderID)
	}
	if exchange.NotAccepted(callErr) {
		// 限流期间撤单没有发出，状态保持不变，下一轮再试。
		return current, accountExecutionError(record.TradingAccountID, "cancel", callErr)
	}
	if !uncertainExchangeError(callErr) {
		if err := ioCtx.Err(); err != nil {
			return current, errors.Join(accountExecutionError(record.TradingAccountID, "cancel", callErr), accountExecutionError(record.TradingAccountID, "cancel_sync", err))
		}
		if err := s.Syncer.SyncAccount(ioCtx, record.TradingAccountID); err != nil {
			return current, errors.Join(accountExecutionError(record.TradingAccountID, "cancel", callErr), err)
		}
	}
	// Even an uncertain response can arrive after a private terminal update.
	record, err = s.Store.GetOrder(persistCtx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	current, err = domainOrder(record)
	if err != nil {
		return orderdomain.Order{}, err
	}
	if current.State.Terminal() ||
		(current.State != orderdomain.Canceling && current.State != orderdomain.CancelUnknown) {
		return current, nil
	}
	expected := current.Version
	switch {
	case uncertainExchangeError(callErr) && current.State == orderdomain.Canceling:
		_, err = current.MarkCancelUnknown()
	case uncertainExchangeError(callErr):
		return current, accountExecutionError(record.TradingAccountID, "cancel", callErr)
	case current.State == orderdomain.Canceling:
		_, err = current.CancelRejected()
	default:
		_, err = current.CancelStillOpen()
	}
	if err != nil {
		return current, err
	}
	applyAggregate(&record, current)
	if err := s.Store.Transaction(persistCtx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	}); err != nil {
		return s.cancelUpdateError(persistCtx, spaceID, orderID, err)
	}
	return current, accountExecutionError(record.TradingAccountID, "cancel", callErr)
}

func (s *Service) cancelUpdateError(ctx context.Context, spaceID, orderID string, updateErr error) (orderdomain.Order, error) {
	if errors.Is(updateErr, store.ErrConflict) {
		latest, err := s.Get(ctx, spaceID, orderID)
		if err != nil {
			return orderdomain.Order{}, err
		}
		if latest.State.Terminal() {
			return latest, nil
		}
	}
	return orderdomain.Order{}, updateErr
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
	unlock, err := s.Store.LockTradingAccountContext(ctx, record.TradingAccountID)
	if err != nil {
		return orderdomain.Order{}, &AccountExecutionError{TradingAccountID: record.TradingAccountID, Operation: "discard_pending_lock", Err: err}
	}
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
	observeOrder(record.TradingAccountID, telemetry.OrderDiscarded)
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
	wasUnknown := record.State == string(orderdomain.Submitting) ||
		record.State == string(orderdomain.SubmitUnknown)
	resolved, err := s.resolveUnknownLocked(ctx, record)
	// 尾部同步在放锁之后：同步要拿执行锁和账户锁。
	if err != nil || !wasUnknown || resolved.State != orderdomain.Open ||
		s.Syncer == nil {
		return resolved, err
	}
	if err := s.Syncer.SyncAccount(ctx, record.TradingAccountID); err != nil {
		return resolved, err
	}
	return s.Get(ctx, record.SpaceID, record.OrderID)
}

// resolveUnknownLocked 持锁解析一笔未知订单，不做尾部同步。目标子单先拿组合账户的执行锁：
// 批量发送时账户锁不跨请求，只有执行锁能保证发送中的 SUBMITTING 不被当成崩溃残留。
func (s *Service) resolveUnknownLocked(ctx context.Context, record store.OrderRecord) (orderdomain.Order, error) {
	if record.OwnerType == string(orderdomain.OwnerTarget) && record.LogicalAccountID != "" {
		unlockExecution, err := s.Store.LockLogicalAccountExecutionContext(ctx, record.SpaceID, record.LogicalAccountID)
		if err != nil {
			return orderdomain.Order{}, &AccountExecutionError{TradingAccountID: record.TradingAccountID, Operation: "resolve_unknown_lock", Err: err}
		}
		defer unlockExecution()
	}
	unlock, err := s.Store.LockTradingAccountContext(ctx, record.TradingAccountID)
	if err != nil {
		return orderdomain.Order{}, &AccountExecutionError{TradingAccountID: record.TradingAccountID, Operation: "resolve_unknown_lock", Err: err}
	}
	defer unlock()
	record, err = s.Store.GetOrder(ctx, record.SpaceID, record.OrderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	current, err := domainOrder(record)
	if err != nil {
		return current, err
	}
	return s.resolveUnknown(ctx, record, current)
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
		return current, accountExecutionError(record.TradingAccountID, "adapter", err)
	}
	found, lookupErr := adapter.GetOrder(ctx, shared.ExchangeSymbol(record.ExchangeSymbol), record.ClientOrderID)
	if lookupErr == nil {
		return s.resolveUnknownFound(ctx, record, current, found.ExchangeOrderID)
	}
	if !exchange.IsKind(lookupErr, exchange.ErrorOrderNotFound) {
		return current, accountExecutionError(record.TradingAccountID, "get_order", lookupErr)
	}
	// 从已同步的位置往后找，不重新拉全部历史。
	cursor := ""
	if account, accountErr := s.Store.GetTradingAccountByID(ctx, record.TradingAccountID); accountErr == nil {
		cursor = account.FillCursors[record.ExchangeSymbol]
	}
	fills, _, fillsErr := adapter.ListRecentFills(ctx, shared.ExchangeSymbol(record.ExchangeSymbol), cursor)
	if fillsErr != nil {
		return current, accountExecutionError(record.TradingAccountID, "list_fills", fillsErr)
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

// freshReference 取交易所新鲜报价并刷新订单的参考价；取不到或已过期都返回可重试错误。
func (s *Service) freshReference(
	ctx context.Context,
	record store.OrderRecord,
	spec orderdomain.OrderSpec,
	maxAge time.Duration,
) (orderdomain.OrderSpec, error) {
	adapter, err := s.Adapters.Adapter(record.TradingAccountID)
	if err != nil {
		return spec, accountExecutionError(record.TradingAccountID, "adapter", err)
	}
	quote, err := adapter.GetQuote(ctx, shared.ExchangeSymbol(record.ExchangeSymbol))
	if err != nil {
		return spec, accountExecutionError(record.TradingAccountID, "quote", err)
	}
	if !paperQuoteFresh(quote, s.now(), maxAge) {
		return spec, accountExecutionError(record.TradingAccountID, "quote", orderdomain.ErrReferencePriceStale)
	}
	price, err := paperExecutablePrice(spec.Side, quote)
	if err != nil {
		return spec, accountExecutionError(record.TradingAccountID, "quote", err)
	}
	spec.ReferencePrice, spec.ReferencePriceAt = price, quote.SourceTime
	return spec, nil
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

// submitSend 是已经落为 SUBMITTING、等待发往交易所的订单。
type submitSend struct {
	record     store.OrderRecord
	aggregate  orderdomain.Order
	adapter    execution.ExecutionAdapter
	validation Validation
	// closeReadiness 表示回执带成交、账户需要关闭准入。批量发送时由批次在全部发出后统一关闭，
	// 否则先回来的回执会挡住同批后面还没发出的单。
	closeReadiness bool
}

// submit 是串行提交：准备、发送、记录都在调用方持有的锁里完成。
func (s *Service) submit(
	ctx context.Context,
	record store.OrderRecord,
) (orderdomain.Order, bool, error) {
	send, result, err := s.prepareSubmit(ctx, record)
	if send == nil {
		return result, false, err
	}
	return s.deliverSubmit(ctx, send, nil)
}

// prepareSubmit 完成提交前的全部检查并把订单落为 SUBMITTING。返回空的 send 表示本地已经处理完
// （拒单、暂缓或出错），result 和 err 就是结果。调用方要持有账户锁。
func (s *Service) prepareSubmit(
	ctx context.Context,
	record store.OrderRecord,
) (*submitSend, orderdomain.Order, error) {
	aggregate, err := domainOrder(record)
	if err != nil {
		return nil, orderdomain.Order{}, err
	}
	aggregate.Spec, err = s.refreshReducePositionOnly(ctx, aggregate.Spec)
	if err != nil {
		if permanentValidationError(err) {
			rejected, rejectErr := s.rejectPending(ctx, record, aggregate, err)
			return nil, rejected, rejectErr
		}
		return nil, orderdomain.Order{}, err
	}
	validator := s.Validator
	retryRepriced := false
	originalReference, originalReferenceAt := aggregate.Spec.ReferencePrice, aggregate.Spec.ReferencePriceAt
	if record.SubmittedAt > 0 {
		if aggregate.Spec.Type == exchange.OrderTypeMarket {
			// 确认不存在后的重试：订单身份（参考价、数量）保持不变，但限额与资金预留都按报价折算，旧报价可能已大幅偏离。
			// 取新鲜报价（取不到就不提交，保持待重试），校验与预留按新旧报价中较高者保守计算；
			// 持久化的参考价仍是原值，只有预留金额按需增大。
			fresh, freshErr := s.freshReference(ctx, record, aggregate.Spec, validator.MaxReferenceAge)
			if freshErr != nil {
				return nil, aggregate, freshErr
			}
			if fresh.ReferencePrice.Cmp(originalReference) < 0 {
				fresh.ReferencePrice = originalReference
			}
			aggregate.Spec = fresh
			retryRepriced = true
			// 校验时间取报价返回之后：报价请求期间产生的时间戳不能比冻结的校验时间更新。
			now := s.now()
			validator.Now = func() time.Time { return now }
		} else {
			// 限价单的名义金额来自限价而非报价：沿用已校验的报价，避免未知查询窗口让每次受控重试都变成过期报价。
			now := s.now()
			validator.Now = func() time.Time { return now }
			validator.MaxReferenceAge = now.Sub(aggregate.Spec.ReferencePriceAt) + time.Second
		}
	}
	validation, err := validator.validate(ctx, record.SpaceID, aggregate.Spec, true)
	if retryRepriced {
		aggregate.Spec.ReferencePrice, aggregate.Spec.ReferencePriceAt = originalReference, originalReferenceAt
	}
	refreshedReference := false
	if errors.Is(err, orderdomain.ErrReferencePriceStale) && record.SubmittedAt == 0 &&
		aggregate.Spec.Owner.Type == orderdomain.OwnerOperator {
		adapter, adapterErr := s.Adapters.Adapter(record.TradingAccountID)
		if adapterErr != nil {
			return nil, aggregate, accountExecutionError(record.TradingAccountID, "adapter", adapterErr)
		}
		quote, quoteErr := adapter.GetQuote(ctx, shared.ExchangeSymbol(record.ExchangeSymbol))
		if quoteErr != nil {
			return nil, aggregate, accountExecutionError(record.TradingAccountID, "quote", quoteErr)
		}
		if !paperQuoteFresh(quote, s.now(), validator.MaxReferenceAge) {
			return nil, aggregate, accountExecutionError(record.TradingAccountID, "quote", orderdomain.ErrReferencePriceStale)
		}
		price, priceErr := paperExecutablePrice(aggregate.Spec.Side, quote)
		if priceErr != nil {
			return nil, aggregate, accountExecutionError(record.TradingAccountID, "quote", priceErr)
		}
		aggregate.Spec.ReferencePrice, aggregate.Spec.ReferencePriceAt = price, quote.SourceTime
		validation, err = validator.validate(ctx, record.SpaceID, aggregate.Spec, true)
		refreshedReference = err == nil
	}
	if err != nil {
		if permanentValidationError(err) {
			rejected, rejectErr := s.rejectPending(ctx, record, aggregate, err)
			return nil, rejected, rejectErr
		}
		return nil, orderdomain.Order{}, err
	}
	marginAdjustment, err := s.fundsMarginAdjustment(ctx, validation)
	if err != nil {
		return nil, aggregate, err
	}
	adapter, err := s.Adapters.Adapter(record.TradingAccountID)
	if err != nil {
		return nil, orderdomain.Order{}, accountExecutionError(record.TradingAccountID, "adapter", err)
	}
	// Place and margin preparation may perform account-scoped I/O. Re-read the
	// target after those calls and before persisting SUBMITTING so a target that
	// expired meanwhile remains a local PENDING intent and never reaches POST.
	if err := s.authorizeTargetSubmit(ctx, record); err != nil {
		return nil, aggregate, err
	}
	if err := ctx.Err(); err != nil {
		return nil, aggregate, err
	}
	if s.SymbolConfig != nil && validation.Account.ExecutionMode == exchange.ExecutionModeLive &&
		exchange.RuleFor(validation.Account.MarketType).Leverage && !aggregate.Spec.ReducePositionOnly {
		if err := s.SymbolConfig.EnsureSymbolConfig(ctx, record.TradingAccountID, record.ExchangeSymbol, validation.Leverage); err != nil {
			if !exchange.IsKind(err, exchange.ErrorRejected) {
				return nil, aggregate, err
			}
			// 交易所明确拒绝：写入提交时间，拒单规则才会把它计入。
			record.SubmittedAt = s.now().UnixMilli()
			rejected, rejectErr := s.rejectPending(ctx, record, aggregate, err)
			return nil, rejected, rejectErr
		}
	}
	expected := aggregate.Version
	if _, err = aggregate.BeginSubmit(); err != nil {
		return nil, orderdomain.Order{}, err
	}
	releaseReservationForReduction := !record.ReduceOnly &&
		aggregate.Spec.ReducePositionOnly
	record.ReduceOnly = aggregate.Spec.ReducePositionOnly
	oldReservedAsset, oldRemaining := record.ReservedAsset, record.RemainingReservedQuantity
	if refreshedReference && validation.Account.ExecutionMode == exchange.ExecutionModePaper && aggregate.Spec.Type == exchange.OrderTypeMarket {
		price := aggregate.Spec.ReferencePrice
		if record.PaperExecutionPrice != nil {
			previous, parseErr := shared.ParseDecimal(*record.PaperExecutionPrice)
			if parseErr != nil {
				return nil, aggregate, parseErr
			}
			reference, parseErr := shared.ParseDecimal(record.ReferencePrice)
			if parseErr != nil || reference.Cmp(shared.Zero()) <= 0 {
				return nil, aggregate, store.ErrInvalidRecord
			}
			// Preserve the slippage fixed at Place, even if account settings changed.
			price = previous.Mul(price).Div(reference)
		} else if validation.Account.Paper != nil {
			price = paperExecutionPrice(price, aggregate.Spec.Side, validation.Account.Paper.SlippageBPS)
		}
		raw := price.String()
		record.PaperExecutionPrice = &raw
		// Repricing preserves the original execution slippage. Reserve against
		// that same price, not a newly edited account slippage setting.
		feeRate := validator.FeeBufferRate
		if validation.Account.Paper != nil {
			feeRate = maxDecimal(validation.Account.Paper.MakerFeeRate, validation.Account.Paper.TakerFeeRate)
		}
		notional := aggregate.Spec.Quantity.Mul(price)
		if exchange.RuleFor(validation.Account.MarketType).UsesMargin {
			validation.ReservedQuantity = notional.Mul(feeRate)
			if !aggregate.Spec.ReducePositionOnly {
				validation.ReservedQuantity = validation.ReservedQuantity.Add(notional.Div(validation.Leverage))
			}
		} else if aggregate.Spec.Side == exchange.SideBuy {
			validation.ReservedQuantity = withFeeBuffer(notional, feeRate)
		}
	}
	if releaseReservationForReduction || refreshedReference || retryRepriced {
		record.ReservedAsset = validation.ReservedAsset
		record.ReservedQuantity = validation.ReservedQuantity.String()
		record.RemainingReservedQuantity = validation.ReservedQuantity.String()
	}
	applyAggregate(&record, aggregate)
	record.SubmittedAt = s.now().UnixMilli()
	if err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if validation.Account.ExecutionMode == exchange.ExecutionModePaper {
			required, err := shared.ParseDecimal(record.RemainingReservedQuantity)
			if err != nil {
				return err
			}
			if !required.IsZero() {
				if err := checkPaperFunds(tx, validation, marginAdjustment, required, oldReservedAsset, oldRemaining); err != nil {
					return err
				}
			}
		} else if refreshedReference || retryRepriced {
			unreflected, err := tx.GetUnreflectedReservation(record.SpaceID, record.TradingAccountID, validation.ReservedAsset, validation.Account.SnapshotSourceTime.UnixMilli())
			if err != nil {
				return err
			}
			if oldReservedAsset == validation.ReservedAsset {
				previous, parseErr := shared.ParseDecimal(oldRemaining)
				if parseErr != nil {
					return parseErr
				}
				unreflected = unreflected.Sub(previous)
			}
			available := availableBalance(validation.Account.Snapshot, validation.ReservedAsset)
			if exchange.RuleFor(validation.Account.MarketType).UsesMargin {
				available = validation.Account.Snapshot.AvailableFunds
			}
			if available.Cmp(unreflected.Add(validation.ReservedQuantity)) < 0 {
				return ErrInsufficientFunds
			}
		}
		if refreshedReference {
			record.ReferencePrice = aggregate.Spec.ReferencePrice.String()
			record.ReferencePriceAt = aggregate.Spec.ReferencePriceAt.UnixMilli()
			if err := tx.UpdatePendingOrderReference(record, expected); err != nil {
				return err
			}
		}
		return tx.UpdateOrder(record, expected)
	}); err != nil {
		return nil, orderdomain.Order{}, err
	}
	return &submitSend{record: record, aggregate: aggregate, adapter: adapter, validation: validation}, orderdomain.Order{}, nil
}

// deliverSubmit 把已经落为 SUBMITTING 的订单发往交易所并记录结果。发送前再确认一次目标授权，
// 失败就退回待提交、不发请求。lockAccount 不为空时只在记录结果时持有账户锁，用于批量并发发送。
func (s *Service) deliverSubmit(
	ctx context.Context,
	send *submitSend,
	lockAccount func(context.Context) (func(), error),
) (orderdomain.Order, bool, error) {
	record, aggregate, adapter, validation := send.record, send.aggregate, send.adapter, send.validation
	var expected uint64
	var err error
	if authorizationErr := s.authorizeTargetSubmit(ctx, record); authorizationErr != nil {
		pending, abortErr := s.abortUnsentSubmit(ctx, record, aggregate)
		if abortErr != nil {
			return orderdomain.Order{}, false, errors.Join(authorizationErr, abortErr)
		}
		return pending, false, authorizationErr
	}

	exchangeSymbol := validation.Instrument.ExchangeSymbol
	if exchangeSymbol == "" {
		exchangeSymbol = aggregate.Spec.InstrumentID
	}
	if err := ctx.Err(); err != nil {
		pending, abortErr := s.abortUnsentSubmit(ctx, record, aggregate)
		if abortErr != nil {
			return orderdomain.Order{}, false, errors.Join(err, abortErr)
		}
		return pending, false, err
	}
	response, callErr := adapter.PlaceOrder(ctx, exchange.OrderRequest{
		ClientOrderID:  aggregate.Spec.ClientOrderID,
		ExchangeSymbol: exchangeSymbol, OrderType: aggregate.Spec.Type,
		FillPolicy: aggregate.Spec.FillPolicy, Side: aggregate.Spec.Side,
		PositionSide: aggregate.Spec.PositionSide, Quantity: aggregate.Spec.Quantity,
		LimitPrice: aggregate.Spec.LimitPrice, ReferencePrice: aggregate.Spec.ReferencePrice,
		ReduceOnly: aggregate.Spec.ReducePositionOnly,
	})

	// Once the adapter was called, record its outcome even if it used the
	// caller's entire budget. This context cannot authorize another POST.
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer persistCancel()
	if lockAccount != nil {
		unlock, lockErr := lockAccount(persistCtx)
		if lockErr != nil {
			return orderdomain.Order{}, false, &AccountExecutionError{TradingAccountID: record.TradingAccountID, Operation: "submit_record_lock", Err: lockErr}
		}
		defer unlock()
	}
	latest, getErr := s.Store.GetOrder(persistCtx, record.SpaceID, record.OrderID)
	if getErr != nil {
		return orderdomain.Order{}, false, getErr
	}
	current, getErr := domainOrder(latest)
	if getErr != nil {
		return orderdomain.Order{}, false, getErr
	}
	if callErr == nil && current.ExchangeOrderID != "" && response.ExchangeOrderID != "" && current.ExchangeOrderID != response.ExchangeOrderID {
		return orderdomain.Order{}, false, accountExecutionError(record.TradingAccountID, "place",
			fmt.Errorf("%w: conflicting Exchange order ID", orderdomain.ErrInvalidTransition))
	}
	// Private execution facts can win the race with a late HTTP error.
	if current.State.Terminal() || (callErr != nil && (current.State == orderdomain.Open || current.State == orderdomain.PartiallyFilled ||
		current.State == orderdomain.Canceling || current.State == orderdomain.CancelUnknown)) {
		return current, false, nil
	}
	if callErr != nil && exchange.NotAccepted(callErr) && current.State == orderdomain.Submitting {
		// 限流、封禁或本地拦截：交易所确定没有受理，回到待提交，不算拒单，也不用查询。
		pending, abortErr := s.abortUnsentSubmit(persistCtx, latest, current)
		if abortErr != nil {
			return orderdomain.Order{}, false, errors.Join(callErr, abortErr)
		}
		observeOrder(record.TradingAccountID, telemetry.OrderNotAccepted)
		return pending, false, accountExecutionError(record.TradingAccountID, "place", callErr)
	}
	expected = current.Version
	switch {
	case callErr == nil:
		_, err = current.Acknowledge(response.ExchangeOrderID)
	case uncertainExchangeError(callErr):
		_, err = current.MarkSubmitUnknown()
	default:
		_, err = current.Reject()
		latest.RejectReason = exchange.RejectMessage(callErr)
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
	if err = s.Store.Transaction(persistCtx, func(tx *store.Tx) error {
		if err := tx.UpdateOrder(latest, expected); err != nil {
			return err
		}
		// 下单回执（币安 RESULT）里的累计成交量写入观察成交量：整条成交事件丢失时也能发现缺口。
		// 实盘有成交就先关闭准入，成交明细补齐、余额覆盖后由同步恢复。
		if callErr == nil && response.FilledQuantity.Cmp(shared.Zero()) > 0 {
			if err := tx.RaiseObservedFilledQuantity(latest.SpaceID, latest.OrderID, response.FilledQuantity); err != nil {
				return err
			}
			if validation.Account.ExecutionMode == exchange.ExecutionModeLive {
				// 水位只用交易所时间：本地时间比成交晚一个往返，现货余额的更新时间永远追不上。
				// 回执没有时间时只关闭准入，成交缺口会挡住恢复，成交入账时再按成交时间抬水位。
				if at := response.UpdatedAt.UnixMilli(); at > 0 {
					if err := tx.RaiseBalancePending(latest.SpaceID, latest.TradingAccountID, at); err != nil {
						return err
					}
				}
				if lockAccount != nil {
					send.closeReadiness = true
					return nil
				}
				return tx.UpdateTradingAccountReadiness(latest.SpaceID, latest.TradingAccountID, false, s.now().UnixMilli(), fillAwaitingBalance)
			}
		}
		return nil
	}); err != nil {
		return orderdomain.Order{}, false, err
	}
	switch {
	case callErr == nil:
		observeOrder(record.TradingAccountID, telemetry.OrderSubmitted)
		telemetry.OrderNotional.WithLabelValues(record.TradingAccountID, validation.Instrument.QuoteAsset).
			Add(aggregate.Spec.Quantity.Mul(aggregate.Spec.ReferencePrice).Float64())
	case current.State == orderdomain.SubmitUnknown:
		observeOrder(record.TradingAccountID, telemetry.OrderUnknown)
		telemetry.NoteUnknownResult(record.TradingAccountID, s.now())
	case current.State == orderdomain.Rejected:
		observeOrder(record.TradingAccountID, telemetry.OrderRejected)
	}
	return current, callErr == nil &&
		response.Status != "" &&
		response.Status != exchange.OrderStatusOpen, accountExecutionError(record.TradingAccountID, "place", callErr)
}

func (s *Service) abortUnsentSubmit(
	ctx context.Context,
	record store.OrderRecord,
	current orderdomain.Order,
) (orderdomain.Order, error) {
	// This local transition must survive cancellation after SUBMITTING commits.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	expected := current.Version
	if _, err := current.AbortSubmit(); err != nil {
		return orderdomain.Order{}, err
	}
	applyAggregate(&record, current)
	record.SubmittedAt = 0
	if err := s.Store.Transaction(cleanupCtx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	}); err != nil {
		return orderdomain.Order{}, err
	}
	return current, nil
}

// fillAwaitingBalance 是成交后等余额同步确认期间的未就绪原因。
const fillAwaitingBalance = "成交待余额同步确认"

func observeOrder(tradingAccountID, result string) {
	telemetry.Orders.WithLabelValues(tradingAccountID, result).Inc()
}

// uncertainExchangeError 报告请求结果是否未知：超时、连接中断、408、503 未知。
// 限流和本地拦截是确定未受理，不在这里。
func uncertainExchangeError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		exchange.IsKind(err, exchange.ErrorTransportUnknown)
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
	record.RejectReason = exchange.RejectMessage(cause)
	record.RemainingReservedQuantity = "0"
	record.FinishedAt = s.now().UnixMilli()
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	}); err != nil {
		return orderdomain.Order{}, err
	}
	return current, cause
}

// IsPermanentValidationError 报告错误是否为重试也不会通过的下单校验失败。
func IsPermanentValidationError(err error) bool {
	return permanentValidationError(err)
}

func permanentValidationError(err error) bool {
	if errors.Is(err, orderdomain.ErrReferencePriceStale) {
		return false
	}
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
		OrderType:        string(value.Spec.Type), TimeInForce: string(value.Spec.FillPolicy),
		Side: string(value.Spec.Side), PositionSide: string(value.Spec.PositionSide),
		Quantity: value.Spec.Quantity.String(), LimitPrice: limitPrice,
		ReferencePrice:   value.Spec.ReferencePrice.String(),
		ReferencePriceAt: value.Spec.ReferencePriceAt.UnixMilli(),
		ReduceOnly:       value.Spec.ReducePositionOnly,
		OwnerType:        string(value.Spec.Owner.Type), OwnerID: value.Spec.Owner.OwnerID,
		LogicalAccountID: value.Spec.Owner.LogicalAccountID,
		RunnerID:         ownerRunnerID(value.Spec.Owner),
		PositionBefore:   value.Spec.PositionBefore,
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
			PositionBefore: record.PositionBefore,
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
