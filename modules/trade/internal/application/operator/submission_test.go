package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func submissionCommand(f *operatorFixture) SubmitOrderCommand {
	return SubmitOrderCommand{LogicalAccountID: "logical-1", ManualOrderCommand: ManualOrderCommand{SpaceID: "space-1", ActionID: "submit-1", TradingAccountID: "account-a", ClientOrderID: "submit-client", InstrumentID: f.instrumentID(), Type: exchange.OrderTypeMarket, Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"), Reason: "ordinary order"}}
}

type submissionSources struct{ f *operatorFixture }

func (s submissionSources) ExecutionEligibility(ctx context.Context, id string) (tradingaccount.Account, error) {
	record, err := s.f.store.GetTradingAccount(ctx, "space-1", id)
	if err != nil {
		return tradingaccount.Account{}, err
	}
	if record.Status != "ENABLED" {
		return tradingaccount.Account{}, ErrInvalidCommand
	}
	return tradingaccount.Account{ID: id, SpaceID: "space-1", Exchange: exchange.ExchangeBinance, MarketType: exchange.MarketTypeSpot, ExecutionMode: exchange.ExecutionModeLive, Environment: exchange.AccountEnvironmentTestnet, Status: exchange.AccountStatusEnabled, Ready: true, SettlementAsset: "USDT", LastSyncAt: s.f.now, Snapshot: exchange.AccountSnapshot{AvailableFunds: shared.MustDecimal("100000"), Balances: []exchange.AssetBalance{{Asset: "USDT", Available: shared.MustDecimal("100000")}}}}, nil
}

func (s submissionSources) GetInstrument(context.Context, exchange.Exchange, exchange.MarketType, string) (exchange.Instrument, error) {
	return exchange.Instrument{Exchange: exchange.ExchangeBinance, MarketType: exchange.MarketTypeSpot, ExchangeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT", Status: "TRADING", ExchangeQuantityStep: shared.MustDecimal("0.001"), MinExchangeQuantity: shared.MustDecimal("0.001"), PriceTick: shared.MustDecimal("0.1"), MinNotional: shared.MustDecimal("5")}, nil
}

type submissionAdapter struct {
	execution.ExecutionAdapter
	posts, queries int
	lookupErr      error
}

func (a *submissionAdapter) Adapter(string) (execution.ExecutionAdapter, error) { return a, nil }
func (a *submissionAdapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	a.posts++
	return exchange.Order{ExchangeOrderID: fmt.Sprintf("exchange-%d", a.posts), Status: exchange.OrderStatusOpen}, nil
}
func (a *submissionAdapter) GetOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error) {
	a.queries++
	return exchange.Order{ExchangeOrderID: "exchange-recovered", Status: exchange.OrderStatusOpen}, a.lookupErr
}

func (a *submissionAdapter) ListRecentFills(context.Context, shared.ExchangeSymbol, string) ([]exchange.Fill, string, error) {
	return nil, "", nil
}

func realSubmissionService(t *testing.T) (*operatorFixture, *Service, *submissionAdapter) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode='MANUAL',c_owner_runner_id='',c_automation_state='PAUSED',c_pause_reason='manual' WHERE c_logical_account_id='logical-1'").Error)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trading_accounts SET c_execution_mode='LIVE',c_live_environment='TESTNET',c_credential_secret_id='test-only'").Error)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_instruments SET c_environment='TESTNET'").Error)
	source := submissionSources{f: f}
	adapter := &submissionAdapter{}
	sequence := 0
	orders := &orderapp.Service{Store: f.store, Validator: orderapp.Validator{Accounts: source, Instruments: source, Now: func() time.Time { return f.now }, MaxReferenceAge: time.Second}, Adapters: adapter, Now: func() time.Time { return f.now }, NewOrderID: func() string { sequence++; return fmt.Sprintf("real-%d", sequence) }}
	s := f.service()
	s.Orders = orders
	return f, s, adapter
}

func TestSubmitOrderRealStoreReplayConflictAndMultipleActive(t *testing.T) {
	f, s, adapter := realSubmissionService(t)
	ctx := context.Background()
	command := submissionCommand(f)
	first, err := s.SubmitOrder(ctx, command)
	require.NoError(t, err)
	require.Equal(t, "PENDING", first.Order.State)
	require.Empty(t, f.trace)
	require.Zero(t, adapter.posts)
	var reservations int64
	require.NoError(t, f.store.DBForTest().Table("t_trade_orders").Where("c_remaining_reserved_quantity <> '0'").Count(&reservations).Error)
	require.EqualValues(t, 1, reservations)
	replay, err := s.SubmitOrder(ctx, command)
	require.NoError(t, err)
	require.Equal(t, first.Order.OrderID, replay.Order.OrderID)
	changed := command
	changed.Quantity = shared.MustDecimal("2")
	_, err = s.SubmitOrder(ctx, changed)
	require.ErrorIs(t, err, store.ErrConflict)
	changed = command
	changed.ActionID = "other-action"
	_, err = s.SubmitOrder(ctx, changed)
	require.ErrorIs(t, err, orderapp.ErrIdempotencyConflict)
	changed = command
	changed.ActionID = "second-action"
	changed.ClientOrderID = "second-client"
	second, err := s.SubmitOrder(ctx, changed)
	require.NoError(t, err)
	require.NotEqual(t, first.Order.OrderID, second.Order.OrderID)
	require.NoError(t, s.ResumeOperatorAction(ctx, first.Action))
	require.Equal(t, 1, adapter.posts)
	stillPending, err := f.store.GetOrder(ctx, "space-1", second.Order.OrderID)
	require.NoError(t, err)
	require.Equal(t, "PENDING", stillPending.State)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trading_accounts SET c_status='DISABLED' WHERE c_trading_account_id='account-a'").Error)
	replay, err = s.SubmitOrder(ctx, command)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", replay.Action.Status)
	require.Equal(t, first.Order.OrderID, replay.Order.OrderID)
}

func TestSubmitOrderCrashLinkRepairDoesNotPost(t *testing.T) {
	f, s, adapter := realSubmissionService(t)
	command := submissionCommand(f)
	first, err := s.SubmitOrder(context.Background(), command)
	require.NoError(t, err)
	progress := manualOrderActionResult{DeadlineAt: command.DeadlineAt}
	progress.DeadlineAt = f.now.Add(time.Minute).UnixMilli()
	raw, err := json.Marshal(progress)
	require.NoError(t, err)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_operator_actions SET c_result_json=? WHERE c_action_id=?", string(raw), command.ActionID).Error)
	recovered, err := s.SubmitOrder(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, first.Order.OrderID, recovered.Order.OrderID)
	require.Zero(t, adapter.posts)
	var count int64
	require.NoError(t, f.store.DBForTest().Table("t_trade_orders").Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestSubmitOrderExplicitDeadlineAndUnknownQueryFirst(t *testing.T) {
	f, s, adapter := realSubmissionService(t)
	command := submissionCommand(f)
	command.DeadlineAt = f.now.Add(time.Second).UnixMilli()
	result, err := s.SubmitOrder(context.Background(), command)
	require.NoError(t, err)
	require.Contains(t, result.Action.RequestJSON, `"deadline_at":3000`)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_orders SET c_state='SUBMIT_UNKNOWN',c_submitted_at=? WHERE c_order_id=?", f.now.UnixMilli(), result.Order.OrderID).Error)
	f.now = f.now.Add(time.Hour)
	s.ManualSubmitWindow = 2 * time.Hour
	adapter.lookupErr = errors.New("network temporarily unavailable")
	require.NoError(t, s.ResumeOperatorAction(context.Background(), result.Action))
	require.Equal(t, 1, adapter.queries)
	require.Zero(t, adapter.posts)
	action, err := f.store.GetOperatorAction(context.Background(), command.SpaceID, command.ActionID)
	require.NoError(t, err)
	progress, err := s.manualProgress(context.Background(), &action)
	require.NoError(t, err)
	require.Equal(t, command.DeadlineAt, progress.DeadlineAt)
	adapter.lookupErr = nil
	require.NoError(t, s.ResumeOperatorAction(context.Background(), action))
	require.Equal(t, 2, adapter.queries)
	require.Zero(t, adapter.posts)
	action, err = f.store.GetOperatorAction(context.Background(), command.SpaceID, command.ActionID)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", action.Status)
}

func TestSubmitOrderPendingExpiredOrModeChangedReleasesReservation(t *testing.T) {
	for _, modeChanged := range []bool{false, true} {
		t.Run(fmt.Sprint(modeChanged), func(t *testing.T) {
			f, s, adapter := realSubmissionService(t)
			command := submissionCommand(f)
			result, err := s.SubmitOrder(context.Background(), command)
			require.NoError(t, err)
			if modeChanged {
				require.NoError(t, f.store.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode='STRATEGY' WHERE c_logical_account_id='logical-1'").Error)
			} else {
				f.now = f.now.Add(time.Hour)
			}
			err = s.ResumeOperatorAction(context.Background(), result.Action)
			require.NoError(t, err)
			require.Zero(t, adapter.posts)
			child, err := f.store.GetOrder(context.Background(), command.SpaceID, result.Order.OrderID)
			require.NoError(t, err)
			require.Equal(t, "CANCELED", child.State)
			require.Empty(t, child.ExchangeOrderID)
			action, err := f.store.GetOperatorAction(context.Background(), command.SpaceID, command.ActionID)
			require.NoError(t, err)
			require.Equal(t, "FAILED", action.Status)
			var remaining string
			require.NoError(t, f.store.DBForTest().Raw("SELECT c_remaining_reserved_quantity FROM t_trade_orders WHERE c_order_id=?", child.OrderID).Scan(&remaining).Error)
			require.Equal(t, "0", remaining)
		})
	}
}

func TestSubmitOrderAcceptsWithoutTakeoverOrPost(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode='MANUAL',c_owner_runner_id='',c_automation_state='PAUSED',c_pause_reason='manual' WHERE c_logical_account_id='logical-1'").Error)
	got, err := f.service().SubmitOrder(context.Background(), submissionCommand(f))
	require.NoError(t, err)
	require.Equal(t, "RUNNING", got.Action.Status)
	require.Equal(t, "PENDING", got.Order.State)
	require.Equal(t, []string{"place:submit-client"}, f.trace)
	require.Zero(t, f.orders.submitCalls)
}

func TestSubmitOrderStrategyWithoutOwnerRejectedBeforeWrites(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_logical_accounts SET c_owner_runner_id='',c_automation_state='PAUSED',c_pause_reason='unbound' WHERE c_logical_account_id='logical-1'").Error)
	_, err := f.service().SubmitOrder(context.Background(), submissionCommand(f))
	require.ErrorIs(t, err, ErrInvalidCommand)
	var count int64
	require.NoError(t, f.store.DBForTest().Table("t_operator_actions").Count(&count).Error)
	require.Zero(t, count)
	require.Empty(t, f.trace)
}

func TestSubmitOrderLogicalLockCancellation(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	unlock := f.store.LockLogicalAccount("space-1", "logical-1")
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := f.service().SubmitOrder(ctx, submissionCommand(f))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var count int64
	require.NoError(t, f.store.DBForTest().Table("t_operator_actions").Count(&count).Error)
	require.Zero(t, count)
}

func TestSubmitOrderRealStrategyRejectsAllSideEffects(t *testing.T) {
	f, s, adapter := realSubmissionService(t)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode='STRATEGY' WHERE c_logical_account_id='logical-1'").Error)
	_, err := s.SubmitOrder(context.Background(), submissionCommand(f))
	require.ErrorIs(t, err, ErrInvalidCommand)
	for _, table := range []string{"t_operator_actions", "t_trade_orders"} {
		var count int64
		require.NoError(t, f.store.DBForTest().Table(table).Count(&count).Error)
		require.Zero(t, count)
	}
	require.Zero(t, adapter.posts)
	require.Empty(t, f.trace)
}

func TestSubmitOrderDeadlineIdentityAndRecoveryLock(t *testing.T) {
	f, s, _ := realSubmissionService(t)
	command := submissionCommand(f)
	command.DeadlineAt = f.now.Add(10 * time.Second).UnixMilli()
	result, err := s.SubmitOrder(context.Background(), command)
	require.NoError(t, err)
	changed := command
	changed.DeadlineAt++
	_, err = s.SubmitOrder(context.Background(), changed)
	require.ErrorIs(t, err, store.ErrConflict)
	unlock := f.store.LockLogicalAccount(command.SpaceID, command.LogicalAccountID)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.NoError(t, s.ResumeOperatorAction(ctx, result.Action))
}

func TestManualOrderExplicitDeadlineRoundTrip(t *testing.T) {
	f := newOperatorFixture(t, exchange.MarketTypeSpot)
	command := submissionCommand(f).ManualOrderCommand
	command.DeadlineAt = f.now.Add(12 * time.Second).UnixMilli()
	result, err := f.service().PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)
	recovered, err := manualOrderCommand(result.Action)
	require.NoError(t, err)
	require.Equal(t, command.DeadlineAt, recovered.DeadlineAt)
	progress, err := f.service().manualProgress(context.Background(), &result.Action)
	require.NoError(t, err)
	require.Equal(t, command.DeadlineAt, progress.DeadlineAt)
	command.DeadlineAt++
	_, err = f.service().PlaceManualOrder(context.Background(), command)
	require.ErrorIs(t, err, store.ErrConflict)
}

func TestSubmitOrderUnknownNotFoundAfterDeadlineDiscardsWithoutPost(t *testing.T) {
	f, s, adapter := realSubmissionService(t)
	command := submissionCommand(f)
	result, err := s.SubmitOrder(context.Background(), command)
	require.NoError(t, err)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_orders SET c_state='SUBMIT_UNKNOWN',c_submitted_at=? WHERE c_order_id=?", f.now.UnixMilli(), result.Order.OrderID).Error)
	f.now = f.now.Add(time.Hour)
	adapter.lookupErr = &exchange.Error{Kind: exchange.ErrorOrderNotFound}
	require.NoError(t, s.ResumeOperatorAction(context.Background(), result.Action))
	require.Equal(t, 1, adapter.queries)
	require.Zero(t, adapter.posts)
	child, err := f.store.GetOrder(context.Background(), command.SpaceID, result.Order.OrderID)
	require.NoError(t, err)
	require.Equal(t, "CANCELED", child.State)
	require.Equal(t, "0", child.RemainingReservedQuantity)
	action, err := f.store.GetOperatorAction(context.Background(), command.SpaceID, command.ActionID)
	require.NoError(t, err)
	require.Equal(t, "FAILED", action.Status)
}

func TestSubmissionRecoveryNeverSwallowsInfrastructureErrors(t *testing.T) {
	failure := errors.New("database is closed")
	require.ErrorIs(t, submissionRecoveryError(ManualOrderResult{Action: store.OperatorActionRecord{Status: "FAILED"}}, failure), failure)
}

type submissionResumerFunc func(context.Context, store.OperatorActionRecord) error

func (f submissionResumerFunc) ResumeOperatorAction(ctx context.Context, action store.OperatorActionRecord) error {
	return f(ctx, action)
}

func TestSubmitOrderWorkerAdvancesDurableActionWithoutSyncer(t *testing.T) {
	f, s, adapter := realSubmissionService(t)
	s.Syncer = nil
	command := submissionCommand(f)
	result, err := s.SubmitOrder(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "PENDING", result.Order.State)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	worker := &traderuntime.OperatorWorker{Actions: f.store, Resumer: submissionResumerFunc(func(ctx context.Context, action store.OperatorActionRecord) error {
		err := s.ResumeOperatorAction(ctx, action)
		cancel()
		return err
	})}
	require.ErrorIs(t, worker.Run(ctx), context.Canceled)
	require.True(t, worker.Snapshot().Ready)
	require.Empty(t, worker.Snapshot().LastError)
	require.Equal(t, 1, adapter.posts)
	action, err := f.store.GetOperatorAction(context.Background(), command.SpaceID, command.ActionID)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", action.Status)
}

func TestSubmitOrderTransientFailureReturnsDurableActionAndOriginalError(t *testing.T) {
	for _, placeFails := range []bool{false, true} {
		t.Run(fmt.Sprint(placeFails), func(t *testing.T) {
			f, s, adapter := realSubmissionService(t)
			cause := errors.New("temporary dependency failure")
			if placeFails {
				s.Orders = rejectedManualPlace{OrderService: s.Orders, cause: &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "place", Err: cause}}
			} else {
				s.Prices = operatorPriceStub{err: cause}
			}
			result, err := s.SubmitOrder(context.Background(), submissionCommand(f))
			require.ErrorIs(t, err, cause)
			require.Equal(t, "RUNNING", result.Action.Status)
			require.Equal(t, cause.Error(), result.Action.LastError)
			require.Empty(t, result.Order.OrderID)
			require.NoError(t, s.ResumeOperatorAction(context.Background(), result.Action))
			require.Zero(t, adapter.posts)
		})
	}
}

func TestSubmitOrderWorkerPlaceDatabaseFailureRemainsUnhealthy(t *testing.T) {
	f, s, adapter := realSubmissionService(t)
	s.Prices = operatorPriceStub{err: errors.New("initial quote unavailable")}
	command := submissionCommand(f)
	result, err := s.SubmitOrder(context.Background(), command)
	require.Error(t, err)
	require.Equal(t, "RUNNING", result.Action.Status)
	s.Prices = f.prices
	dbFailure := errors.New("injected order insert database failure")
	injected := false
	callback := "test:submission_insert_failure"
	require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register(callback, func(db *gorm.DB) {
		if !injected && strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "INSERT INTO t_trade_orders") {
			injected = true
			db.AddError(dbFailure)
		}
	}))
	t.Cleanup(func() { require.NoError(t, f.store.DBForTest().Callback().Raw().Remove(callback)) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var recoveryErr error
	worker := &traderuntime.OperatorWorker{Actions: f.store, Resumer: submissionResumerFunc(func(ctx context.Context, action store.OperatorActionRecord) error {
		recoveryErr = s.ResumeOperatorAction(ctx, action)
		cancel()
		return recoveryErr
	})}
	require.ErrorIs(t, worker.Run(ctx), context.Canceled)
	require.True(t, injected)
	require.ErrorIs(t, recoveryErr, dbFailure)
	require.False(t, worker.Snapshot().Ready)
	require.Contains(t, worker.Snapshot().LastError, dbFailure.Error())
	require.Zero(t, adapter.posts)
	action, err := f.store.GetOperatorAction(context.Background(), command.SpaceID, command.ActionID)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", action.Status)
	require.Equal(t, dbFailure.Error(), action.LastError)
	var count int64
	require.NoError(t, f.store.DBForTest().Table("t_trade_orders").Count(&count).Error)
	require.Zero(t, count)
}

func TestSubmitOrderRecoveryDatabaseFailuresRemainVisible(t *testing.T) {
	for _, path := range []string{"unknown", "submit", "submit-outcome", "discard"} {
		t.Run(path, func(t *testing.T) {
			f, s, adapter := realSubmissionService(t)
			command := submissionCommand(f)
			result, err := s.SubmitOrder(context.Background(), command)
			require.NoError(t, err)
			wantState := "PENDING"
			if path == "unknown" {
				wantState = "SUBMIT_UNKNOWN"
				require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_orders SET c_state='SUBMIT_UNKNOWN',c_submitted_at=? WHERE c_order_id=?", f.now.UnixMilli(), result.Order.OrderID).Error)
			}
			if path == "discard" {
				f.now = f.now.Add(time.Hour)
			}
			if path == "submit-outcome" {
				wantState = "SUBMITTING"
			}
			cause := errors.New("injected " + path + " order update database failure")
			injected := false
			callback := "test:submission_recovery_failure"
			require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register(callback, func(db *gorm.DB) {
				if !injected && strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "UPDATE t_trade_orders") && (path != "submit-outcome" || adapter.posts == 1) {
					injected = true
					db.AddError(cause)
				}
			}))
			t.Cleanup(func() { require.NoError(t, f.store.DBForTest().Callback().Raw().Remove(callback)) })
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var recoveryErr error
			worker := &traderuntime.OperatorWorker{Actions: f.store, Resumer: submissionResumerFunc(func(ctx context.Context, action store.OperatorActionRecord) error {
				recoveryErr = s.ResumeOperatorAction(ctx, action)
				cancel()
				return recoveryErr
			})}
			require.ErrorIs(t, worker.Run(ctx), context.Canceled)
			require.True(t, injected)
			require.ErrorIs(t, recoveryErr, cause)
			require.False(t, worker.Snapshot().Ready)
			child, err := f.store.GetOrder(context.Background(), command.SpaceID, result.Order.OrderID)
			require.NoError(t, err)
			require.Equal(t, wantState, child.State)
			require.Equal(t, result.Order.RemainingReservedQuantity, child.RemainingReservedQuantity)
			action, err := f.store.GetOperatorAction(context.Background(), command.SpaceID, command.ActionID)
			require.NoError(t, err)
			require.Equal(t, "RUNNING", action.Status)
			require.Equal(t, cause.Error(), action.LastError)
			if path == "submit-outcome" {
				require.Equal(t, 1, adapter.posts)
				require.NoError(t, s.ResumeOperatorAction(context.Background(), action))
				require.Equal(t, 1, adapter.posts)
				require.Equal(t, 1, adapter.queries)
			} else {
				require.Zero(t, adapter.posts)
			}
		})
	}
}

func TestSubmitOrderUnconfirmedLookupIsNotInfrastructureFailure(t *testing.T) {
	f, s, adapter := realSubmissionService(t)
	result, err := s.SubmitOrder(context.Background(), submissionCommand(f))
	require.NoError(t, err)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_orders SET c_state='SUBMIT_UNKNOWN',c_submitted_at=? WHERE c_order_id=?", f.now.UnixMilli(), result.Order.OrderID).Error)
	adapter.lookupErr = &exchange.Error{Kind: exchange.ErrorOrderNotFound}
	require.NoError(t, s.ResumeOperatorAction(context.Background(), result.Action))
	require.Zero(t, adapter.posts)
	require.Equal(t, 1, adapter.queries)
	child, err := f.store.GetOrder(context.Background(), "space-1", result.Order.OrderID)
	require.NoError(t, err)
	require.Equal(t, "SUBMIT_UNKNOWN", child.State)
}

func TestSubmitOrderWorkerPermanentRevalidationFailureIsBusinessOutcome(t *testing.T) {
	f, s, adapter := realSubmissionService(t)
	result, err := s.SubmitOrder(context.Background(), submissionCommand(f))
	require.NoError(t, err)
	s.Orders.(*orderapp.Service).Validator.MaxChildNotional = shared.MustDecimal("1")
	require.NoError(t, s.ResumeOperatorAction(context.Background(), result.Action))
	require.Zero(t, adapter.posts)
	action, err := f.store.GetOperatorAction(context.Background(), result.Action.SpaceID, result.Action.ActionID)
	require.NoError(t, err)
	require.Equal(t, "FAILED", action.Status)
	require.Contains(t, action.LastError, orderapp.ErrNotionalLimit.Error())
}
