package operator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type rejectedManualPlace struct {
	OrderService
	cause error
}

func (s rejectedManualPlace) Place(context.Context, string, orderdomain.OrderSpec) (orderdomain.Order, error) {
	return orderdomain.Order{}, s.cause
}

func TestManualPermanentFailureIdentitySurvivesReplay(t *testing.T) {
	for _, cause := range []error{orderapp.ErrInsufficientFunds, orderapp.ErrQuantityRule, orderdomain.ErrInvalidSpec} {
		t.Run(cause.Error(), func(t *testing.T) {
			f := newOperatorFixture(t, exchange.MarketTypeSpot)
			s := f.service()
			s.Orders = rejectedManualPlace{OrderService: f.orders, cause: cause}
			command := ManualOrderCommand{SpaceID: "space-1", ActionID: "permanent", TradingAccountID: "account-a", ClientOrderID: "permanent-client", InstrumentID: f.instrumentID(), Type: exchange.OrderTypeMarket, Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"), Reason: "permanent error test"}
			for attempt := 0; attempt < 2; attempt++ {
				result, err := s.PlaceManualOrder(context.Background(), command)
				require.ErrorIs(t, err, cause)
				require.Equal(t, "FAILED", result.Action.Status)
				require.Contains(t, *result.Action.ResultJSON, "error_code")
			}
		})
	}
}

func TestManualFailurePersistenceErrorRemainsSystemError(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	closed, err := store.Open(filepath.Join(t.TempDir(), "closed.db"))
	require.NoError(t, err)
	action := store.OperatorActionRecord{SpaceID: "space-1", ActionID: "closed", LogicalAccountID: "logical-1", ActionType: "MANUAL_ORDER", RequestJSON: `{}`, Reason: "closed db", Status: "RUNNING"}
	require.NoError(t, closed.Close())
	s := f.service()
	s.Store = closed
	_, err = s.failManualAction(context.Background(), action, orderapp.ErrInsufficientFunds, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "database is closed")
	require.NotErrorIs(t, err, orderapp.ErrInsufficientFunds)
}

type unresolvedOrderStub struct {
	OrderService
	calls int
}

func (s *unresolvedOrderStub) ResolveUnknown(context.Context, string, string) (orderdomain.Order, error) {
	s.calls++
	if s.calls > 1 {
		return orderdomain.Order{}, errors.New("unexpected recursive lookup")
	}
	return orderdomain.Order{State: orderdomain.SubmitUnknown}, nil
}

func TestStopOrderYieldsAfterUnresolvedLookup(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	child := activeOrder(f, "unknown", "TARGET")
	child.State = string(orderdomain.SubmitUnknown)
	f.order(t, child)
	s := f.service()
	orders := &unresolvedOrderStub{OrderService: f.orders}
	s.Orders = orders
	err := s.stopOrder(context.Background(), child)
	require.ErrorIs(t, err, ErrCancelUnconfirmed)
	require.Equal(t, 1, orders.calls)
}

func TestManualMissingInstrumentFailsAction(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	command := ManualOrderCommand{SpaceID: "space-1", ActionID: "missing-instrument", TradingAccountID: "account-a", ClientOrderID: "missing-client", InstrumentID: "does-not-exist", Type: exchange.OrderTypeMarket, Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"), Reason: "invalid instrument test"}
	result, err := f.service().PlaceManualOrder(context.Background(), command)
	require.ErrorIs(t, err, ErrInvalidCommand)
	require.Equal(t, "FAILED", result.Action.Status)
	action, err := f.store.GetOperatorAction(context.Background(), "space-1", command.ActionID)
	require.NoError(t, err)
	require.Equal(t, "FAILED", action.Status)
}

func TestManualRunningDiagnosticIsNotReadError(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	raw := `{"deadline_at":123}`
	result, err := f.service().loadManualOrderResult(context.Background(), store.OperatorActionRecord{Status: "RUNNING", ResultJSON: &raw, LastError: "temporary transport failure"})
	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status)
}

func TestManualLegacyDeadlineIsBackfilledOnce(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	ctx := context.Background()
	action, _, err := f.store.CreateOperatorAction(ctx, store.OperatorActionRecord{
		SpaceID: "space-1", ActionID: "legacy", LogicalAccountID: "logical-1",
		ActionType: "MANUAL_ORDER", Reason: "legacy recovery", RequestJSON: `{}`, Status: "RUNNING",
	})
	require.NoError(t, err)
	s := f.service()
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_operator_actions SET c_ctime = ? WHERE c_space_id = ? AND c_action_id = ?", f.now.Add(-time.Hour), "space-1", "legacy").Error)
	action, err = f.store.GetOperatorAction(ctx, "space-1", "legacy")
	require.NoError(t, err)
	progress, err := s.manualProgress(ctx, &action)
	require.NoError(t, err)
	require.Equal(t, action.CreatedAt.Add(time.Minute).UnixMilli(), progress.DeadlineAt)
	require.True(t, s.manualExpired(progress))
	persisted, err := f.store.GetOperatorAction(ctx, "space-1", "legacy")
	require.NoError(t, err)
	s.ManualSubmitWindow = time.Hour
	s.Now = func() time.Time { return f.now.Add(5 * time.Minute) }
	reloaded, err := s.manualProgress(ctx, &persisted)
	require.NoError(t, err)
	require.Equal(t, progress.DeadlineAt, reloaded.DeadlineAt)
}

func TestManualTerminalWithoutResultIsCorrupt(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	for _, status := range []string{"COMPLETED", "FAILED"} {
		_, err := f.service().loadManualOrderResult(context.Background(), store.OperatorActionRecord{Status: status})
		require.ErrorIs(t, err, store.ErrInvalidRecord)
	}
}

func TestManualPersistedResultContractFailsClosed(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	f.order(t, activeOrder(f, "child", "OPERATOR"))
	for _, tc := range []struct{ name, status, raw, lastError string }{
		{name: "nil-completed", status: "COMPLETED"},
		{name: "nil-failed", status: "FAILED", lastError: "failure"},
		{name: "empty-completed", status: "COMPLETED", raw: `{}`},
		{name: "empty-failed", status: "FAILED", raw: `{}`},
		{name: "deadline-completed", status: "COMPLETED", raw: `{"deadline_at":1}`},
		{name: "deadline-failed", status: "FAILED", raw: `{"deadline_at":1}`},
		{name: "failed-child-no-error", status: "FAILED", raw: `{"order_id":"child"}`},
		{name: "completed-with-error", status: "COMPLETED", raw: `{"order_id":"child"}`, lastError: "failure"},
		{name: "unknown-status", status: "UNKNOWN", raw: `{"order_id":"child"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := store.OperatorActionRecord{SpaceID: "space-1", Status: tc.status, LastError: tc.lastError}
			if tc.raw != "" {
				action.ResultJSON = &tc.raw
			}
			_, err := f.service().loadManualOrderResult(context.Background(), action)
			require.ErrorIs(t, err, ErrInvalidActionResult)
			require.ErrorIs(t, err, store.ErrInvalidRecord)
		})
	}
}

func TestManualLegacyExpiredActionCannotSubmit(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	ctx := context.Background()
	command := ManualOrderCommand{SpaceID: "space-1", ActionID: "old-action", TradingAccountID: "account-a", ClientOrderID: "old-client", InstrumentID: f.instrumentID(), Type: exchange.OrderTypeMarket, Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"), Reason: "legacy"}
	request, err := manualOrderRequestJSON(command)
	require.NoError(t, err)
	_, _, err = f.store.CreateOperatorAction(ctx, store.OperatorActionRecord{SpaceID: command.SpaceID, ActionID: command.ActionID, LogicalAccountID: "logical-1", ActionType: "MANUAL_ORDER", Status: "RUNNING", Reason: command.Reason, RequestJSON: request})
	require.NoError(t, err)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_operator_actions SET c_ctime = ? WHERE c_space_id = ? AND c_action_id = ?", f.now.Add(-time.Hour), command.SpaceID, command.ActionID).Error)
	result, err := f.service().PlaceManualOrder(ctx, command)
	require.ErrorContains(t, err, "deadline")
	require.Equal(t, "FAILED", result.Action.Status)
	require.Empty(t, f.orders.specs)
	require.Zero(t, f.orders.submitCalls)
}

func TestManualCreationClockMatchesDurableDeadline(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	command := ManualOrderCommand{SpaceID: "space-1", ActionID: "clock", TradingAccountID: "account-a", ClientOrderID: "clock-client", InstrumentID: f.instrumentID(), Type: exchange.OrderTypeMarket, Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"), Reason: "clock"}
	result, err := f.service().PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)
	require.True(t, result.Action.CreatedAt.Equal(f.now), "creation must use the same clock as deadline")
	progress, err := f.service().manualProgress(context.Background(), &result.Action)
	require.NoError(t, err)
	require.Equal(t, result.Action.CreatedAt.Add(time.Minute).UnixMilli(), progress.DeadlineAt)
}

func TestManualOrderPausesAndCancelsTargetsBeforeSubmit(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "target-child",
		TradingAccountID: "account-a", ClientOrderID: "target-child",
		ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
		PositionSide: "NET", Quantity: "1", ReferencePrice: "100",
		ReferencePriceAt: fixture.now.UnixMilli(),
		OwnerType:        "TARGET", OwnerID: "target-1",
		LogicalAccountID: "logical-1", RunnerID: "runner-1",
		State: "OPEN", Version: 1,
	})

	result, err := fixture.service().PlaceManualOrder(
		context.Background(),
		ManualOrderCommand{
			SpaceID: "space-1", ActionID: "manual-1",
			TradingAccountID: "account-a", ClientOrderID: "manual-client",
			InstrumentID: fixture.instrumentID(), Type: exchange.OrderTypeMarket,
			Side: exchange.SideSell, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), Reason: "operator override",
		},
	)

	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status)
	require.Equal(t, "manual-order-1", result.Order.OrderID)
	require.Equal(t, []string{
		"sync:account-a",
		"cancel:target-child",
		"sync:account-a",
		"sync:account-b",
		"sync:account-b",
		"place:manual-client",
		"submit:manual-order-1",
	}, fixture.trace)
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, orderdomain.OwnerOperator, fixture.orders.specs[0].Owner.Type)
	require.Equal(t, "manual-1", fixture.orders.specs[0].Owner.OwnerID)
	require.Equal(t, "logical-1", fixture.orders.specs[0].Owner.LogicalAccountID)
	require.Nil(t, fixture.orders.specs[0].Owner.RunnerID)
	require.Equal(t, []string{"BTCUSDT"}, *fixture.prices.calls)
	account, err := fixture.store.GetLogicalAccount(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", account.AutomationState)
	require.Equal(t, "operator override", account.PauseReason)
}

func TestManualOrderActionIDIsIdempotent(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	command := ManualOrderCommand{
		SpaceID: "space-1", ActionID: "manual-1",
		TradingAccountID: "account-a", ClientOrderID: "manual-client",
		InstrumentID: fixture.instrumentID(), Type: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.1"),
		Reason: "operator override",
	}

	first, err := fixture.service().PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)
	replayed, err := fixture.service().PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)

	require.Equal(t, first.Action.ActionID, replayed.Action.ActionID)
	require.Equal(t, first.Order.OrderID, replayed.Order.OrderID)
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, 1, fixture.orders.submitCalls)

	command.Quantity = shared.MustDecimal("0.2")
	_, err = fixture.service().PlaceManualOrder(context.Background(), command)
	require.ErrorIs(t, err, store.ErrConflict)
}

func TestManualOrderTerminalReplayDoesNotPauseAfterResume(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	command := ManualOrderCommand{
		SpaceID: "space-1", ActionID: "manual-1",
		TradingAccountID: "account-a", ClientOrderID: "manual-client",
		InstrumentID: fixture.instrumentID(), Type: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.1"),
		Reason: "operator override",
	}
	_, err := fixture.service().PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.DeleteLogicalAccountMember(
			"space-1", "logical-1", "account-a",
		); err != nil {
			return err
		}
		return tx.SetLogicalAccountAutomation(
			"space-1", "logical-1", "ACTIVE", "",
		)
	}))

	_, err = fixture.service().PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)

	account, err := fixture.store.GetLogicalAccount(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", account.AutomationState)
}

func TestManualOrderRunningReplayFailsStablyAfterMemberRemoval(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	command := ManualOrderCommand{
		SpaceID: "space-1", ActionID: "manual-1",
		TradingAccountID: "account-a", ClientOrderID: "manual-client",
		InstrumentID: fixture.instrumentID(), Type: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.1"),
		Reason: "operator override",
	}
	requestJSON, err := manualOrderRequestJSON(command)
	require.NoError(t, err)
	_, _, err = fixture.store.CreateOperatorAction(
		context.Background(),
		store.OperatorActionRecord{
			SpaceID: "space-1", ActionID: "manual-1",
			LogicalAccountID: "logical-1", ActionType: "MANUAL_ORDER",
			Reason: command.Reason, RequestJSON: requestJSON, Status: "RUNNING",
		},
	)
	require.NoError(t, err)
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation(
			"space-1", "logical-1", "PAUSED", "membership change",
		); err != nil {
			return err
		}
		return tx.DeleteLogicalAccountMember(
			"space-1", "logical-1", "account-a",
		)
	}))

	result, err := fixture.service().PlaceManualOrder(
		context.Background(), command,
	)

	require.Error(t, err)
	require.Equal(t, "FAILED", result.Action.Status)
	replayed, replayErr := fixture.service().PlaceManualOrder(
		context.Background(), command,
	)
	require.Error(t, replayErr)
	require.Equal(t, "FAILED", replayed.Action.Status)
}

func TestManualOrderRunningReplayFailsAfterAccountMovesLogicalAccount(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	command := ManualOrderCommand{
		SpaceID: "space-1", ActionID: "manual-1",
		TradingAccountID: "account-a", ClientOrderID: "manual-client",
		InstrumentID: fixture.instrumentID(), Type: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.1"),
		Reason: "operator override",
	}
	requestJSON, err := manualOrderRequestJSON(command)
	require.NoError(t, err)
	_, _, err = fixture.store.CreateOperatorAction(
		context.Background(),
		store.OperatorActionRecord{
			SpaceID: "space-1", ActionID: "manual-1",
			LogicalAccountID: "logical-1", ActionType: "MANUAL_ORDER",
			Reason: command.Reason, RequestJSON: requestJSON, Status: "RUNNING",
		},
	)
	require.NoError(t, err)
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation(
			"space-1", "logical-1", "PAUSED", "membership change",
		); err != nil {
			return err
		}
		if err := tx.DeleteLogicalAccountMember(
			"space-1", "logical-1", "account-a",
		); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-2", Name: "logical-2",
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-2",
			TradingAccountID: "account-a", Enabled: true,
		})
	}))

	result, err := fixture.service().PlaceManualOrder(
		context.Background(), command,
	)

	require.Error(t, err)
	require.Equal(t, "FAILED", result.Action.Status)
	require.Contains(t, result.Accounts[0].Error, "moved")
}

func TestManualOrderFreshSyncsTargetWithoutKnownTargetOrders(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)

	_, err := fixture.service().PlaceManualOrder(
		context.Background(),
		ManualOrderCommand{
			SpaceID: "space-1", ActionID: "manual-1",
			TradingAccountID: "account-a", ClientOrderID: "manual-client",
			InstrumentID: fixture.instrumentID(), Type: exchange.OrderTypeMarket,
			Side: exchange.SideSell, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), Reason: "operator override",
		},
	)

	require.NoError(t, err)
	placeIndex := traceIndex(fixture.trace, "place:manual-client")
	require.GreaterOrEqual(t, placeIndex, 0)
	require.Less(t, traceIndex(fixture.trace, "sync:account-a"), placeIndex)
}

func TestManualLogicalAccountLockRevalidatesMovedEnabledMembership(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	held := fixture.store.LockLogicalAccount("space-1", "logical-1")
	type lockResult struct {
		account store.LogicalAccountRecord
		unlock  func()
		err     error
	}
	resultCh := make(chan lockResult, 1)
	go func() {
		account, unlock, err := fixture.service().lockCurrentLogicalAccount(
			context.Background(), "space-1", "account-a",
		)
		resultCh <- lockResult{account: account, unlock: unlock, err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation(
			"space-1", "logical-1", "PAUSED", "move member",
		); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "account-a", Enabled: false, Priority: 1,
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-2", Name: "logical-2",
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-2",
			TradingAccountID: "account-a", Enabled: true, Priority: 1,
		})
	}))
	held()

	result := <-resultCh
	require.NoError(t, result.err)
	require.Equal(t, "logical-2", result.account.LogicalAccountID)
	result.unlock()
}

func TestManualOrderDoesNotCancelNonTargetOrders(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	fixture.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "external",
		TradingAccountID: "account-a", ClientOrderID: "external",
		ExchangeOrderID: "exchange-1", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "SELL", Quantity: "0.1",
		ReferencePrice: "100", ReferencePriceAt: fixture.now.UnixMilli(),
		OwnerType: "EXTERNAL", OwnerID: "exchange-1",
		LogicalAccountID: "logical-1", State: "OPEN", Version: 1,
	})

	_, err := fixture.service().PlaceManualOrder(
		context.Background(),
		ManualOrderCommand{
			SpaceID: "space-1", ActionID: "manual-1",
			TradingAccountID: "account-a", ClientOrderID: "manual-client",
			InstrumentID: fixture.instrumentID(), Type: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.1"),
			Reason: "operator override",
		},
	)

	require.NoError(t, err)
	require.NotContains(t, fixture.trace, "cancel:external")
}

func TestManualOrderRejectsTargetDiscoveredDuringCancellationSync(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	fixture.order(t, activeOrder(fixture, "target-child", "TARGET"))
	fixture.syncer.onSync = func(ctx context.Context, accountID string, call int) error {
		if accountID != "account-a" || call != 2 {
			return nil
		}
		fixture.order(t, store.OrderRecord{
			SpaceID: "space-1", OrderID: "late-target",
			TradingAccountID: "account-a", ClientOrderID: "late-target",
			ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
			Quantity: "0.1", ReferencePrice: "100",
			ReferencePriceAt: fixture.now.UnixMilli(),
			OwnerType:        "TARGET", OwnerID: "target-1",
			LogicalAccountID: "logical-1", RunnerID: "runner-1",
			State: "OPEN", Version: 1,
		})
		return nil
	}

	_, err := fixture.service().PlaceManualOrder(
		context.Background(),
		ManualOrderCommand{
			SpaceID: "space-1", ActionID: "manual-1",
			TradingAccountID: "account-a", ClientOrderID: "manual-client",
			InstrumentID: fixture.instrumentID(), Type: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.1"),
			Reason: "operator override",
		},
	)

	require.NoError(t, err)
	action, getErr := fixture.store.GetOperatorAction(context.Background(), "space-1", "manual-1")
	require.NoError(t, getErr)
	require.Equal(t, "RUNNING", action.Status)
	require.Contains(t, action.LastError, ErrCancelUnconfirmed.Error())
	require.Empty(t, fixture.orders.specs)
}

func TestManualOrderCancelsEveryMemberAndReturnsPerAccountErrors(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.order(t, activeOrder(fixture, "target-a", "TARGET"))
	orderB := activeOrder(fixture, "target-b", "TARGET")
	orderB.TradingAccountID = "account-b"
	orderB.ExchangeSymbol = "BTC-USDT-SWAP"
	fixture.order(t, orderB)
	fixture.orders.leaveOpen = map[string]bool{"target-a": true}

	result, err := fixture.service().PlaceManualOrder(
		context.Background(),
		ManualOrderCommand{
			SpaceID: "space-1", ActionID: "manual-1",
			TradingAccountID: "account-a", ClientOrderID: "manual-client",
			InstrumentID: fixture.instrumentID(), Type: exchange.OrderTypeMarket,
			Side: exchange.SideSell, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), Reason: "operator override",
		},
	)

	require.NoError(t, err)
	require.Contains(t, fixture.trace, "cancel:target-a")
	require.Contains(t, fixture.trace, "cancel:target-b")
	require.Empty(t, fixture.orders.specs)
	require.NotEmpty(t, result.Accounts)
	require.Equal(t, "account-a", result.Accounts[0].TradingAccountID)
	require.Equal(t, "RUNNING", result.Action.Status)
	require.Contains(t, result.Action.LastError, ErrCancelUnconfirmed.Error())
}

func TestCancelOrderPausesLogicalAccountAndIsIdempotent(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.order(t, activeOrder(fixture, "target-child", "TARGET"))
	command := CancelOrderCommand{
		SpaceID: "space-1", ActionID: "cancel-1",
		OrderID: "target-child", Reason: "operator cancel",
	}

	first, err := fixture.service().CancelOrder(context.Background(), command)
	require.NoError(t, err)
	replayed, err := fixture.service().CancelOrder(context.Background(), command)
	require.NoError(t, err)

	require.Equal(t, "COMPLETED", first.Action.Status)
	require.Equal(t, first.Order.OrderID, replayed.Order.OrderID)
	require.Equal(t, []string{
		"cancel:target-child", "sync:account-a",
	}, fixture.trace)
	account, err := fixture.store.GetLogicalAccount(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", account.AutomationState)
	require.Equal(t, "operator cancel", account.PauseReason)
	orderRecord, err := fixture.store.GetOrder(
		context.Background(), "space-1", "target-child",
	)
	require.NoError(t, err)
	require.Equal(t, "TARGET", orderRecord.OwnerType)
	require.Equal(t, "owner-1", orderRecord.OwnerID)

	command.OrderID = "other"
	_, err = fixture.service().CancelOrder(context.Background(), command)
	require.ErrorIs(t, err, store.ErrConflict)
}

func TestResumeOperatorActionContinuesPersistedCancel(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.order(t, activeOrder(fixture, "target-child", "TARGET"))
	requestJSON, err := cancelOrderRequestJSON(CancelOrderCommand{
		SpaceID: "space-1", ActionID: "cancel-1",
		OrderID: "target-child", Reason: "operator cancel",
	})
	require.NoError(t, err)
	action, _, err := fixture.store.CreateOperatorAction(
		context.Background(),
		store.OperatorActionRecord{
			SpaceID: "space-1", ActionID: "cancel-1",
			LogicalAccountID: "logical-1", ActionType: "CANCEL_ORDER",
			Reason: "operator cancel", RequestJSON: requestJSON, Status: "RUNNING",
		},
	)
	require.NoError(t, err)

	require.NoError(t, fixture.service().ResumeOperatorAction(
		context.Background(), action,
	))

	current, err := fixture.store.GetOperatorAction(
		context.Background(), "space-1", "cancel-1",
	)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", current.Status)
	require.Equal(t, []string{
		"cancel:target-child", "sync:account-a",
	}, fixture.trace)
}

type operatorFixture struct {
	t      *testing.T
	store  *store.Store
	orders *operatorOrderStub
	syncer *operatorSyncStub
	prices operatorPriceStub
	trace  []string
	now    time.Time
	market exchange.MarketType
}

func newOperatorFixture(t *testing.T, market exchange.MarketType) *operatorFixture {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	fixture := &operatorFixture{
		t: t, store: tradeStore, now: time.UnixMilli(2_000).UTC(),
		market: market,
		prices: operatorPriceStub{
			quote: Quote{
				Price: shared.MustDecimal("100"), UpdatedAt: time.UnixMilli(2_000).UTC(),
			},
			calls: new([]string),
		},
	}
	fixture.orders = &operatorOrderStub{
		fixture: fixture, store: tradeStore, nextID: "manual-order-1",
	}
	fixture.syncer = &operatorSyncStub{fixture: fixture}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, accountID := range []string{"account-a", "account-b"} {
			if err := tx.CreateTradingAccount(fixture.account(accountID)); err != nil {
				return err
			}
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerRunnerID: "runner-1", ExecutionMode: "PAPER",
			MarketType: string(market), SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		for index, accountID := range []string{"account-a", "account-b"} {
			if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
				SpaceID: "space-1", LogicalAccountID: "logical-1",
				TradingAccountID: accountID, Enabled: true, Priority: index + 1,
			}); err != nil {
				return err
			}
		}
		for _, accountID := range []string{"account-a", "account-b"} {
			if err := tx.UpsertInstrument(fixture.instrument(accountID)); err != nil {
				return err
			}
		}
		return tx.SetLogicalAccountAutomation(
			"space-1", "logical-1", "ACTIVE", "",
		)
	}))
	return fixture
}

func (f *operatorFixture) account(id string) store.TradingAccountRecord {
	exchangeName := "BINANCE"
	if id == "account-b" {
		exchangeName = "OKX"
	}
	return store.TradingAccountRecord{
		SpaceID: "space-1", TradingAccountID: id, Name: id,
		Exchange: exchangeName, MarketType: string(f.market),
		ExecutionMode: "PAPER", Environment: "PAPER",
		SettlementAsset: "USDT", MarginMode: map[bool]string{
			true: "CROSS", false: "",
		}[f.market == exchange.MarketTypeSwap],
		Status: "ENABLED", Ready: true,
		LeverageSettings: store.LeverageSettings{
			"BTCUSDT": "5", "BTC-USDT-SWAP": "5",
		},
		Snapshot: store.TradingAccountSnapshot{
			AvailableFunds: "100000",
			Balances: []store.AssetBalance{{
				Asset: "USDT", Available: "100000", Total: "100000",
			}},
		},
		LastSyncAt: f.now.UnixMilli(), SnapshotSourceTime: f.now.UnixMilli(),
	}
}

func (f *operatorFixture) instrument(accountID string) store.InstrumentRecord {
	exchangeName := "BINANCE"
	symbol := "BTCUSDT"
	if accountID == "account-b" {
		exchangeName = "OKX"
		symbol = "BTC-USDT-SWAP"
	}
	instrument := store.InstrumentRecord{
		Exchange: exchangeName, MarketType: string(f.market), ExchangeSymbol: symbol,
		InstrumentID: "BTC-USDT-" + string(f.market),
		BaseAsset:    "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
		ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001",
		PriceTick: "0.1", MinNotional: "5", Status: "TRADING",
		ExchangeUpdatedAt: f.now.UnixMilli(),
	}
	if f.market == exchange.MarketTypeSwap {
		instrument.Linear = true
		instrument.ContractValue = "0.001"
		instrument.ContractValueAsset = "BTC"
		instrument.ExchangeQuantityStep = "1"
		instrument.MinExchangeQuantity = "1"
	}
	return instrument
}

func (f *operatorFixture) instrumentID() string {
	return "BTC-USDT-" + string(f.market)
}

func (f *operatorFixture) order(t *testing.T, record store.OrderRecord) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(record)
	}))
}

func (f *operatorFixture) position(t *testing.T, accountID, symbol, quantity string) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: accountID,
			ExchangeSymbol: symbol, PositionSide: "NET", SignedQuantity: quantity,
			EntryPrice: "100", MarkPrice: "100", Leverage: "5",
			MarginMode: "CROSS", ExchangeUpdatedAt: f.now.UnixMilli(),
		})
	}))
}

func (f *operatorFixture) service() *Service {
	return &Service{
		Store: f.store, Orders: f.orders, Syncer: f.syncer, Prices: f.prices,
		Now: func() time.Time { return f.now }, FlattenMaxAttempts: 1,
		FlattenRetryInterval: -1,
	}
}

type operatorOrderStub struct {
	fixture     *operatorFixture
	store       *store.Store
	nextID      string
	specs       []orderdomain.OrderSpec
	submitCalls int
	leaveOpen   map[string]bool
	cancelError map[string]error
}

func (s *operatorOrderStub) Place(
	ctx context.Context,
	spaceID string,
	spec orderdomain.OrderSpec,
) (orderdomain.Order, error) {
	s.fixture.trace = append(s.fixture.trace, "place:"+spec.ClientOrderID)
	s.specs = append(s.specs, spec)
	id := s.nextID
	if id == "" {
		id = "child-" + spec.TradingAccountID + "-" + spec.InstrumentID
	}
	record := store.OrderRecord{
		SpaceID: spaceID, OrderID: id,
		TradingAccountID: spec.TradingAccountID,
		ClientOrderID:    spec.ClientOrderID, ExchangeSymbol: spec.InstrumentID,
		OrderType: string(spec.Type), TimeInForce: string(spec.FillPolicy),
		Side: string(spec.Side), PositionSide: string(spec.PositionSide),
		Quantity: spec.Quantity.String(), ReferencePrice: spec.ReferencePrice.String(),
		ReferencePriceAt: spec.ReferencePriceAt.UnixMilli(),
		ReduceOnly:       spec.ReducePositionOnly,
		OwnerType:        string(spec.Owner.Type), OwnerID: spec.Owner.OwnerID,
		LogicalAccountID: spec.Owner.LogicalAccountID,
		State:            "PENDING", Version: 1,
	}
	if spec.LimitPrice != nil {
		value := spec.LimitPrice.String()
		record.LimitPrice = &value
	}
	if spec.Owner.RunnerID != nil {
		record.RunnerID = *spec.Owner.RunnerID
	}
	if err := s.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateOrder(record)
	}); err != nil {
		return orderdomain.Order{}, err
	}
	return orderdomain.Order{
		ID: shared.OrderID(id), Spec: spec, State: orderdomain.Pending, Version: 1,
	}, nil
}

func (s *operatorOrderStub) Submit(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	s.fixture.trace = append(s.fixture.trace, "submit:"+orderID)
	s.submitCalls++
	if s.leaveOpen[orderID] {
		return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Open}, nil
	}
	if err := setOperatorOrderState(ctx, s.store, spaceID, orderID, "FILLED"); err != nil {
		return orderdomain.Order{}, err
	}
	return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Filled}, nil
}

func (s *operatorOrderStub) Cancel(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	s.fixture.trace = append(s.fixture.trace, "cancel:"+orderID)
	if err := s.cancelError[orderID]; err != nil {
		return orderdomain.Order{}, err
	}
	if s.leaveOpen[orderID] {
		return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Open}, nil
	}
	if err := setOperatorOrderState(ctx, s.store, spaceID, orderID, "CANCELED"); err != nil {
		return orderdomain.Order{}, err
	}
	return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Canceled}, nil
}

func (s *operatorOrderStub) DiscardPending(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	return s.Cancel(ctx, spaceID, orderID)
}

func (s *operatorOrderStub) ResolveUnknown(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	return s.Cancel(ctx, spaceID, orderID)
}

func (s *operatorOrderStub) RecoverCancel(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	return s.Cancel(ctx, spaceID, orderID)
}

func setOperatorOrderState(
	ctx context.Context,
	tradeStore *store.Store,
	spaceID string,
	orderID string,
	state string,
) error {
	record, err := tradeStore.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return err
	}
	expected := record.Version
	record.State = state
	record.Version++
	record.FinishedAt = time.Now().UnixMilli()
	return tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	})
}

type operatorSyncStub struct {
	fixture *operatorFixture
	fail    map[string]error
	onSync  func(context.Context, string, int) error
}

func (s *operatorSyncStub) SyncAccount(
	ctx context.Context,
	tradingAccountID string,
) error {
	s.fixture.trace = append(s.fixture.trace, "sync:"+tradingAccountID)
	if err := s.fail[tradingAccountID]; err != nil {
		return err
	}
	if s.onSync == nil {
		return nil
	}
	return s.onSync(ctx, tradingAccountID, s.callsFor(tradingAccountID))
}

func (s *operatorSyncStub) callsFor(accountID string) int {
	count := 0
	for _, entry := range s.fixture.trace {
		if entry == "sync:"+accountID {
			count++
		}
	}
	return count
}

type operatorPriceStub struct {
	quote Quote
	err   error
	calls *[]string
}

func (s operatorPriceStub) LatestPrice(
	_ context.Context,
	_ string,
	symbol string,
) (Quote, error) {
	if s.calls != nil {
		*s.calls = append(*s.calls, symbol)
	}
	return s.quote, s.err
}

func traceIndex(trace []string, expected string) int {
	for index, current := range trace {
		if current == expected {
			return index
		}
	}
	return -1
}
