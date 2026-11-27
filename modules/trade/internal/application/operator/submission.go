package operator

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

func submissionRecoveryError(result ManualOrderResult, err error) error {
	if allSubmissionErrors(err, func(leaf error) bool {
		switch leaf.(type) {
		case manualFailureError:
			return result.Action.Status == string(operatordomain.StatusFailed)
		case submissionDeferredError:
			return result.Action.Status == string(operatordomain.StatusRunning)
		}
		return false
	}) {
		return nil
	}
	return err
}

type submissionDeferredError struct{ cause error }

func (e submissionDeferredError) Error() string { return e.cause.Error() }
func (e submissionDeferredError) Unwrap() error { return e.cause }

func (s *Service) deferSubmission(ctx context.Context, action store.OperatorActionRecord, cause error) (ManualOrderResult, error) {
	result, err := s.submissionDiagnostic(ctx, action, cause)
	if err != nil {
		return result, errors.Join(cause, err)
	}
	return result, submissionDeferredError{cause: cause}
}

func submissionAccountError(err error) bool {
	return allSubmissionErrors(err, func(leaf error) bool {
		_, account := leaf.(*orderapp.AccountExecutionError)
		return account || leaf == ErrCancelUnconfirmed || leaf == orderdomain.ErrReferencePriceStale || leaf == orderapp.ErrAccountNotReady || leaf == tradingaccount.ErrAccountNotExecutable
	})
}

// ActionScopedError 报告动作恢复返回的错误是否只关乎这个动作：每一支都是账户、交易所侧的错误、业务校验错误或清仓终结原因。
// 工作线程（runtime.OperatorWorker.ActionScoped）据此只把它记在这个动作上，不让一个动作反复失败拖垮整个 Trade 的就绪；
// 掺着数据库等共享故障的照样算共享故障。
//
// 另外两类也只关乎这个动作：成员在发起之后被移出组合（ErrNotInLogicalAccount，动作按失败结束）；store.ErrConflict
// （订单版本冲突、成交单号冲突、会话 CAS 落空等比较后写入落空，多半是私有流或别的动作并发改了同一行，下次恢复重读即可）。
// ErrConflict 不算业务错误（submissionBusinessError），清仓照样重试。store 不会把 SQLite 忙、锁等待映射成 ErrConflict。
func ActionScopedError(err error) bool {
	return err != nil && allSubmissionErrors(err, func(leaf error) bool {
		return submissionAccountError(leaf) || submissionBusinessError(leaf) || isFlattenFinal(leaf) ||
			leaf == ErrNotInLogicalAccount || leaf == store.ErrConflict
	})
}

// A typed account boundary is atomic; joins outside that boundary must have
// every branch classified, so a sync database failure cannot be hidden.
func allSubmissionErrors(err error, match func(error) bool) bool {
	if err == nil {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if !allSubmissionErrors(child, match) {
				return false
			}
		}
		return true
	}
	if match(err) {
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		child := wrapped.Unwrap()
		return child != nil && allSubmissionErrors(child, match)
	}
	return false
}

func submissionBusinessError(err error) bool {
	return allSubmissionErrors(err, func(leaf error) bool {
		for _, entry := range manualFailureCodes {
			if leaf == entry.cause && leaf != store.ErrConflict {
				return true
			}
		}
		return false
	})
}

func (s *Service) submissionOrderError(ctx context.Context, action store.OperatorActionRecord, cause error) (ManualOrderResult, error) {
	if allSubmissionErrors(cause, func(err error) bool { return submissionAccountError(err) || submissionBusinessError(err) }) {
		return s.deferSubmission(ctx, action, cause)
	}
	// A durable diagnostic must not turn a shared persistence failure into a
	// successful recovery attempt. Preserve the actual order state for retry.
	result, saveErr := s.submissionDiagnostic(ctx, action, cause)
	if saveErr != nil {
		return result, errors.Join(cause, saveErr)
	}
	return result, cause
}

func submissionIdentity(action store.OperatorActionRecord) ManualOrderResult {
	result := ManualOrderResult{Action: action}
	var progress manualOrderActionResult
	if action.ResultJSON != nil {
		_ = json.Unmarshal([]byte(*action.ResultJSON), &progress)
	}
	result.Order = store.OrderRecord{SpaceID: action.SpaceID, OrderID: progress.OrderID, LogicalAccountID: action.LogicalAccountID}
	var request manualOrderRequest
	if json.Unmarshal([]byte(action.RequestJSON), &request) == nil {
		result.Order.TradingAccountID = request.TradingAccountID
		result.Order.ClientOrderID = request.ClientOrderID
	}
	return result
}

func (s *Service) submissionDiagnostic(ctx context.Context, action store.OperatorActionRecord, cause error, accounts ...OperatorAccountError) (ManualOrderResult, error) {
	// This detached budget only persists/reads diagnostics, never authorizes I/O
	// to an exchange or starts a fresh child order.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result, err := s.deferManualAction(persistCtx, action, cause, accounts)
	if err != nil {
		fallback := submissionIdentity(action)
		if result.Action.ActionID == "" {
			result.Action = fallback.Action
		}
		if result.Order.OrderID == "" {
			result.Order = fallback.Order
		}
	}
	return result, err
}

// Lock waiters may only append a diagnostic to the current action. They must
// not overwrite progress or reopen an action completed by the lock owner.
func (s *Service) deferActionLock(ctx context.Context, action store.OperatorActionRecord, cause error) (store.OperatorActionRecord, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		current, err := tx.GetOperatorAction(action.SpaceID, action.ActionID)
		if err != nil {
			return err
		}
		action = current
		if current.Status != string(operatordomain.StatusRunning) {
			return nil
		}
		current.LastError = cause.Error()
		current.UpdatedAt = s.clock().UTC()
		if err := tx.UpdateOperatorAction(current); err != nil {
			return err
		}
		action = current
		return nil
	})
	if err != nil {
		return action, errors.Join(cause, err)
	}
	if action.Status != string(operatordomain.StatusRunning) {
		return action, nil
	}
	return action, submissionDeferredError{cause: cause}
}
