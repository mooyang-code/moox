package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

type SubmitOrderCommand struct {
	ManualOrderCommand
	LogicalAccountID string
}

type submissionRequest struct {
	manualOrderRequest
	LogicalAccountID string `json:"logical_account_id"`
}

// SubmitOrder only accepts and reserves locally. The worker owns exchange I/O.
func (s *Service) SubmitOrder(ctx context.Context, command SubmitOrderCommand) (result ManualOrderResult, retErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var knownAction store.OperatorActionRecord
	var knownOrderID string
	defer func() {
		if retErr == nil || knownAction.ActionID == "" {
			return
		}
		fallback := submissionIdentity(knownAction)
		if result.Action.ActionID == "" {
			result.Action = fallback.Action
		}
		if result.Order.OrderID == "" {
			result.Order = fallback.Order
			if knownOrderID != "" {
				result.Order.OrderID = knownOrderID
			}
		}
	}()
	if s == nil || s.Store == nil || s.Orders == nil || s.Prices == nil {
		return ManualOrderResult{}, ErrServiceConfig
	}
	if blank(command.LogicalAccountID) || command.DeadlineAt < 0 {
		return ManualOrderResult{}, ErrInvalidCommand
	}
	raw, err := manualOrderRequestJSON(command.ManualOrderCommand)
	if err != nil {
		return ManualOrderResult{}, err
	}
	request := submissionRequest{LogicalAccountID: command.LogicalAccountID}
	if err := json.Unmarshal([]byte(raw), &request.manualOrderRequest); err != nil {
		return ManualOrderResult{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return ManualOrderResult{}, err
	}
	expected := store.OperatorActionRecord{SpaceID: command.SpaceID, ActionID: command.ActionID, LogicalAccountID: command.LogicalAccountID, ActionType: "SUBMIT_ORDER", Reason: strings.TrimSpace(command.Reason), RequestJSON: string(encoded), Status: "RUNNING"}
	if existing, found, err := s.existingAction(ctx, expected); err != nil {
		return ManualOrderResult{}, err
	} else if found {
		knownAction = existing
		if existing.Status != "RUNNING" {
			return s.loadManualOrderResult(ctx, existing)
		}
	}
	unlock, err := s.Store.LockLogicalAccountContext(ctx, command.SpaceID, command.LogicalAccountID)
	if err != nil {
		return ManualOrderResult{}, err
	}
	defer unlock()
	action, found, err := s.existingAction(ctx, expected)
	if err != nil {
		return ManualOrderResult{}, err
	}
	if found {
		knownAction = action
		if result, handled, err := s.recoverChild(ctx, action, command.ManualOrderCommand, false); handled || err != nil {
			return result, err
		}
	}
	logical, member, err := s.Store.FindLogicalAccountByTradingAccount(ctx, command.SpaceID, command.TradingAccountID)
	if err == nil && (logical.LogicalAccountID != command.LogicalAccountID || logical.ControlMode != "MANUAL" || !member.Enabled) {
		err = fmt.Errorf("%w: ordinary submission requires an enabled MANUAL logical account member", ErrInvalidCommand)
	}
	if err != nil {
		if found && (errors.Is(err, ErrInvalidCommand) || errors.Is(err, gorm.ErrRecordNotFound)) {
			return s.failManualAction(ctx, action, err, nil)
		}
		return ManualOrderResult{}, err
	}
	if !found {
		account, accountErr := s.Store.GetTradingAccount(ctx, command.SpaceID, command.TradingAccountID)
		if accountErr != nil {
			return ManualOrderResult{}, accountErr
		}
		if account.Status != "ENABLED" {
			return ManualOrderResult{}, fmt.Errorf("%w: %w", ErrInvalidCommand, tradingaccount.ErrAccountNotExecutable)
		}
		expected.CreatedAt = time.Now().UTC()
		if s.Now != nil {
			expected.CreatedAt = s.Now().UTC()
		}
		deadline := command.DeadlineAt
		if deadline == 0 {
			deadline = s.manualDeadlineFrom(expected.CreatedAt)
		}
		progress, _ := json.Marshal(manualOrderActionResult{DeadlineAt: deadline})
		value := string(progress)
		expected.ResultJSON = &value
		action, _, err = s.Store.CreateOperatorAction(ctx, expected)
		if err != nil {
			return ManualOrderResult{}, err
		}
		knownAction = action
	}
	if result, handled, err := s.recoverChild(ctx, action, command.ManualOrderCommand, false); handled || err != nil {
		return result, err
	}
	instrument, err := s.Store.GetInstrumentByIDForAccount(ctx, command.SpaceID, command.TradingAccountID, command.InstrumentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return s.failManualAction(ctx, action, fmt.Errorf("%w: instrument does not exist", ErrInvalidCommand), nil)
		}
		return ManualOrderResult{Action: action}, err
	}
	quote, err := s.Prices.LatestPrice(ctx, command.TradingAccountID, instrument.ExchangeSymbol)
	if err != nil {
		return s.deferSubmission(ctx, action, err)
	}
	spec := orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{TradingAccountID: command.TradingAccountID, ClientOrderID: command.ClientOrderID, InstrumentID: command.InstrumentID, Type: command.Type, FillPolicy: command.FillPolicy, Side: command.Side, PositionSide: command.PositionSide, Quantity: command.Quantity, LimitPrice: command.LimitPrice},
		ReferencePrice:  quote.Price, ReferencePriceAt: quote.UpdatedAt,
		Owner: orderdomain.OrderOwner{Type: orderdomain.OwnerOperator, OwnerID: command.ActionID, LogicalAccountID: command.LogicalAccountID},
	}
	placed, err := s.Orders.Place(ctx, command.SpaceID, spec)
	if err != nil {
		if submissionBusinessError(err) && manualErrorCode(err) != "" {
			return s.failManualAction(ctx, action, err, nil)
		}
		return s.submissionOrderError(ctx, action, err)
	}
	knownOrderID = string(placed.ID)
	progress, err := s.manualProgress(ctx, &action)
	if err != nil {
		return ManualOrderResult{}, err
	}
	progress.OrderID = string(placed.ID)
	previousAction := action
	if err := s.saveManualProgress(ctx, &action, progress); err != nil {
		result := submissionIdentity(previousAction)
		result.Order.OrderID = string(placed.ID)
		return result, err
	}
	return s.loadManualOrderResult(ctx, action)
}

func (s *Service) resumeSubmission(ctx context.Context, action store.OperatorActionRecord) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command, err := manualOrderCommand(action)
	if err != nil {
		return err
	}
	var request submissionRequest
	if err := json.Unmarshal([]byte(action.RequestJSON), &request); err != nil {
		return err
	}
	if request.LogicalAccountID != action.LogicalAccountID {
		return ErrInvalidCommand
	}
	result, err := s.SubmitOrder(ctx, SubmitOrderCommand{ManualOrderCommand: command, LogicalAccountID: request.LogicalAccountID})
	if err != nil {
		return submissionRecoveryError(result, err)
	}
	if result.Action.Status != "RUNNING" || result.Order.OrderID == "" {
		return nil
	}
	unlock, err := s.Store.LockLogicalAccountContext(ctx, action.SpaceID, action.LogicalAccountID)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := s.Store.GetOperatorAction(ctx, action.SpaceID, action.ActionID)
	if err != nil {
		return err
	}
	if current.Status != "RUNNING" {
		return nil
	}
	progress, err := s.manualProgress(ctx, &current)
	if err != nil {
		return err
	}
	result, err = s.advanceManualChild(ctx, current, progress)
	return submissionRecoveryError(result, err)
}

func submissionRecoveryError(result ManualOrderResult, err error) error {
	if allSubmissionErrors(err, func(leaf error) bool {
		switch leaf.(type) {
		case manualFailureError:
			return result.Action.Status == "FAILED"
		case submissionDeferredError:
			return result.Action.Status == "RUNNING"
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
		return account || leaf == orderdomain.ErrReferencePriceStale || leaf == orderapp.ErrAccountNotReady || leaf == tradingaccount.ErrAccountNotExecutable
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
	if submissionAccountError(cause) {
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

func (s *Service) submissionDiagnostic(ctx context.Context, action store.OperatorActionRecord, cause error) (ManualOrderResult, error) {
	// This detached budget only persists/reads diagnostics, never authorizes I/O
	// to an exchange or starts a fresh child order.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result, err := s.deferManualAction(persistCtx, action, cause, nil)
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

func (s *Service) deferChildOrderError(ctx context.Context, action store.OperatorActionRecord, cause error) (ManualOrderResult, error) {
	if action.ActionType == "SUBMIT_ORDER" {
		return s.submissionOrderError(ctx, action, cause)
	}
	return s.deferManualAction(ctx, action, cause, nil)
}
