package accountsync

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

var ErrServiceConfig = errors.New("trade account sync: service is not configured")

type AdapterSource interface {
	Adapter(tradingAccountID string) (execution.ExecutionAdapter, error)
}

type SessionState interface {
	Ready(tradingAccountID string) bool
}

type Metrics interface {
	Observe(tradingAccountID string, now time.Time, maxDifference float64, err error)
}

type FactsObserver interface {
	AccountFactsChanged(
		ctx context.Context,
		tradingAccountID string,
		external bool,
	) error
}

type Result struct {
	FillsIngested          int
	OrdersUpdated          int
	PositionsUpdated       int
	AccountSnapshotUpdated bool
	UnknownOrdersResolved  int
	ExternalFactsImported  bool
	Ready                  bool
	Warnings               []string
}

type Snapshot struct {
	Fills       []exchange.Fill
	Orders      []exchange.Order
	Positions   []exchange.Position
	Account     exchange.AccountSnapshot
	FillCursors store.FillCursors
	Ready       bool
}

type Service struct {
	Store          *store.Store
	Adapters       AdapterSource
	SessionState   SessionState
	Fills          *consumer.Reducer
	Orders         *orderapp.Service
	Metrics        Metrics
	Facts          FactsObserver
	Now            func() time.Time
	LateFillWindow time.Duration
}

// ConfirmCancel exposes the shared reducer's terminal cancellation transition
// to deterministic local adapters. Paper cancellation is authoritative once
// the local adapter accepts it; the following sync only refreshes the account
// snapshot after the reservation has been released.
func (s *Service) ConfirmCancel(ctx context.Context, spaceID, orderID string) error {
	if s == nil || s.Fills == nil {
		return errors.New("trade account sync: fill reducer is not configured")
	}
	return s.Fills.ConfirmCancel(ctx, spaceID, orderID)
}

func (s *Service) SyncAccount(
	ctx context.Context,
	tradingAccountID string,
) (result Result, err error) {
	if err := s.validate(); err != nil {
		return Result{}, err
	}
	maxDifference := 0.0
	defer func() {
		if s.Metrics != nil {
			s.Metrics.Observe(tradingAccountID, s.now(), maxDifference, err)
		}
	}()
	unlockMembership := s.Store.LockLogicalAccountMembership()
	unlockExecution, err := s.lockLogicalAccountExecution(ctx, tradingAccountID)
	if err != nil {
		unlockMembership()
		return Result{}, err
	}
	unlock := s.Store.LockTradingAccount(tradingAccountID)
	result, maxDifference, err = s.syncAccountLocked(ctx, tradingAccountID)
	unlock()
	if err == nil && result.ExternalFactsImported {
		err = s.pauseForExternalFact(ctx, tradingAccountID)
	}
	unlockExecution()
	unlockMembership()
	if err != nil {
		return result, err
	}
	result, err = s.resolveUnknownOrders(ctx, tradingAccountID, result)
	if err != nil {
		return result, err
	}
	return result, s.notifyFacts(
		ctx, tradingAccountID, false,
	)
}

func (s *Service) syncAccountLocked(
	ctx context.Context,
	tradingAccountID string,
) (Result, float64, error) {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return Result{}, 0, err
	}
	adapter, err := s.Adapters.Adapter(tradingAccountID)
	if err != nil {
		return Result{}, 0, err
	}

	openOrders, err := adapter.ListOpenOrders(ctx)
	if err != nil {
		result, failErr := s.fail(ctx, account, err)
		return result, 0, failErr
	}
	localOrders, err := s.Store.ListOrdersForAccount(
		ctx,
		account.SpaceID,
		account.TradingAccountID,
		s.now().Add(-s.lateFillWindow()).UnixMilli(),
	)
	if err != nil {
		result, failErr := s.fail(ctx, account, err)
		return result, 0, failErr
	}
	positions, err := adapter.ListPositionSnapshots(ctx)
	if err != nil {
		result, failErr := s.fail(ctx, account, err)
		return result, 0, failErr
	}
	accountSnapshot, err := adapter.GetAccountSnapshot(ctx)
	if err != nil {
		result, failErr := s.fail(ctx, account, err)
		return result, 0, failErr
	}
	maxDifference := maxBalanceDifference(
		account.Snapshot.Balances,
		snapshotRecord(accountSnapshot).Balances,
	)
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		result, failErr := s.fail(ctx, account, err)
		return result, maxDifference, failErr
	}
	symbols := syncSymbols(
		account,
		openOrders,
		localOrders,
		positions,
		instruments,
		accountSnapshot,
	)
	fills := make([]exchange.Fill, 0)
	cursors := cloneCursors(account.FillCursors)
	for _, symbol := range symbols {
		rows, cursor, listErr := adapter.ListRecentFills(ctx, shared.ExchangeSymbol(symbol), cursors[symbol])
		if listErr != nil {
			result, failErr := s.fail(ctx, account, listErr)
			return result, maxDifference, failErr
		}
		fills = append(fills, rows...)
		if strings.TrimSpace(cursor) != "" {
			cursors[symbol] = cursor
		}
	}

	orders := append([]exchange.Order(nil), openOrders...)
	openByClient := make(map[string]struct{}, len(openOrders))
	for _, current := range openOrders {
		openByClient[current.ClientOrderID] = struct{}{}
	}
	for _, local := range localOrders {
		if orderdomain.State(local.State).Terminal() {
			continue
		}
		if _, found := openByClient[local.ClientOrderID]; found {
			continue
		}
		current, lookupErr := adapter.GetOrder(ctx, shared.ExchangeSymbol(local.Symbol), local.ClientOrderID)
		switch {
		case lookupErr == nil:
			orders = append(orders, current)
		case exchange.IsKind(lookupErr, exchange.ErrorOrderNotFound):
			continue
		default:
			result, failErr := s.fail(ctx, account, lookupErr)
			return result, maxDifference, failErr
		}
	}
	ready := account.Ready
	if s.SessionState != nil {
		ready = s.SessionState.Ready(account.TradingAccountID)
	}
	result, err := s.applySnapshot(ctx, account.TradingAccountID, Snapshot{
		Fills: fills, Orders: orders, Positions: positions,
		Account: accountSnapshot, FillCursors: cursors, Ready: ready,
	})
	return result, maxDifference, err
}

func maxBalanceDifference(before, after []store.AssetBalance) float64 {
	previous := make(map[string]float64, len(before))
	for _, balance := range before {
		previous[balance.Asset] = parseBalance(balance.Total)
	}
	var maximum float64
	for _, balance := range after {
		current := parseBalance(balance.Total)
		old := previous[balance.Asset]
		denominator := math.Max(math.Abs(current), 1e-12)
		maximum = max(maximum, math.Abs(current-old)/denominator)
		delete(previous, balance.Asset)
	}
	for _, old := range previous {
		if old != 0 {
			maximum = max(maximum, 1)
		}
	}
	return maximum
}

func parseBalance(value string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0
	}
	return parsed
}

func (s *Service) ApplySnapshot(
	ctx context.Context,
	tradingAccountID string,
	snapshot Snapshot,
) (Result, error) {
	if err := s.validate(); err != nil {
		return Result{}, err
	}
	unlockMembership := s.Store.LockLogicalAccountMembership()
	unlockExecution, err := s.lockLogicalAccountExecution(ctx, tradingAccountID)
	if err != nil {
		unlockMembership()
		return Result{}, err
	}
	unlock := s.Store.LockTradingAccount(tradingAccountID)
	result, err := s.applySnapshot(ctx, tradingAccountID, snapshot)
	unlock()
	if err == nil && result.ExternalFactsImported {
		err = s.pauseForExternalFact(ctx, tradingAccountID)
	}
	unlockExecution()
	unlockMembership()
	if err != nil {
		return result, err
	}
	result, err = s.resolveUnknownOrders(ctx, tradingAccountID, result)
	if err != nil {
		return result, err
	}
	return result, s.notifyFacts(
		ctx, tradingAccountID, false,
	)
}

func (s *Service) applySnapshot(
	ctx context.Context,
	tradingAccountID string,
	snapshot Snapshot,
) (Result, error) {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return Result{}, err
	}
	if snapshot.Account.ExchangeUpdatedAt.UnixMilli() <= 0 {
		return s.fail(ctx, account, errors.New(
			"trade account sync: account snapshot has no Exchange timestamp",
		))
	}
	result := Result{Ready: snapshot.Ready}

	batchQuantities := make(map[string]shared.Decimal)
	for _, fill := range snapshot.Fills {
		key := fillBatchKey(fill)
		if key == "" {
			continue
		}
		batchQuantities[key] = batchQuantities[key].Add(fill.Quantity)
	}
	for _, fill := range snapshot.Fills {
		applied, external, applyErr := s.applyFill(
			ctx,
			account,
			fill,
			consumer.OriginRESTSnapshot,
			batchQuantities[fillBatchKey(fill)],
		)
		if applyErr != nil {
			return s.fail(ctx, account, applyErr)
		}
		if applied {
			result.FillsIngested++
		}
		result.ExternalFactsImported = result.ExternalFactsImported || external
	}
	for _, current := range snapshot.Orders {
		updated, external, warning, applyErr := s.applyOrder(ctx, account, current)
		if applyErr != nil {
			return s.fail(ctx, account, applyErr)
		}
		if updated {
			result.OrdersUpdated++
		}
		if warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
		result.ExternalFactsImported = result.ExternalFactsImported || external
	}

	positionRecords := make([]store.PositionRecord, 0, len(snapshot.Positions))
	for _, position := range snapshot.Positions {
		if position.ExchangeUpdatedAt.UnixMilli() <= 0 {
			return s.fail(ctx, account, errors.New(
				"trade account sync: position snapshot has no Exchange timestamp",
			))
		}
		positionRecords = append(positionRecords, positionRecord(account, position))
	}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.ReplacePositionsForAccount(
			account.SpaceID,
			account.TradingAccountID,
			positionRecords,
			snapshot.Account.ExchangeUpdatedAt.UnixMilli(),
		)
	}); err != nil {
		return s.fail(ctx, account, err)
	}
	result.PositionsUpdated = len(positionRecords)

	if snapshot.Account.ExchangeUpdatedAt.UnixMilli() >= account.SnapshotSourceTime {
		account.Snapshot = snapshotRecord(snapshot.Account)
		account.SnapshotSourceTime = snapshot.Account.ExchangeUpdatedAt.UnixMilli()
		result.AccountSnapshotUpdated = true
	} else {
		result.Warnings = append(
			result.Warnings,
			"ignored stale REST account snapshot",
		)
	}

	now := s.now().UnixMilli()
	if snapshot.FillCursors == nil {
		snapshot.FillCursors = account.FillCursors
	}
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateTradingAccountFacts(
			account.SpaceID,
			account.TradingAccountID,
			snapshot.FillCursors,
			account.Snapshot,
			account.SnapshotSourceTime,
			now,
		); err != nil {
			return err
		}
		return tx.UpdateTradingAccountReadiness(
			account.SpaceID,
			account.TradingAccountID,
			snapshot.Ready,
			now,
			"",
		)
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *Service) resolveUnknownOrders(
	ctx context.Context,
	tradingAccountID string,
	result Result,
) (Result, error) {
	if s.Orders == nil {
		return result, nil
	}
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return result, err
	}
	unknown, err := s.Store.ListOrdersForAccount(
		ctx,
		account.SpaceID,
		account.TradingAccountID,
		s.now().Add(-s.lateFillWindow()).UnixMilli(),
	)
	if err != nil {
		return result, err
	}
	for _, current := range unknown {
		state := orderdomain.State(current.State)
		if state != orderdomain.Submitting && state != orderdomain.SubmitUnknown {
			continue
		}
		resolved, resolveErr := s.Orders.ResolveUnknown(
			ctx,
			account.SpaceID,
			current.OrderID,
		)
		if resolveErr != nil {
			result.Warnings = append(result.Warnings, resolveErr.Error())
			continue
		}
		if resolved.State != state {
			result.UnknownOrdersResolved++
		}
	}
	return result, nil
}

func (s *Service) ApplyFill(
	ctx context.Context,
	tradingAccountID string,
	fill exchange.Fill,
) (bool, error) {
	if err := s.validate(); err != nil {
		return false, err
	}
	unlockMembership := s.Store.LockLogicalAccountMembership()
	defer unlockMembership()
	unlockExecution, err := s.lockLogicalAccountExecution(ctx, tradingAccountID)
	if err != nil {
		return false, err
	}
	defer unlockExecution()
	unlock := s.Store.LockTradingAccount(tradingAccountID)
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		unlock()
		return false, err
	}
	applied, external, err := s.applyFill(
		ctx,
		account,
		fill,
		consumer.OriginPrivateSocket,
		shared.Zero(),
	)
	unlock()
	if err == nil && external {
		err = s.pauseForExternalFact(ctx, tradingAccountID)
	}
	if err != nil {
		return applied, err
	}
	return applied, s.notifyFacts(ctx, tradingAccountID, false)
}

func (s *Service) ApplyOrder(
	ctx context.Context,
	tradingAccountID string,
	current exchange.Order,
) error {
	if err := s.validate(); err != nil {
		return err
	}
	unlockMembership := s.Store.LockLogicalAccountMembership()
	defer unlockMembership()
	unlockExecution, err := s.lockLogicalAccountExecution(ctx, tradingAccountID)
	if err != nil {
		return err
	}
	defer unlockExecution()
	unlock := s.Store.LockTradingAccount(tradingAccountID)
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		unlock()
		return err
	}
	_, external, _, err := s.applyOrder(ctx, account, current)
	unlock()
	if err == nil && external {
		err = s.pauseForExternalFact(ctx, tradingAccountID)
	}
	if err != nil {
		return err
	}
	return s.notifyFacts(ctx, tradingAccountID, false)
}

func (s *Service) ApplyPosition(
	ctx context.Context,
	tradingAccountID string,
	position exchange.Position,
) error {
	if err := s.validate(); err != nil {
		return err
	}
	unlock := s.Store.LockTradingAccount(tradingAccountID)
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		unlock()
		return err
	}
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		record := positionRecord(account, position)
		if position.RequiresSync || position.Present != (exchange.PositionPresence{}) {
			current, found, getErr := tx.GetPosition(
				account.SpaceID,
				account.TradingAccountID,
				position.Symbol,
				string(position.PositionSide),
			)
			if getErr != nil {
				return getErr
			}
			if !found && position.RequiresSync && !position.Present.Leverage &&
				account.LeverageSettings[position.Symbol] == "" {
				// A private partial update cannot create a valid SWAP
				// projection without leverage. Keep the stream alive and let
				// the queued full synchronization create the authoritative row.
				return tx.UpdateTradingAccountReadiness(
					account.SpaceID,
					account.TradingAccountID,
					false,
					s.now().UnixMilli(),
					"private position update awaiting full sync",
				)
			}
			record = mergePositionRecord(account, current, found, record, position.Present)
		}
		if err := tx.UpsertPosition(record); err != nil {
			return err
		}
		return tx.UpdateTradingAccountReadiness(
			account.SpaceID,
			account.TradingAccountID,
			false,
			s.now().UnixMilli(),
			"private position update awaiting full sync",
		)
	})
	unlock()
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) ApplyAccountSnapshot(
	ctx context.Context,
	tradingAccountID string,
	snapshot exchange.AccountSnapshot,
) error {
	if err := s.validate(); err != nil {
		return err
	}
	unlock := s.Store.LockTradingAccount(tradingAccountID)
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		unlock()
		return err
	}
	if snapshot.ExchangeUpdatedAt.UnixMilli() <= 0 {
		unlock()
		return errors.New("trade account sync: account snapshot has no Exchange timestamp")
	}
	if snapshot.ExchangeUpdatedAt.UnixMilli() < account.Snapshot.ExchangeUpdatedAt {
		unlock()
		return nil
	}
	merged := mergePrivateSnapshot(account.Snapshot, snapshot)
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateTradingAccountSnapshot(
			account.SpaceID,
			account.TradingAccountID,
			merged,
		); err != nil {
			return err
		}
		return tx.UpdateTradingAccountReadiness(
			account.SpaceID,
			account.TradingAccountID,
			false,
			s.now().UnixMilli(),
			"private account update awaiting full sync",
		)
	})
	unlock()
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) SetReady(
	ctx context.Context,
	tradingAccountID string,
	ready bool,
	cause error,
) error {
	if s == nil || s.Store == nil {
		return ErrServiceConfig
	}
	unlock := s.Store.LockTradingAccount(tradingAccountID)
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		unlock()
		return err
	}
	err = s.setReady(ctx, account, ready, cause)
	unlock()
	if err != nil {
		return err
	}
	return s.notifyFacts(ctx, tradingAccountID, false)
}

func (s *Service) setReady(
	ctx context.Context,
	account store.TradingAccountRecord,
	ready bool,
	cause error,
) error {
	now := s.now().UnixMilli()
	lastError := ""
	if cause != nil && !errors.Is(cause, context.Canceled) {
		lastError = cause.Error()
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountReadiness(
			account.SpaceID,
			account.TradingAccountID,
			ready,
			now,
			lastError,
		)
	})
}

func (s *Service) applyFill(
	ctx context.Context,
	account store.TradingAccountRecord,
	fill exchange.Fill,
	kind consumer.FillOrigin,
	syntheticQuantity shared.Decimal,
) (bool, bool, error) {
	external, err := s.ensureOrderForFill(
		ctx, account, fill, syntheticQuantity,
	)
	if err != nil {
		return false, false, err
	}
	applied, err := s.Fills.ApplyFill(ctx, fill, consumer.Source{
		SpaceID: account.SpaceID, TradingAccountID: account.TradingAccountID,
		Kind: kind,
	})
	return applied, external && applied, err
}

func (s *Service) ensureOrderForFill(
	ctx context.Context,
	account store.TradingAccountRecord,
	fill exchange.Fill,
	syntheticQuantity shared.Decimal,
) (bool, error) {
	if fill.ExchangeOrderID != "" {
		if record, err := s.Store.GetOrderByExchangeID(
			ctx,
			account.SpaceID,
			account.TradingAccountID,
			fill.Symbol,
			fill.ExchangeOrderID,
		); err == nil {
			return record.OwnerType == string(orderdomain.OwnerExternal), nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return false, err
		}
	}
	if fill.ClientOrderID != "" {
		if record, err := s.Store.GetOrderByClientID(
			ctx,
			account.SpaceID,
			account.TradingAccountID,
			fill.ClientOrderID,
		); err == nil {
			return record.OwnerType == string(orderdomain.OwnerExternal), nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return false, err
		}
	}
	adapter, err := s.Adapters.Adapter(account.TradingAccountID)
	if err != nil {
		return false, err
	}
	var current exchange.Order
	var lookupErr error
	if fill.ClientOrderID != "" {
		current, lookupErr = adapter.GetOrder(ctx, shared.ExchangeSymbol(fill.Symbol), fill.ClientOrderID)
	} else if lookup, ok := adapter.(execution.ExchangeOrderLookup); ok {
		current, lookupErr = lookup.GetOrderByExchangeID(
			ctx,
			fill.Symbol,
			fill.ExchangeOrderID,
		)
	} else {
		lookupErr = &exchange.Error{
			Kind: exchange.ErrorOrderNotFound,
			Err:  errors.New("Exchange order lookup by ID is unavailable"),
		}
	}
	if lookupErr != nil {
		if !exchange.IsKind(lookupErr, exchange.ErrorOrderNotFound) {
			return false, lookupErr
		}
		if syntheticQuantity.Cmp(shared.Zero()) <= 0 {
			syntheticQuantity = fill.Quantity
		}
		current = exchange.Order{
			ExchangeOrderID: fill.ExchangeOrderID, ClientOrderID: fill.ClientOrderID,
			Symbol: fill.Symbol, OrderType: exchange.OrderTypeMarket,
			Side: fill.Side, PositionSide: fill.PositionSide,
			Quantity: syntheticQuantity, Status: exchange.OrderStatusOpen,
			CreatedAt: fill.TradedAt, UpdatedAt: fill.TradedAt,
		}
	}
	// The fill stream is authoritative for symbol identity. Some exchange
	// lookup adapters return a cached order without the requested symbol (or
	// with a stale one); never let that collapse fills from two instruments.
	current.Symbol = fill.Symbol
	_, err = s.importExternalOrder(ctx, account, current)
	return err == nil, err
}

func fillBatchKey(fill exchange.Fill) string {
	if fill.ExchangeOrderID != "" {
		return fill.Symbol + "\x00exchange\x00" + fill.ExchangeOrderID
	}
	if fill.ClientOrderID != "" {
		return fill.Symbol + "\x00client\x00" + fill.ClientOrderID
	}
	return ""
}

func (s *Service) applyOrder(
	ctx context.Context,
	account store.TradingAccountRecord,
	current exchange.Order,
) (bool, bool, string, error) {
	var record store.OrderRecord
	var err error
	switch {
	case current.ClientOrderID != "":
		record, err = s.Store.GetOrderByClientID(
			ctx,
			account.SpaceID,
			account.TradingAccountID,
			current.ClientOrderID,
		)
	case current.ExchangeOrderID != "":
		record, err = s.Store.GetOrderByExchangeID(
			ctx,
			account.SpaceID,
			account.TradingAccountID,
			current.Symbol,
			current.ExchangeOrderID,
		)
	default:
		return false, false, "", errors.New(
			"trade account sync: Exchange order has no stable identifier",
		)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		record, err = s.importExternalOrder(ctx, account, current)
		if err != nil {
			return false, false, "", err
		}
		return true, true, "", s.applyOrderState(ctx, record, current)
	}
	if err != nil {
		return false, false, "", err
	}
	storedFilled := shared.MustDecimal(record.FilledQuantity)
	externalChanged := record.OwnerType == string(orderdomain.OwnerExternal) &&
		(current.UpdatedAt.UnixMilli() > record.ExchangeUpdatedAt ||
			current.FilledQuantity.Cmp(storedFilled) > 0)
	if current.FilledQuantity.Cmp(storedFilled) < 0 {
		return false, false, fmt.Sprintf(
			"order %s ignored regressing cumulative filled quantity",
			record.OrderID,
		), nil
	}
	if current.FilledQuantity.Cmp(storedFilled) > 0 {
		return false, externalChanged, fmt.Sprintf(
			"order %s snapshot is ahead of ingested Fills",
			record.OrderID,
		), s.applyOrderState(ctx, record, current)
	}
	return true, externalChanged, "", s.applyOrderState(ctx, record, current)
}

func (s *Service) applyOrderState(
	ctx context.Context,
	record store.OrderRecord,
	current exchange.Order,
) error {
	exchangeUpdatedAt := current.UpdatedAt.UnixMilli()
	if exchangeUpdatedAt <= 0 {
		exchangeUpdatedAt = current.CreatedAt.UnixMilli()
	}
	if exchangeUpdatedAt > 0 && record.ExchangeUpdatedAt > exchangeUpdatedAt {
		return nil
	}
	if current.ExchangeOrderID != "" && record.ExchangeOrderID != "" &&
		record.ExchangeOrderID != current.ExchangeOrderID {
		return fmt.Errorf("trade account sync: conflicting Exchange order ID")
	}
	state := orderdomain.State(record.State)
	if state.Terminal() {
		return nil
	}
	switch current.Status {
	case exchange.OrderStatusCanceled, exchange.OrderStatusPartiallyCanceled:
		return s.Fills.ConfirmCancel(ctx, record.SpaceID, record.OrderID)
	case exchange.OrderStatusRejected, exchange.OrderStatusExpired:
		record.ExchangeUpdatedAt = exchangeUpdatedAt
		return s.terminalize(ctx, record, current.Status)
	}
	next := state
	switch current.Status {
	case exchange.OrderStatusOpen:
		if state == orderdomain.Pending || state == orderdomain.Submitting ||
			state == orderdomain.SubmitUnknown || state == orderdomain.CancelUnknown {
			next = orderdomain.Open
		}
	case exchange.OrderStatusPartiallyFilled:
		if !shared.MustDecimal(record.FilledQuantity).IsZero() &&
			state != orderdomain.Filled {
			next = orderdomain.PartiallyFilled
		}
	case exchange.OrderStatusFilled:
		if shared.MustDecimal(record.FilledQuantity).Cmp(shared.MustDecimal(record.Quantity)) == 0 {
			next = orderdomain.Filled
		}
	}
	exchangeOrderID := record.ExchangeOrderID
	if exchangeOrderID == "" {
		exchangeOrderID = current.ExchangeOrderID
	}
	if next == state && exchangeOrderID == record.ExchangeOrderID &&
		exchangeUpdatedAt <= record.ExchangeUpdatedAt {
		return nil
	}
	expected := record.Version
	record.State = string(next)
	record.ExchangeOrderID = exchangeOrderID
	record.ExchangeUpdatedAt = max(record.ExchangeUpdatedAt, exchangeUpdatedAt)
	record.Version++
	if next.Terminal() && record.FinishedAt == 0 {
		record.FinishedAt = s.now().UnixMilli()
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	})
}

func (s *Service) terminalize(
	ctx context.Context,
	record store.OrderRecord,
	status exchange.OrderStatus,
) error {
	state := orderdomain.State(record.State)
	if state.Terminal() {
		if (status == exchange.OrderStatusRejected && state == orderdomain.Rejected) ||
			(status == exchange.OrderStatusExpired && state == orderdomain.Expired) {
			return nil
		}
		return fmt.Errorf("trade account sync: conflicting terminal order state")
	}
	expected := record.Version
	record.Version++
	record.FinishedAt = s.now().UnixMilli()
	if status == exchange.OrderStatusRejected {
		record.State = string(orderdomain.Rejected)
	} else {
		record.State = string(orderdomain.Expired)
	}
	record.RemainingReservedQuantity = "0"
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	})
}

func (s *Service) importExternalOrder(
	ctx context.Context,
	account store.TradingAccountRecord,
	current exchange.Order,
) (store.OrderRecord, error) {
	if strings.TrimSpace(current.ExchangeOrderID) == "" ||
		strings.TrimSpace(current.Symbol) == "" ||
		!current.Side.Valid() ||
		current.Quantity.Cmp(shared.Zero()) <= 0 {
		return store.OrderRecord{}, fmt.Errorf("trade account sync: incomplete external order")
	}
	clientOrderID := current.ClientOrderID
	if strings.TrimSpace(clientOrderID) == "" {
		clientOrderID = "external-" + current.Symbol + "-" + current.ExchangeOrderID
	}
	environment := account.Environment
	if account.ExecutionMode == "PAPER" || environment == "" {
		environment = "PRODUCTION"
	}
	instrument, err := s.Store.GetInstrumentInEnvironment(
		ctx, account.Exchange, environment, account.MarketType, current.Symbol,
	)
	if err != nil {
		return store.OrderRecord{}, err
	}
	reference := current.AveragePrice
	if current.LimitPrice != nil {
		reference = *current.LimitPrice
	}
	if reference.Cmp(shared.Zero()) <= 0 {
		reference = shared.MustDecimal(instrument.PriceTick)
	}
	orderType := current.OrderType
	if orderType != exchange.OrderTypeLimit && orderType != exchange.OrderTypeMarket {
		orderType = exchange.OrderTypeMarket
	}
	tif := current.TimeInForce
	var limitPrice *string
	if orderType == exchange.OrderTypeLimit {
		if current.LimitPrice == nil {
			return store.OrderRecord{}, fmt.Errorf(
				"trade account sync: external LIMIT order has no price",
			)
		}
		value := current.LimitPrice.String()
		limitPrice = &value
		if !tif.ValidForLimit() {
			tif = exchange.TimeInForceGTC
		}
	} else {
		tif = exchange.TimeInForceUnspecified
	}
	positionSide := current.PositionSide
	if exchange.MarketType(account.MarketType) == exchange.MarketTypeSwap {
		positionSide = exchange.PositionSideNet
	}
	referenceAt := current.CreatedAt
	if referenceAt.IsZero() {
		referenceAt = current.UpdatedAt
	}
	if referenceAt.IsZero() {
		referenceAt = s.now()
	}
	ownerID := current.ExchangeOrderID
	if ownerID == "" {
		ownerID = clientOrderID
	}
	logicalAccountID := ""
	if logicalAccount, _, findErr := s.Store.FindLogicalAccountByTradingAccount(
		ctx, account.SpaceID, account.TradingAccountID,
	); findErr == nil {
		logicalAccountID = logicalAccount.LogicalAccountID
	} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
		return store.OrderRecord{}, findErr
	}
	record := store.OrderRecord{
		SpaceID: account.SpaceID,
		OrderID: "external:" + account.TradingAccountID + ":" +
			current.Symbol + ":" + current.ExchangeOrderID,
		TradingAccountID: account.TradingAccountID,
		ClientOrderID:    clientOrderID, ExchangeOrderID: current.ExchangeOrderID,
		Exchange: account.Exchange, MarketType: account.MarketType,
		Symbol: current.Symbol, OrderType: string(orderType),
		TimeInForce: string(tif), Side: string(current.Side),
		PositionSide: string(positionSide), Quantity: current.Quantity.String(),
		LimitPrice: limitPrice, ReferencePrice: reference.String(),
		ReferencePriceAt: referenceAt.UnixMilli(), ReduceOnly: current.ReduceOnly,
		OwnerType: "EXTERNAL", OwnerID: ownerID,
		LogicalAccountID: logicalAccountID, State: string(orderdomain.Open),
		FilledQuantity: "0", AveragePrice: "0", ReservedQuantity: "0",
		RemainingReservedQuantity: "0",
		ExchangeUpdatedAt:         current.UpdatedAt.UnixMilli(),
		Version:                   1,
	}
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateOrder(record)
	})
	if errors.Is(err, store.ErrConflict) {
		return s.Store.GetOrderByClientID(
			ctx,
			account.SpaceID,
			account.TradingAccountID,
			clientOrderID,
		)
	}
	return record, err
}

func (s *Service) fail(
	ctx context.Context,
	account store.TradingAccountRecord,
	cause error,
) (Result, error) {
	if setErr := s.setReady(ctx, account, false, cause); setErr != nil {
		return Result{}, errors.Join(cause, setErr)
	}
	return Result{}, cause
}

func (s *Service) validate() error {
	if s == nil || s.Store == nil || s.Adapters == nil || s.Fills == nil {
		return ErrServiceConfig
	}
	return nil
}

func (s *Service) notifyFacts(
	ctx context.Context,
	tradingAccountID string,
	external bool,
) error {
	if s.Facts == nil {
		return nil
	}
	return s.Facts.AccountFactsChanged(ctx, tradingAccountID, external)
}

func (s *Service) lockLogicalAccountExecution(
	ctx context.Context,
	tradingAccountID string,
) (func(), error) {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return nil, err
	}
	logicalAccount, _, err := s.Store.FindLogicalAccountByTradingAccount(
		ctx,
		account.SpaceID,
		tradingAccountID,
	)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Store.LockLogicalAccountExecution(
		logicalAccount.SpaceID,
		logicalAccount.LogicalAccountID,
	), nil
}

func (s *Service) pauseForExternalFact(
	ctx context.Context,
	tradingAccountID string,
) error {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return err
	}
	logicalAccount, _, err := s.Store.FindLogicalAccountByTradingAccount(
		ctx,
		account.SpaceID,
		tradingAccountID,
	)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation(
			logicalAccount.SpaceID,
			logicalAccount.LogicalAccountID,
			"PAUSED",
			"EXTERNAL order or fill detected on "+tradingAccountID,
		)
	})
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Service) lateFillWindow() time.Duration {
	if s != nil && s.LateFillWindow > 0 {
		return s.LateFillWindow
	}
	return 5 * time.Minute
}

func syncSymbols(
	account store.TradingAccountRecord,
	openOrders []exchange.Order,
	localOrders []store.OrderRecord,
	positions []exchange.Position,
	instruments []store.InstrumentRecord,
	snapshot exchange.AccountSnapshot,
) []string {
	set := make(map[string]struct{})
	for _, symbol := range account.SyncSymbols {
		set[symbol] = struct{}{}
	}
	for symbol := range account.LeverageSettings {
		set[symbol] = struct{}{}
	}
	for symbol := range account.FillCursors {
		set[symbol] = struct{}{}
	}
	for _, current := range openOrders {
		if current.Symbol != "" {
			set[current.Symbol] = struct{}{}
		}
	}
	for _, current := range localOrders {
		if current.Symbol != "" {
			set[current.Symbol] = struct{}{}
		}
	}
	for _, position := range positions {
		if position.Symbol != "" {
			set[position.Symbol] = struct{}{}
		}
	}
	if exchange.MarketType(account.MarketType) == exchange.MarketTypeSpot {
		heldAssets := make(map[string]struct{})
		for _, balance := range snapshot.Balances {
			if balance.Total.Cmp(shared.Zero()) > 0 ||
				balance.Available.Cmp(shared.Zero()) > 0 ||
				balance.Locked.Cmp(shared.Zero()) > 0 {
				heldAssets[balance.Asset] = struct{}{}
			}
		}
		for _, instrument := range instruments {
			if instrument.Status != "TRADING" && instrument.Status != "live" {
				continue
			}
			if !strings.EqualFold(instrument.QuoteAsset, account.SettlementAsset) {
				continue
			}
			if _, held := heldAssets[instrument.BaseAsset]; held {
				set[instrument.Symbol] = struct{}{}
			}
		}
	}
	symbols := make([]string, 0, len(set))
	for symbol := range set {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return symbols
}

func cloneCursors(current store.FillCursors) store.FillCursors {
	result := make(store.FillCursors, len(current))
	for symbol, cursor := range current {
		result[symbol] = cursor
	}
	return result
}

func snapshotRecord(snapshot exchange.AccountSnapshot) store.TradingAccountSnapshot {
	balances := make([]store.AssetBalance, 0, len(snapshot.Balances))
	for _, balance := range snapshot.Balances {
		balances = append(balances, store.AssetBalance{
			Asset: balance.Asset, Available: balance.Available.String(),
			Locked: balance.Locked.String(), Total: balance.Total.String(),
		})
	}
	return store.TradingAccountSnapshot{
		Balances: balances, Equity: snapshot.Equity.String(),
		AvailableFunds:    snapshot.AvailableFunds.String(),
		UsedMargin:        snapshot.UsedMargin.String(),
		MaintenanceMargin: snapshot.MaintenanceMargin.String(),
		UnrealizedPnL:     snapshot.UnrealizedPnL.String(),
		ExchangeUpdatedAt: snapshot.ExchangeUpdatedAt.UnixMilli(),
	}
}

func mergePrivateSnapshot(
	current store.TradingAccountSnapshot,
	update exchange.AccountSnapshot,
) store.TradingAccountSnapshot {
	incoming := snapshotRecord(update)
	if update.Present.Balances {
		balances := make(
			map[string]store.AssetBalance,
			len(current.Balances)+len(incoming.Balances),
		)
		order := make([]string, 0, len(current.Balances)+len(incoming.Balances))
		for _, balance := range current.Balances {
			if _, found := balances[balance.Asset]; !found {
				order = append(order, balance.Asset)
			}
			balances[balance.Asset] = balance
		}
		for _, balance := range incoming.Balances {
			if _, found := balances[balance.Asset]; !found {
				order = append(order, balance.Asset)
			}
			balances[balance.Asset] = balance
		}
		current.Balances = current.Balances[:0]
		for _, asset := range order {
			current.Balances = append(current.Balances, balances[asset])
		}
	}
	for _, field := range []struct {
		present     bool
		destination *string
		source      string
	}{
		{update.Present.Equity, &current.Equity, incoming.Equity},
		{update.Present.AvailableFunds, &current.AvailableFunds, incoming.AvailableFunds},
		{update.Present.UsedMargin, &current.UsedMargin, incoming.UsedMargin},
		{
			update.Present.MaintenanceMargin,
			&current.MaintenanceMargin,
			incoming.MaintenanceMargin,
		},
		{update.Present.UnrealizedPnL, &current.UnrealizedPnL, incoming.UnrealizedPnL},
	} {
		if field.present {
			*field.destination = field.source
		}
	}
	current.ExchangeUpdatedAt = incoming.ExchangeUpdatedAt
	return current
}

func positionRecord(
	account store.TradingAccountRecord,
	position exchange.Position,
) store.PositionRecord {
	exchangeSymbol := position.ExchangeSymbol
	if exchangeSymbol == "" {
		exchangeSymbol = position.Symbol
	}
	return store.PositionRecord{
		SpaceID: account.SpaceID, TradingAccountID: account.TradingAccountID,
		InstrumentID: position.InstrumentID, ExchangeSymbol: exchangeSymbol,
		Symbol: position.Symbol, PositionSide: string(position.PositionSide),
		SignedQuantity: position.SignedQuantity.String(),
		EntryPrice:     position.EntryPrice.String(), MarkPrice: position.MarkPrice.String(),
		Leverage: position.Leverage.String(), MarginMode: string(position.MarginMode),
		UsedMargin:        position.UsedMargin.String(),
		LiquidationPrice:  position.LiquidationPrice.String(),
		UnrealizedPnL:     position.UnrealizedPnL.String(),
		RealizedPnL:       position.RealizedPnL.String(),
		ExchangeUpdatedAt: position.ExchangeUpdatedAt.UnixMilli(),
	}
}

func mergePositionRecord(
	account store.TradingAccountRecord,
	current store.PositionRecord,
	found bool,
	incoming store.PositionRecord,
	present exchange.PositionPresence,
) store.PositionRecord {
	if !found {
		current = incoming
		current.MarkPrice = "0"
		current.UsedMargin = "0"
		current.LiquidationPrice = "0"
		current.RealizedPnL = "0"
		if leverage := account.LeverageSettings[incoming.Symbol]; leverage != "" {
			current.Leverage = leverage
		}
	}
	current.SpaceID = incoming.SpaceID
	current.TradingAccountID = incoming.TradingAccountID
	current.Symbol = incoming.Symbol
	current.PositionSide = incoming.PositionSide
	for _, field := range []struct {
		present     bool
		destination *string
		source      string
	}{
		{present.SignedQuantity, &current.SignedQuantity, incoming.SignedQuantity},
		{present.EntryPrice, &current.EntryPrice, incoming.EntryPrice},
		{present.MarkPrice, &current.MarkPrice, incoming.MarkPrice},
		{present.Leverage, &current.Leverage, incoming.Leverage},
		{present.MarginMode, &current.MarginMode, incoming.MarginMode},
		{present.UsedMargin, &current.UsedMargin, incoming.UsedMargin},
		{present.LiquidationPrice, &current.LiquidationPrice, incoming.LiquidationPrice},
		{present.UnrealizedPnL, &current.UnrealizedPnL, incoming.UnrealizedPnL},
		{present.RealizedPnL, &current.RealizedPnL, incoming.RealizedPnL},
	} {
		if field.present {
			*field.destination = field.source
		}
	}
	current.ExchangeUpdatedAt = incoming.ExchangeUpdatedAt
	return current
}
