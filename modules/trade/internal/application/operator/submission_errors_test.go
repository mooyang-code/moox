package operator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type deadlinePrice struct{}

func (deadlinePrice) LatestPrice(ctx context.Context, _, _ string) (Quote, error) {
	<-ctx.Done()
	return Quote{}, ctx.Err()
}

type rejectedSubmissionAdapter struct{ *submissionAdapter }

func (a rejectedSubmissionAdapter) Adapter(string) (execution.ExecutionAdapter, error) { return a, nil }
func (a rejectedSubmissionAdapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	a.posts++
	return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorInsufficientBalance}
}

type databaseSubmissionSyncer struct{ store *store.Store }

func (s databaseSubmissionSyncer) SyncAccount(ctx context.Context, id string) error {
	return s.store.DBForTest().WithContext(ctx).Exec("UPDATE t_trading_accounts SET c_mtime=CURRENT_TIMESTAMP WHERE c_trading_account_id=?", id).Error
}

func TestSubmissionJoinedExchangeAndSyncDatabaseFailurePreservesRejection(t *testing.T) {
	f, s, adapter := realSubmissionService(t)
	result, err := s.SubmitOrder(context.Background(), submissionCommand(f))
	require.NoError(t, err)
	orders := s.Orders.(*orderapp.Service)
	orders.Adapters = rejectedSubmissionAdapter{adapter}
	orders.Syncer = databaseSubmissionSyncer{f.store}
	dbErr := errors.New("sync update database failure")
	callback := "test:submission_sync_failure"
	require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register(callback, func(db *gorm.DB) {
		if strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "UPDATE t_trading_accounts") {
			db.AddError(dbErr)
		}
	}))
	t.Cleanup(func() { require.NoError(t, f.store.DBForTest().Callback().Raw().Remove(callback)) })
	err = s.ResumeOperatorAction(context.Background(), result.Action)
	require.ErrorIs(t, err, dbErr)
	child, err := f.store.GetOrder(context.Background(), result.Action.SpaceID, result.Order.OrderID)
	require.NoError(t, err)
	require.Equal(t, "REJECTED", child.State)
	require.Equal(t, "0", child.RemainingReservedQuantity)
	action, err := f.store.GetOperatorAction(context.Background(), result.Action.SpaceID, result.Action.ActionID)
	require.NoError(t, err)
	require.Equal(t, "FAILED", action.Status)
}

type deadlineOrders struct{ OrderService }

func (s deadlineOrders) Place(ctx context.Context, _ string, _ orderdomain.OrderSpec) (orderdomain.Order, error) {
	<-ctx.Done()
	return orderdomain.Order{}, &orderapp.AccountExecutionError{Err: ctx.Err()}
}
func (s deadlineOrders) Submit(ctx context.Context, _, _ string) (orderdomain.Order, error) {
	<-ctx.Done()
	return orderdomain.Order{}, &orderapp.AccountExecutionError{Err: ctx.Err()}
}

type deadlineSubmitOrders struct{ OrderService }

func (s deadlineSubmitOrders) Submit(ctx context.Context, _, _ string) (orderdomain.Order, error) {
	<-ctx.Done()
	return orderdomain.Order{}, &orderapp.AccountExecutionError{Err: ctx.Err()}
}

type deadlineUnknownOrders struct{ OrderService }

func (s deadlineUnknownOrders) ResolveUnknown(ctx context.Context, _, _ string) (orderdomain.Order, error) {
	<-ctx.Done()
	return orderdomain.Order{}, &orderapp.AccountExecutionError{Err: ctx.Err()}
}

func TestSubmissionInitialDisabledHasNoDurableSideEffects(t *testing.T) {
	f, s, _ := realSubmissionService(t)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trading_accounts SET c_status='DISABLED' WHERE c_trading_account_id='account-a'").Error)
	_, err := s.SubmitOrder(context.Background(), submissionCommand(f))
	require.Error(t, err)
	for _, table := range []string{"t_operator_actions", "t_trade_orders"} {
		var n int64
		require.NoError(t, f.store.DBForTest().Table(table).Count(&n).Error)
		require.Zero(t, n)
	}
}

func TestSubmissionAllErrorLeavesMustBeAccountLocal(t *testing.T) {
	accountErr := &orderapp.AccountExecutionError{Err: errors.New("account unavailable")}
	dbErr := errors.New("sync database unavailable")
	require.False(t, submissionAccountError(errors.Join(accountErr, dbErr)))
	require.True(t, submissionAccountError(errors.Join(accountErr, tradingaccount.ErrAccountNotExecutable)))
	require.ErrorIs(t, submissionRecoveryError(ManualOrderResult{Action: store.OperatorActionRecord{Status: "RUNNING"}}, errors.Join(submissionDeferredError{cause: accountErr}, dbErr)), dbErr)
}

func TestSubmissionCanceledDependenciesStillPersistDiagnostics(t *testing.T) {
	for _, path := range []string{"price", "place", "recovery-link", "recovery-submit", "recovery-unknown"} {
		t.Run(path, func(t *testing.T) {
			f, s, _ := realSubmissionService(t)
			command := submissionCommand(f)
			var action store.OperatorActionRecord
			if strings.HasPrefix(path, "recovery") {
				result, err := s.SubmitOrder(context.Background(), command)
				require.NoError(t, err)
				action = result.Action
				if path == "recovery-unknown" {
					require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_orders SET c_state='SUBMIT_UNKNOWN',c_submitted_at=? WHERE c_order_id=?", f.now.UnixMilli(), result.Order.OrderID).Error)
				}
			}
			if path == "price" {
				s.Prices = deadlinePrice{}
			} else {
				switch path {
				case "recovery-submit":
					s.Orders = deadlineSubmitOrders{OrderService: s.Orders}
				case "recovery-unknown":
					s.Orders = deadlineUnknownOrders{OrderService: s.Orders}
				default:
					s.Orders = deadlineOrders{OrderService: s.Orders}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if strings.HasPrefix(path, "recovery") {
				require.NoError(t, s.ResumeOperatorAction(ctx, action))
			} else {
				result, err := s.SubmitOrder(ctx, command)
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.Equal(t, command.ActionID, result.Action.ActionID)
			}
			persisted, err := f.store.GetOperatorAction(context.Background(), command.SpaceID, command.ActionID)
			require.NoError(t, err)
			require.Contains(t, persisted.LastError, context.DeadlineExceeded.Error())
			require.Equal(t, "RUNNING", persisted.Status)
		})
	}
}

func TestSubmissionDiagnosticFailureKeepsCauseAndDurableIdentity(t *testing.T) {
	f, s, _ := realSubmissionService(t)
	result, err := s.SubmitOrder(context.Background(), submissionCommand(f))
	require.NoError(t, err)
	dbErr := errors.New("diagnostic update failed")
	callback := "test:diagnostic_failure"
	require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register(callback, func(db *gorm.DB) {
		if strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "UPDATE t_operator_actions") {
			db.AddError(dbErr)
		}
	}))
	t.Cleanup(func() { require.NoError(t, f.store.DBForTest().Callback().Raw().Remove(callback)) })
	cause := &orderapp.AccountExecutionError{Err: context.DeadlineExceeded}
	got, err := s.submissionOrderError(context.Background(), result.Action, cause)
	require.ErrorIs(t, err, dbErr)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, result.Action.ActionID, got.Action.ActionID)
	require.Equal(t, result.Order.OrderID, got.Order.OrderID)
}

func TestSubmissionPlacedChildLinkFailureReturnsBothDurableIdentities(t *testing.T) {
	f, s, _ := realSubmissionService(t)
	dbErr := errors.New("child action link database failure")
	callback := "test:submission_link_failure"
	require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register(callback, func(db *gorm.DB) {
		if strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "UPDATE t_operator_actions") {
			db.AddError(dbErr)
		}
	}))
	t.Cleanup(func() { require.NoError(t, f.store.DBForTest().Callback().Raw().Remove(callback)) })
	command := submissionCommand(f)
	got, err := s.SubmitOrder(context.Background(), command)
	require.ErrorIs(t, err, dbErr)
	require.Equal(t, command.ActionID, got.Action.ActionID)
	require.NotEmpty(t, got.Order.OrderID)
	child, err := f.store.GetOrder(context.Background(), command.SpaceID, got.Order.OrderID)
	require.NoError(t, err)
	require.Equal(t, "PENDING", child.State)
}

func TestSubmissionReplayCanceledLockKeepsKnownIdentity(t *testing.T) {
	f, s, _ := realSubmissionService(t)
	command := submissionCommand(f)
	accepted, err := s.SubmitOrder(context.Background(), command)
	require.NoError(t, err)
	unlock := f.store.LockLogicalAccount(command.SpaceID, command.LogicalAccountID)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got, err := s.SubmitOrder(ctx, command)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, accepted.Action.ActionID, got.Action.ActionID)
	require.Equal(t, accepted.Order.OrderID, got.Order.OrderID)
}

func TestSubmissionCrashGapRecoveryErrorsKeepTrustedChild(t *testing.T) {
	for _, path := range []string{"place", "link"} {
		t.Run(path, func(t *testing.T) {
			f, s, _ := realSubmissionService(t)
			command := submissionCommand(f)
			first, err := s.SubmitOrder(context.Background(), command)
			require.NoError(t, err)
			require.NoError(t, f.store.DBForTest().Exec("UPDATE t_operator_actions SET c_result_json=? WHERE c_action_id=?", `{"deadline_at":62000}`, command.ActionID).Error)
			cause := errors.New("crash gap " + path + " failure")
			if path == "place" {
				s.Orders = rejectedManualPlace{OrderService: s.Orders, cause: cause}
			} else {
				callback := "test:crashgap_link_failure"
				require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register(callback, func(db *gorm.DB) {
					if strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "UPDATE t_operator_actions") {
						db.AddError(cause)
					}
				}))
				t.Cleanup(func() { require.NoError(t, f.store.DBForTest().Callback().Raw().Remove(callback)) })
			}
			got, err := s.SubmitOrder(context.Background(), command)
			require.ErrorIs(t, err, cause)
			require.Equal(t, first.Action.ActionID, got.Action.ActionID)
			require.Equal(t, first.Order.OrderID, got.Order.OrderID)
			require.Equal(t, command.ActionID, got.Order.OwnerID)
			persisted, readErr := f.store.GetOperatorAction(context.Background(), command.SpaceID, command.ActionID)
			require.NoError(t, readErr)
			require.Equal(t, persisted.ResultJSON, got.Action.ResultJSON)
			require.NotContains(t, *got.Action.ResultJSON, "order_id")
		})
	}
}

func TestSubmissionCrashGapNeverReturnsAnotherActionsChild(t *testing.T) {
	f, s, _ := realSubmissionService(t)
	command := submissionCommand(f)
	_, err := s.SubmitOrder(context.Background(), command)
	require.NoError(t, err)
	command.ActionID = "different-action"
	got, err := s.SubmitOrder(context.Background(), command)
	require.ErrorIs(t, err, orderapp.ErrIdempotencyConflict)
	require.Empty(t, got.Order.OrderID)
}

type submissionNilUnwrapper struct{}

func (submissionNilUnwrapper) Error() string { return "unclassified wrapper" }
func (submissionNilUnwrapper) Unwrap() error { return nil }
func TestSubmissionUnmatchedNilUnwrapperIsNotAccountLocal(t *testing.T) {
	for _, err := range []error{submissionNilUnwrapper{}, &exchange.Error{Kind: exchange.ErrorRejected}} {
		require.False(t, submissionAccountError(err))
		require.False(t, submissionBusinessError(err))
	}
	require.True(t, submissionAccountError(nil))
}
