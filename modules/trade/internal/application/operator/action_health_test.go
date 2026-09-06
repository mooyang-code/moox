package operator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	accountsyncapp "github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestManualRecoverySharedWriteFailureIsNotHealthy(t *testing.T) {
	f, s, _ := realSubmissionService(t)
	accepted, err := s.SubmitOrder(context.Background(), submissionCommand(f))
	require.NoError(t, err)
	raw, err := manualOrderRequestJSON(submissionCommand(f).ManualOrderCommand)
	require.NoError(t, err)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_operator_actions SET c_action_type='MANUAL_ORDER',c_request_json=? WHERE c_action_id=?", raw, accepted.Action.ActionID).Error)
	action, err := f.store.GetOperatorAction(context.Background(), "space-1", accepted.Action.ActionID)
	require.NoError(t, err)
	failure := errors.New("order write unavailable")
	callback := "test:manual_write"
	require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register(callback, func(db *gorm.DB) {
		if strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "UPDATE t_trade_orders") {
			db.AddError(failure)
		}
	}))
	defer f.store.DBForTest().Callback().Raw().Remove(callback)
	require.ErrorIs(t, s.ResumeOperatorAction(context.Background(), action), failure)
}

type deadlineSyncAdapter struct{ execution.ExecutionAdapter }

func (a deadlineSyncAdapter) Adapter(string) (execution.ExecutionAdapter, error) { return a, nil }
func (a deadlineSyncAdapter) ListOpenOrders(ctx context.Context) ([]exchange.Order, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type realOperatorSync struct{ service *accountsyncapp.Service }

func (s realOperatorSync) SyncAccount(ctx context.Context, id string) error {
	_, err := s.service.SyncAccount(ctx, id)
	return err
}

type failedSyncAdapter struct{ *submissionAdapter }

func (a failedSyncAdapter) Adapter(string) (execution.ExecutionAdapter, error) { return a, nil }
func (a failedSyncAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	return nil, errors.New("venue unavailable")
}
func (a failedSyncAdapter) CancelOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}

func TestOperatorRealSyncMixedDatabaseFailureNeverBecomesHealthy(t *testing.T) {
	for _, kind := range []string{"MANUAL_ORDER", "FLATTEN", "CANCEL_ORDER"} {
		t.Run(kind, func(t *testing.T) {
			f, s, a := realSubmissionService(t)
			var orderID string
			if kind == "CANCEL_ORDER" {
				accepted, err := s.SubmitOrder(context.Background(), submissionCommand(f))
				require.NoError(t, err)
				require.NoError(t, s.ResumeOperatorAction(context.Background(), accepted.Action))
				orderID = accepted.Order.OrderID
			}
			syncer := realOperatorSync{&accountsyncapp.Service{Store: f.store, Adapters: failedSyncAdapter{a}, Fills: &consumer.Reducer{Store: f.store}}}
			s.Syncer = syncer
			if kind == "CANCEL_ORDER" {
				orders := s.Orders.(*orderapp.Service)
				orders.Adapters = failedSyncAdapter{a}
				orders.Syncer = syncer
			}
			failure := errors.New("shared readiness update failure")
			callback := "test:mixed_operator_sync"
			require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register(callback, func(db *gorm.DB) {
				if strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "UPDATE t_trading_accounts") {
					db.AddError(failure)
				}
			}))
			defer f.store.DBForTest().Callback().Raw().Remove(callback)
			var err error
			switch kind {
			case "MANUAL_ORDER":
				_, err = s.PlaceManualOrder(context.Background(), submissionCommand(f).ManualOrderCommand)
			case "FLATTEN":
				_, err = s.FlattenLogicalAccount(context.Background(), FlattenCommand{SpaceID: "space-1", ActionID: "mixed-flatten", LogicalAccountID: "logical-1", Reason: "stop"})
			case "CANCEL_ORDER":
				_, err = s.CancelOrder(context.Background(), CancelOrderCommand{SpaceID: "space-1", ActionID: "mixed-cancel", OrderID: orderID, Reason: "stop"})
			}
			require.ErrorIs(t, err, failure)
			require.False(t, submissionAccountError(err))
		})
	}
}

func TestOperatorWorkerRealServicesTimeoutActionThenAdvancesNext(t *testing.T) {
	for _, kind := range []string{"MANUAL_ORDER", "FLATTEN", "CANCEL_ORDER"} {
		t.Run(kind, func(t *testing.T) {
			f, s, adapter := realSubmissionService(t)
			s.Syncer = realOperatorSync{&accountsyncapp.Service{Store: f.store, Adapters: deadlineSyncAdapter{}, Fills: &consumer.Reducer{Store: f.store}}}
			var raw string
			switch kind {
			case "MANUAL_ORDER":
				command := submissionCommand(f).ManualOrderCommand
				command.ActionID = "a-action"
				command.ClientOrderID = "a-client"
				var err error
				raw, err = manualOrderRequestJSON(command)
				require.NoError(t, err)
			case "FLATTEN":
				raw = `{"logical_account_id":"logical-1"}`
			case "CANCEL_ORDER":
				command := submissionCommand(f)
				command.ActionID = "completed"
				command.ClientOrderID = "cancel-child"
				accepted, err := s.SubmitOrder(context.Background(), command)
				require.NoError(t, err)
				require.NoError(t, s.ResumeOperatorAction(context.Background(), accepted.Action))
				raw = `{"order_id":"` + accepted.Order.OrderID + `"}`
				orders := s.Orders.(*orderapp.Service)
				orders.Adapters = cancelDeadlineAdapter{adapter}
				orders.Syncer = databaseSubmissionSyncer{f.store}
			}
			action, _, err := f.store.CreateOperatorAction(context.Background(), store.OperatorActionRecord{SpaceID: "space-1", ActionID: "a-action", LogicalAccountID: "logical-1", ActionType: kind, Reason: "ordinary order", RequestJSON: raw, Status: "RUNNING", CreatedAt: f.now})
			require.NoError(t, err)
			command := submissionCommand(f)
			command.ActionID = "z-action"
			command.ClientOrderID = "z-client"
			second, err := s.SubmitOrder(context.Background(), command)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var resumed []string
			worker := &traderuntime.OperatorWorker{Actions: f.store, ActionTimeout: 40 * time.Millisecond, Resumer: submissionResumerFunc(func(ctx context.Context, current store.OperatorActionRecord) error {
				resumed = append(resumed, current.ActionID)
				err := s.ResumeOperatorAction(ctx, current)
				if current.ActionID == second.Action.ActionID {
					cancel()
				}
				return err
			})}
			require.ErrorIs(t, worker.Run(ctx), context.Canceled)
			require.Equal(t, []string{action.ActionID, second.Action.ActionID}, resumed)
			require.True(t, worker.Snapshot().Ready, worker.Snapshot().LastError)
			persisted, err := f.store.GetOperatorAction(context.Background(), "space-1", action.ActionID)
			require.NoError(t, err)
			require.Equal(t, "RUNNING", persisted.Status)
			require.Contains(t, persisted.LastError, "deadline")
			progressed, err := f.store.GetOperatorAction(context.Background(), "space-1", second.Action.ActionID)
			require.NoError(t, err)
			require.Equal(t, "COMPLETED", progressed.Status)
		})
	}
}

type cancelDeadlineAdapter struct{ *submissionAdapter }

func (a cancelDeadlineAdapter) Adapter(string) (execution.ExecutionAdapter, error) { return a, nil }
func (a cancelDeadlineAdapter) CancelOrder(ctx context.Context, _ shared.ExchangeSymbol, _ string) (exchange.Order, error) {
	<-ctx.Done()
	return exchange.Order{}, ctx.Err()
}

func TestCancelDeadlineRemainsRecoverableWithIdentity(t *testing.T) {
	f, s, a := realSubmissionService(t)
	accepted, err := s.SubmitOrder(context.Background(), submissionCommand(f))
	require.NoError(t, err)
	require.NoError(t, s.ResumeOperatorAction(context.Background(), accepted.Action))
	orders := s.Orders.(*orderapp.Service)
	orders.Adapters = cancelDeadlineAdapter{a}
	orders.Syncer = databaseSubmissionSyncer{f.store}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result, err := s.CancelOrder(ctx, CancelOrderCommand{SpaceID: "space-1", ActionID: "cancel-1", OrderID: accepted.Order.OrderID, Reason: "stop"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "cancel-1", result.Action.ActionID)
	require.Equal(t, accepted.Order.OrderID, result.Order.OrderID)
	currentOrder, readErr := f.store.GetOrder(context.Background(), "space-1", accepted.Order.OrderID)
	require.NoError(t, readErr)
	require.Equal(t, "CANCEL_UNKNOWN", result.Order.State)
	require.Equal(t, currentOrder, result.Order)
	persisted, err := f.store.GetOperatorAction(context.Background(), "space-1", "cancel-1")
	require.NoError(t, err)
	require.Equal(t, "RUNNING", persisted.Status)
	require.Contains(t, persisted.LastError, "deadline")
}

func TestCancelDiagnosticOrderReadFailurePreservesErrorAndIdentity(t *testing.T) {
	f, s, a := realSubmissionService(t)
	accepted, err := s.SubmitOrder(context.Background(), submissionCommand(f))
	require.NoError(t, err)
	require.NoError(t, s.ResumeOperatorAction(context.Background(), accepted.Action))
	orders := s.Orders.(*orderapp.Service)
	orders.Adapters = cancelDeadlineAdapter{a}
	orders.Syncer = databaseSubmissionSyncer{f.store}
	unknownWritten := false
	injected := false
	failure := errors.New("diagnostic order read unavailable")
	require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register("test:cancel_unknown_written", func(db *gorm.DB) {
		if !strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "UPDATE t_trade_orders") {
			return
		}
		for _, value := range db.Statement.Vars {
			if state, ok := value.(string); ok && state == "CANCEL_UNKNOWN" {
				unknownWritten = true
			}
		}
	}))
	defer f.store.DBForTest().Callback().Raw().Remove("test:cancel_unknown_written")
	require.NoError(t, f.store.DBForTest().Callback().Query().Before("gorm:query").Register("test:cancel_diagnostic_read", func(db *gorm.DB) {
		if unknownWritten && db.Statement.Table == "t_trade_orders" {
			injected = true
			db.AddError(failure)
		}
	}))
	defer f.store.DBForTest().Callback().Query().Remove("test:cancel_diagnostic_read")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result, err := s.CancelOrder(ctx, CancelOrderCommand{SpaceID: "space-1", ActionID: "cancel-read", OrderID: accepted.Order.OrderID, Reason: "stop"})
	require.True(t, injected)
	require.ErrorIs(t, err, failure)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "cancel-read", result.Action.ActionID)
	require.Equal(t, accepted.Order.OrderID, result.Order.OrderID)
	require.Empty(t, result.Order.State, "an unreadable current order must not be represented by stale OPEN state")
	require.False(t, submissionAccountError(err))
}

func TestFlattenSharedSyncFailureIsNotHealthy(t *testing.T) {
	f, s, _ := realSubmissionService(t)
	s.Syncer = databaseSubmissionSyncer{f.store}
	failure := errors.New("shared sync database failure")
	callback := "test:flatten_sync"
	require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register(callback, func(db *gorm.DB) {
		if strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "UPDATE t_trading_accounts") {
			db.AddError(failure)
		}
	}))
	defer f.store.DBForTest().Callback().Raw().Remove(callback)
	result, err := s.FlattenLogicalAccount(context.Background(), FlattenCommand{SpaceID: "space-1", LogicalAccountID: "logical-1", ActionID: "flatten-1", Reason: "stop"})
	require.ErrorIs(t, err, failure)
	require.Equal(t, "RUNNING", result.Action.Status)
}

func TestFlattenCanceledLockPersistsRetryAndKeepsIdentity(t *testing.T) {
	f, s, _ := realSubmissionService(t)
	action, _, err := f.store.CreateOperatorAction(context.Background(), store.OperatorActionRecord{SpaceID: "space-1", ActionID: "flatten-lock", LogicalAccountID: "logical-1", ActionType: "FLATTEN", RequestJSON: `{"logical_account_id":"logical-1"}`, Reason: "stop", Status: "RUNNING"})
	require.NoError(t, err)
	unlock := f.store.LockLogicalAccount("space-1", "logical-1")
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result, err := s.FlattenLogicalAccount(ctx, FlattenCommand{SpaceID: "space-1", ActionID: action.ActionID, LogicalAccountID: "logical-1", Reason: "stop"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, action.ActionID, result.Action.ActionID)
	require.Contains(t, result.Action.LastError, "deadline")
}

func TestCancelDiagnosticAndCompletionWriteFailureKeepDurableAction(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		t.Run(map[bool]string{true: "completion", false: "diagnostic"}[terminal], func(t *testing.T) {
			f, s, a := realSubmissionService(t)
			accepted, err := s.SubmitOrder(context.Background(), submissionCommand(f))
			require.NoError(t, err)
			if terminal {
				_, err = s.Orders.DiscardPending(context.Background(), "space-1", accepted.Order.OrderID)
				require.NoError(t, err)
			} else {
				require.NoError(t, s.ResumeOperatorAction(context.Background(), accepted.Action))
				orders := s.Orders.(*orderapp.Service)
				orders.Adapters = cancelDeadlineAdapter{a}
				orders.Syncer = databaseSubmissionSyncer{f.store}
			}
			action, _, err := f.store.CreateOperatorAction(context.Background(), store.OperatorActionRecord{SpaceID: "space-1", ActionID: "cancel-write", LogicalAccountID: "logical-1", ActionType: "CANCEL_ORDER", RequestJSON: `{"order_id":"` + accepted.Order.OrderID + `"}`, Reason: "stop", Status: "RUNNING"})
			require.NoError(t, err)
			failure := errors.New("action update unavailable")
			callback := "test:cancel_action_write"
			require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register(callback, func(db *gorm.DB) {
				if strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "UPDATE t_operator_actions") {
					db.AddError(failure)
				}
			}))
			defer f.store.DBForTest().Callback().Raw().Remove(callback)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			result, err := s.CancelOrder(ctx, CancelOrderCommand{SpaceID: "space-1", ActionID: action.ActionID, OrderID: accepted.Order.OrderID, Reason: "stop"})
			require.ErrorIs(t, err, failure)
			require.Equal(t, action.Status, result.Action.Status)
			require.Equal(t, action.ResultJSON, result.Action.ResultJSON)
			require.Equal(t, action.LastError, result.Action.LastError)
		})
	}
}

func TestFlattenRecoversUnknownChildBeforeQuoteAndNeverCompletesUncertain(t *testing.T) {
	f, s, a := realSubmissionService(t)
	accepted, err := s.SubmitOrder(context.Background(), submissionCommand(f))
	require.NoError(t, err)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_operator_actions SET c_action_type='FLATTEN',c_request_json=? WHERE c_action_id=?", `{"logical_account_id":"logical-1"}`, accepted.Action.ActionID).Error)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_orders SET c_state='SUBMIT_UNKNOWN',c_submitted_at=? WHERE c_order_id=?", f.now.UnixMilli(), accepted.Order.OrderID).Error)
	action, err := f.store.GetOperatorAction(context.Background(), "space-1", accepted.Action.ActionID)
	require.NoError(t, err)
	s.Prices = deadlinePrice{}
	a.lookupErr = errors.New("lookup temporarily unavailable")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.NoError(t, s.ResumeOperatorAction(ctx, action))
	require.Equal(t, 1, a.queries)
	require.Zero(t, a.posts)
	persisted, err := f.store.GetOperatorAction(context.Background(), "space-1", action.ActionID)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", persisted.Status)
}

func TestManualRecoveryBusinessRejectionAndUnknownQuery(t *testing.T) {
	for _, path := range []string{"business", "unknown"} {
		t.Run(path, func(t *testing.T) {
			f, s, a := realSubmissionService(t)
			command := submissionCommand(f)
			accepted, err := s.SubmitOrder(context.Background(), command)
			require.NoError(t, err)
			raw, err := manualOrderRequestJSON(command.ManualOrderCommand)
			require.NoError(t, err)
			require.NoError(t, f.store.DBForTest().Exec("UPDATE t_operator_actions SET c_action_type='MANUAL_ORDER',c_request_json=? WHERE c_action_id=?", raw, accepted.Action.ActionID).Error)
			action, err := f.store.GetOperatorAction(context.Background(), "space-1", accepted.Action.ActionID)
			require.NoError(t, err)
			want := "FAILED"
			if path == "unknown" {
				want = "RUNNING"
				require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_orders SET c_state='SUBMIT_UNKNOWN',c_submitted_at=? WHERE c_order_id=?", f.now.UnixMilli(), accepted.Order.OrderID).Error)
				a.lookupErr = errors.New("temporary lookup failure")
			} else {
				s.Orders.(*orderapp.Service).Validator.MaxChildNotional = shared.MustDecimal("1")
			}
			require.NoError(t, s.ResumeOperatorAction(context.Background(), action))
			persisted, err := f.store.GetOperatorAction(context.Background(), "space-1", action.ActionID)
			require.NoError(t, err)
			require.Equal(t, want, persisted.Status)
			require.NotEmpty(t, persisted.LastError)
			require.Zero(t, a.posts)
			if path == "unknown" {
				require.Equal(t, 1, a.queries)
			}
		})
	}
}

func TestManualAndCancelLockWaitsAreCancelableAndKeepDurableIDs(t *testing.T) {
	for _, kind := range []string{"MANUAL_ORDER", "CANCEL_ORDER"} {
		t.Run(kind, func(t *testing.T) {
			f, s, _ := realSubmissionService(t)
			command := submissionCommand(f)
			accepted, err := s.SubmitOrder(context.Background(), command)
			require.NoError(t, err)
			var raw string
			if kind == "MANUAL_ORDER" {
				raw, err = manualOrderRequestJSON(command.ManualOrderCommand)
				require.NoError(t, err)
			} else {
				raw = `{"order_id":"` + accepted.Order.OrderID + `"}`
			}
			require.NoError(t, f.store.DBForTest().Exec("UPDATE t_operator_actions SET c_action_type=?,c_request_json=? WHERE c_action_id=?", kind, raw, accepted.Action.ActionID).Error)
			unlock := f.store.LockLogicalAccount("space-1", "logical-1")
			defer unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			var action store.OperatorActionRecord
			var orderID string
			if kind == "MANUAL_ORDER" {
				got, callErr := s.PlaceManualOrder(ctx, command.ManualOrderCommand)
				err = callErr
				action = got.Action
				orderID = got.Order.OrderID
			} else {
				got, callErr := s.CancelOrder(ctx, CancelOrderCommand{SpaceID: "space-1", ActionID: command.ActionID, OrderID: accepted.Order.OrderID, Reason: command.Reason})
				err = callErr
				action = got.Action
				orderID = got.Order.OrderID
			}
			if kind == "MANUAL_ORDER" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			require.Equal(t, command.ActionID, action.ActionID)
			require.Equal(t, accepted.Order.OrderID, orderID)
			require.Contains(t, action.LastError, "deadline")
		})
	}
}

func TestSubmitWorkerLockTimeoutContinuesNextActionWithHealthyDiagnostics(t *testing.T) {
	for _, failDiagnostic := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "shared"}[failDiagnostic], func(t *testing.T) {
			f, s, _ := realSubmissionService(t)
			command := submissionCommand(f)
			command.ActionID = "a-action"
			first, err := s.SubmitOrder(context.Background(), command)
			require.NoError(t, err)
			command.ActionID = "z-action"
			command.ClientOrderID = "z-client"
			second, err := s.SubmitOrder(context.Background(), command)
			require.NoError(t, err)
			failure := errors.New("diagnostic write unavailable")
			if failDiagnostic {
				callback := "test:submit_lock_diagnostic"
				require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register(callback, func(db *gorm.DB) {
					if strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "UPDATE t_operator_actions") && strings.Contains(db.Statement.SQL.String(), "c_action_id") {
						for _, v := range db.Statement.Vars {
							if v == first.Action.ActionID {
								db.AddError(failure)
								break
							}
						}
					}
				}))
				defer f.store.DBForTest().Callback().Raw().Remove(callback)
			}
			unlock := f.store.LockLogicalAccount("space-1", "logical-1")
			locked := true
			defer func() {
				if locked {
					unlock()
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			worker := &traderuntime.OperatorWorker{Actions: f.store, ActionTimeout: 30 * time.Millisecond, Resumer: submissionResumerFunc(func(ctx context.Context, action store.OperatorActionRecord) error {
				err := s.ResumeOperatorAction(ctx, action)
				if action.ActionID == first.Action.ActionID {
					unlock()
					locked = false
				}
				if action.ActionID == second.Action.ActionID {
					cancel()
				}
				return err
			})}
			require.ErrorIs(t, worker.Run(ctx), context.Canceled)
			require.Equal(t, !failDiagnostic, worker.Snapshot().Ready, worker.Snapshot().LastError)
			if failDiagnostic {
				require.Contains(t, worker.Snapshot().LastError, failure.Error())
			} else {
				current, err := f.store.GetOperatorAction(context.Background(), "space-1", first.Action.ActionID)
				require.NoError(t, err)
				require.Equal(t, "RUNNING", current.Status)
				require.Contains(t, current.LastError, "deadline")
			}
			current, err := f.store.GetOperatorAction(context.Background(), "space-1", second.Action.ActionID)
			require.NoError(t, err)
			require.Equal(t, "COMPLETED", current.Status)
		})
	}
}
