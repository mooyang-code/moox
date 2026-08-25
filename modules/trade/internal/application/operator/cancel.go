package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
) (CancelOrderResult, error) {
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
	unlock := s.Store.LockLogicalAccount(
		command.SpaceID, orderRecord.LogicalAccountID,
	)
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
	if action.Status != "RUNNING" {
		return s.loadCancelOrderResult(ctx, action)
	}
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
	orderRecord, err = s.Store.GetOrder(ctx, command.SpaceID, command.OrderID)
	if err != nil {
		return s.failCancelAction(ctx, action, orderRecord, err)
	}
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
	action.Status = "COMPLETED"
	action.ResultJSON = &raw
	action.LastError = ""
	if err := s.updateAction(ctx, action); err != nil {
		return CancelOrderResult{}, err
	}
	action, err = s.Store.GetOperatorAction(ctx, action.SpaceID, action.ActionID)
	return CancelOrderResult{Action: action, Order: orderRecord}, err
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
	action.Status = "FAILED"
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
		return CancelOrderResult{}, errors.Join(cause, err)
	}
	current, _ := s.Store.GetOperatorAction(ctx, action.SpaceID, action.ActionID)
	return CancelOrderResult{Action: current, Order: orderRecord}, cause
}
