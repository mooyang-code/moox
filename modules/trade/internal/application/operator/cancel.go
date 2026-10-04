package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

type CancelOrderCommand struct {
	SpaceID  string
	ActionID string
	OrderID  string
	Reason   string
}

type CancelOrderResult struct {
	Action store.OperatorActionRecord
	Order  store.OrderRecord
}

type cancelOrderRequest struct {
	OrderID string `json:"order_id"`
}

type cancelOrderActionResult struct {
	OrderID string `json:"order_id"`
}

func (s *Service) CancelOrder(
	ctx context.Context,
	command CancelOrderCommand,
) (result CancelOrderResult, retErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var knownAction store.OperatorActionRecord
	defer func() {
		if retErr == nil || knownAction.ActionID == "" {
			return
		}
		if result.Action.ActionID == "" {
			result.Action = knownAction
		}
		if result.Order.OrderID == "" {
			result.Order = store.OrderRecord{SpaceID: command.SpaceID, OrderID: command.OrderID, LogicalAccountID: knownAction.LogicalAccountID}
		}
	}()
	if err := s.validate(); err != nil {
		return CancelOrderResult{}, err
	}
	requestJSON, err := cancelOrderRequestJSON(command)
	if err != nil {
		return CancelOrderResult{}, err
	}
	existing, existingErr := s.Store.GetOperatorAction(
		ctx, command.SpaceID, command.ActionID,
	)
	if existingErr == nil {
		current, _, ensureErr := s.Store.CreateOperatorAction(
			ctx,
			store.OperatorActionRecord{
				SpaceID: command.SpaceID, ActionID: command.ActionID,
				LogicalAccountID: existing.LogicalAccountID,
				ActionType:       "CANCEL_ORDER", Reason: strings.TrimSpace(command.Reason),
				RequestJSON: requestJSON, Status: "RUNNING",
			},
		)
		if ensureErr != nil {
			return CancelOrderResult{}, ensureErr
		}
		knownAction = current
		if current.Status != "RUNNING" {
			return s.loadCancelOrderResult(ctx, current)
		}
	} else if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
		return CancelOrderResult{}, existingErr
	}

	orderRecord, err := s.Store.GetOrder(ctx, command.SpaceID, command.OrderID)
	if err != nil {
		return CancelOrderResult{}, err
	}
	if blank(orderRecord.LogicalAccountID) {
		return CancelOrderResult{}, fmt.Errorf(
			"%w: order does not belong to a logical account",
			ErrInvalidCommand,
		)
	}
	if existingErr == nil &&
		existing.LogicalAccountID != orderRecord.LogicalAccountID {
		return CancelOrderResult{}, store.ErrConflict
	}
	unlock, err := s.Store.LockLogicalAccountContext(ctx, command.SpaceID, orderRecord.LogicalAccountID)
	if err != nil {
		if existing.ActionID != "" {
			current, diagnosticErr := s.deferActionLock(ctx, existing, err)
			return CancelOrderResult{Action: current, Order: orderRecord}, diagnosticErr
		}
		return CancelOrderResult{Order: orderRecord}, err
	}
	defer unlock()
	var action store.OperatorActionRecord
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation(
			command.SpaceID,
			orderRecord.LogicalAccountID,
			"PAUSED",
			strings.TrimSpace(command.Reason),
		); err != nil {
			return err
		}
		var ensureErr error
		action, _, ensureErr = tx.EnsureOperatorAction(
			store.OperatorActionRecord{
				SpaceID: command.SpaceID, ActionID: command.ActionID,
				LogicalAccountID: orderRecord.LogicalAccountID,
				ActionType:       "CANCEL_ORDER", Reason: strings.TrimSpace(command.Reason),
				RequestJSON: requestJSON, Status: "RUNNING",
			},
		)
		return ensureErr
	})
	if err != nil {
		return CancelOrderResult{}, err
	}
	knownAction = action
	if action.Status != "RUNNING" {
		return s.loadCancelOrderResult(ctx, action)
	}
	current, err := s.Store.GetOrder(ctx, command.SpaceID, command.OrderID)
	if err != nil {
		return s.failCancelAction(ctx, action, orderRecord, err)
	}
	orderRecord = current
	if !orderdomain.State(orderRecord.State).Terminal() {
		if err := s.stopOrder(ctx, orderRecord); err != nil {
			return s.failCancelAction(ctx, action, orderRecord, err)
		}
		if err := s.Syncer.SyncAccount(
			ctx, orderRecord.TradingAccountID,
		); err != nil {
			return s.failCancelAction(ctx, action, orderRecord, err)
		}
	}
	current, err = s.Store.GetOrder(ctx, command.SpaceID, command.OrderID)
	if err != nil {
		return s.failCancelAction(ctx, action, orderRecord, err)
	}
	orderRecord = current
	if !orderdomain.State(orderRecord.State).Terminal() {
		err = fmt.Errorf(
			"%w: order %s remains %s",
			ErrCancelUnconfirmed,
			orderRecord.OrderID,
			orderRecord.State,
		)
		return s.failCancelAction(ctx, action, orderRecord, err)
	}
	resultData, err := json.Marshal(cancelOrderActionResult{
		OrderID: orderRecord.OrderID,
	})
	if err != nil {
		return s.failCancelAction(ctx, action, orderRecord, err)
	}
	raw := string(resultData)
	previousAction := action
	action.Status = "COMPLETED"
	action.ResultJSON = &raw
	action.LastError = ""
	if err := s.updateAction(ctx, action); err != nil {
		return CancelOrderResult{Action: previousAction, Order: orderRecord}, err
	}
	return CancelOrderResult{Action: action, Order: orderRecord}, nil
}

func cancelOrderRequestJSON(command CancelOrderCommand) (string, error) {
	if blank(command.SpaceID) || blank(command.ActionID) ||
		blank(command.OrderID) || blank(command.Reason) {
		return "", ErrInvalidCommand
	}
	data, err := json.Marshal(cancelOrderRequest{OrderID: command.OrderID})
	return string(data), err
}

func (s *Service) loadCancelOrderResult(
	ctx context.Context,
	action store.OperatorActionRecord,
) (CancelOrderResult, error) {
	if action.ResultJSON == nil {
		if action.LastError == "" {
			return CancelOrderResult{Action: action}, nil
		}
		return CancelOrderResult{Action: action}, errors.New(action.LastError)
	}
	var result cancelOrderActionResult
	if err := json.Unmarshal([]byte(*action.ResultJSON), &result); err != nil {
		return CancelOrderResult{Action: action}, err
	}
	current, err := s.Store.GetOrder(ctx, action.SpaceID, result.OrderID)
	if err == nil && action.LastError != "" {
		err = errors.New(action.LastError)
	}
	return CancelOrderResult{Action: action, Order: current}, err
}

func (s *Service) failCancelAction(
	ctx context.Context,
	action store.OperatorActionRecord,
	orderRecord store.OrderRecord,
	cause error,
) (CancelOrderResult, error) {
	previousAction := action
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	current, readErr := s.Store.GetOrder(ctx, action.SpaceID, orderRecord.OrderID)
	if readErr != nil {
		cause = errors.Join(cause, readErr)
		orderRecord = store.OrderRecord{
			SpaceID: orderRecord.SpaceID, OrderID: orderRecord.OrderID,
			TradingAccountID: orderRecord.TradingAccountID, LogicalAccountID: orderRecord.LogicalAccountID,
			ClientOrderID: orderRecord.ClientOrderID,
		}
	} else {
		orderRecord = current
	}
	action.Status = "RUNNING"
	action.LastError = cause.Error()
	data, marshalErr := json.Marshal(map[string]string{
		"order_id": orderRecord.OrderID,
		"error":    cause.Error(),
	})
	if marshalErr == nil {
		raw := string(data)
		action.ResultJSON = &raw
	}
	if err := s.updateAction(ctx, action); err != nil {
		return CancelOrderResult{Action: previousAction, Order: orderRecord}, errors.Join(cause, err)
	}
	if submissionAccountError(cause) {
		cause = submissionDeferredError{cause: cause}
	}
	return CancelOrderResult{Action: action, Order: orderRecord}, cause
}
