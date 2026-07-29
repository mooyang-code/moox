package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

var (
	ErrServiceConfig     = errors.New("trade operator: service is not configured")
	ErrInvalidCommand    = errors.New("trade operator: invalid command")
	ErrCancelUnconfirmed = errors.New("trade operator: cancellation is not confirmed")
)

type Quote = targetapp.Quote

type PriceSource interface {
	LatestPrice(context.Context, string, string) (Quote, error)
}

type AccountSyncer interface {
	SyncAccount(context.Context, string) error
}

type OrderService interface {
	Place(context.Context, string, orderdomain.OrderSpec) (orderdomain.Order, error)
	Submit(context.Context, string, string) (orderdomain.Order, error)
	Cancel(context.Context, string, string) (orderdomain.Order, error)
	DiscardPending(context.Context, string, string) (orderdomain.Order, error)
	ResolveUnknown(context.Context, string, string) (orderdomain.Order, error)
	RecoverCancel(context.Context, string, string) (orderdomain.Order, error)
}

type Service struct {
	Store  *store.Store
	Orders OrderService
	Syncer AccountSyncer
	Prices PriceSource
	Now    func() time.Time
}

type ManualOrderCommand struct {
	SpaceID           string
	ActionID          string
	ExchangeAccountID string
	ClientOrderID     string
	InstrumentID      string
	Type              exchange.OrderType
	FillPolicy        exchange.FillPolicy
	Side              exchange.Side
	PositionSide      exchange.PositionSide
	Quantity          shared.Decimal
	LimitPrice        *shared.Decimal
	Reason            string
}

type ManualOrderResult struct {
	Action store.OperatorActionRecord
	Order  store.OrderRecord
}

type manualOrderRequest struct {
	ExchangeAccountID string  `json:"exchange_account_id"`
	ClientOrderID     string  `json:"client_order_id"`
	InstrumentID      string  `json:"instrument_id"`
	OrderType         string  `json:"order_type"`
	FillPolicy        string  `json:"fill_policy,omitempty"`
	Side              string  `json:"side"`
	PositionSide      string  `json:"position_side,omitempty"`
	Quantity          string  `json:"quantity"`
	LimitPrice        *string `json:"limit_price,omitempty"`
}

type manualOrderActionResult struct {
	OrderID string `json:"order_id"`
}

func (s *Service) PlaceManualOrder(
	ctx context.Context,
	command ManualOrderCommand,
) (ManualOrderResult, error) {
	if err := s.validate(); err != nil {
		return ManualOrderResult{}, err
	}
	requestJSON, err := manualOrderRequestJSON(command)
	if err != nil {
		return ManualOrderResult{}, err
	}
	logicalAccount, _, err := s.Store.FindLogicalAccountByExchangeAccount(
		ctx, command.SpaceID, command.ExchangeAccountID,
	)
	if err != nil {
		return ManualOrderResult{}, err
	}
	unlock := s.Store.LockLogicalAccount(
		command.SpaceID, logicalAccount.LogicalAccountID,
	)
	defer unlock()

	var action store.OperatorActionRecord
	var created bool
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation(
			command.SpaceID,
			logicalAccount.LogicalAccountID,
			"PAUSED",
			strings.TrimSpace(command.Reason),
		); err != nil {
			return err
		}
		var ensureErr error
		action, created, ensureErr = tx.EnsureOperatorAction(
			store.OperatorActionRecord{
				SpaceID: command.SpaceID, ActionID: command.ActionID,
				LogicalAccountID: logicalAccount.LogicalAccountID,
				ActionType:       "MANUAL_ORDER", Reason: strings.TrimSpace(command.Reason),
				RequestJSON: requestJSON, Status: "RUNNING",
			},
		)
		return ensureErr
	})
	if err != nil {
		return ManualOrderResult{}, err
	}
	if !created && action.Status != "RUNNING" {
		return s.loadManualOrderResult(ctx, action)
	}

	if err := s.cancelLogicalAccountOrders(
		ctx,
		command.SpaceID,
		logicalAccount.LogicalAccountID,
		command.ActionID,
		true,
	); err != nil {
		return s.failManualAction(ctx, action, err)
	}
	quote, err := s.Prices.LatestPrice(
		ctx, command.ExchangeAccountID, command.InstrumentID,
	)
	if err != nil {
		return s.failManualAction(ctx, action, err)
	}
	spec := orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			ExchangeAccountID: command.ExchangeAccountID,
			ClientOrderID:     command.ClientOrderID, InstrumentID: command.InstrumentID,
			Type: command.Type, FillPolicy: command.FillPolicy,
			Side: command.Side, PositionSide: command.PositionSide,
			Quantity: command.Quantity, LimitPrice: command.LimitPrice,
		},
		ReferencePrice: quote.Price, ReferencePriceAt: quote.UpdatedAt,
		Owner: orderdomain.OrderOwner{
			Type: orderdomain.OwnerOperator, OwnerID: command.ActionID,
			LogicalAccountID: logicalAccount.LogicalAccountID,
		},
	}
	placed, err := s.Orders.Place(ctx, command.SpaceID, spec)
	if err == nil {
		_, err = s.Orders.Submit(ctx, command.SpaceID, string(placed.ID))
	}
	if err != nil {
		return s.failManualAction(ctx, action, err)
	}
	orderRecord, err := s.Store.GetOrder(ctx, command.SpaceID, string(placed.ID))
	if err != nil {
		return s.failManualAction(ctx, action, err)
	}
	resultJSON, err := json.Marshal(manualOrderActionResult{OrderID: orderRecord.OrderID})
	if err != nil {
		return s.failManualAction(ctx, action, err)
	}
	resultRaw := string(resultJSON)
	action.Status = "COMPLETED"
	action.ResultJSON = &resultRaw
	action.LastError = ""
	if err := s.updateAction(ctx, action); err != nil {
		return ManualOrderResult{}, err
	}
	action, err = s.Store.GetOperatorAction(ctx, action.SpaceID, action.ActionID)
	return ManualOrderResult{Action: action, Order: orderRecord}, err
}

func (s *Service) ResumeOperatorAction(
	ctx context.Context,
	action store.OperatorActionRecord,
) error {
	if err := s.validate(); err != nil {
		return err
	}
	if action.Status != "RUNNING" {
		return nil
	}
	switch action.ActionType {
	case "FLATTEN":
		_, err := s.FlattenLogicalAccount(ctx, FlattenCommand{
			SpaceID: action.SpaceID, ActionID: action.ActionID,
			LogicalAccountID: action.LogicalAccountID, Reason: action.Reason,
		})
		return err
	case "MANUAL_ORDER":
		command, err := manualOrderCommand(action)
		if err != nil {
			return err
		}
		_, err = s.PlaceManualOrder(ctx, command)
		return err
	case "CANCEL_ORDER":
		var request cancelOrderRequest
		if err := json.Unmarshal([]byte(action.RequestJSON), &request); err != nil {
			return err
		}
		_, err := s.CancelOrder(ctx, CancelOrderCommand{
			SpaceID: action.SpaceID, ActionID: action.ActionID,
			OrderID: request.OrderID, Reason: action.Reason,
		})
		return err
	default:
		return fmt.Errorf("%w: unsupported action type %q", ErrInvalidCommand, action.ActionType)
	}
}

func manualOrderRequestJSON(command ManualOrderCommand) (string, error) {
	if blank(command.SpaceID) || blank(command.ActionID) ||
		blank(command.ExchangeAccountID) || blank(command.ClientOrderID) ||
		blank(command.InstrumentID) || blank(command.Reason) ||
		(command.Type != exchange.OrderTypeMarket &&
			command.Type != exchange.OrderTypeLimit) ||
		!command.Side.Valid() ||
		command.Quantity.Cmp(shared.Zero()) <= 0 {
		return "", ErrInvalidCommand
	}
	var limitPrice *string
	if command.LimitPrice != nil {
		value := command.LimitPrice.String()
		limitPrice = &value
	}
	data, err := json.Marshal(manualOrderRequest{
		ExchangeAccountID: command.ExchangeAccountID,
		ClientOrderID:     command.ClientOrderID, InstrumentID: command.InstrumentID,
		OrderType: string(command.Type), FillPolicy: string(command.FillPolicy),
		Side: string(command.Side), PositionSide: string(command.PositionSide),
		Quantity: command.Quantity.String(), LimitPrice: limitPrice,
	})
	return string(data), err
}

func manualOrderCommand(
	action store.OperatorActionRecord,
) (ManualOrderCommand, error) {
	var request manualOrderRequest
	if err := json.Unmarshal([]byte(action.RequestJSON), &request); err != nil {
		return ManualOrderCommand{}, err
	}
	quantity, err := shared.ParseDecimal(request.Quantity)
	if err != nil {
		return ManualOrderCommand{}, err
	}
	var limitPrice *shared.Decimal
	if request.LimitPrice != nil {
		value, parseErr := shared.ParseDecimal(*request.LimitPrice)
		if parseErr != nil {
			return ManualOrderCommand{}, parseErr
		}
		limitPrice = &value
	}
	return ManualOrderCommand{
		SpaceID: action.SpaceID, ActionID: action.ActionID,
		ExchangeAccountID: request.ExchangeAccountID,
		ClientOrderID:     request.ClientOrderID, InstrumentID: request.InstrumentID,
		Type:         exchange.OrderType(request.OrderType),
		FillPolicy:   exchange.FillPolicy(request.FillPolicy),
		Side:         exchange.Side(request.Side),
		PositionSide: exchange.PositionSide(request.PositionSide),
		Quantity:     quantity, LimitPrice: limitPrice, Reason: action.Reason,
	}, nil
}

func (s *Service) loadManualOrderResult(
	ctx context.Context,
	action store.OperatorActionRecord,
) (ManualOrderResult, error) {
	if action.ResultJSON == nil {
		return ManualOrderResult{Action: action}, errors.New(action.LastError)
	}
	var result manualOrderActionResult
	if err := json.Unmarshal([]byte(*action.ResultJSON), &result); err != nil {
		return ManualOrderResult{Action: action}, err
	}
	if result.OrderID == "" {
		if action.LastError != "" {
			return ManualOrderResult{Action: action}, errors.New(action.LastError)
		}
		return ManualOrderResult{Action: action}, ErrInvalidCommand
	}
	current, err := s.Store.GetOrder(ctx, action.SpaceID, result.OrderID)
	if err == nil && action.LastError != "" {
		err = errors.New(action.LastError)
	}
	return ManualOrderResult{Action: action, Order: current}, err
}

func (s *Service) failManualAction(
	ctx context.Context,
	action store.OperatorActionRecord,
	cause error,
) (ManualOrderResult, error) {
	action.Status = "FAILED"
	action.LastError = cause.Error()
	result, marshalErr := json.Marshal(map[string]string{"error": cause.Error()})
	if marshalErr == nil {
		raw := string(result)
		action.ResultJSON = &raw
	}
	if err := s.updateAction(ctx, action); err != nil {
		return ManualOrderResult{}, errors.Join(cause, err)
	}
	current, _ := s.Store.GetOperatorAction(ctx, action.SpaceID, action.ActionID)
	return ManualOrderResult{Action: current}, cause
}

func (s *Service) validate() error {
	if s == nil || s.Store == nil || s.Orders == nil ||
		s.Syncer == nil || s.Prices == nil {
		return ErrServiceConfig
	}
	return nil
}

func (s *Service) updateAction(
	ctx context.Context,
	action store.OperatorActionRecord,
) error {
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOperatorAction(action)
	})
}

func (s *Service) cancelLogicalAccountOrders(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	actionID string,
	targetOnly bool,
) error {
	members, err := s.Store.ListLogicalAccountMembers(
		ctx, spaceID, logicalAccountID, true,
	)
	if err != nil {
		return err
	}
	for _, member := range members {
		records, err := s.Store.ListOrdersForAccount(
			ctx, spaceID, member.ExchangeAccountID, 1,
		)
		if err != nil {
			return err
		}
		var selected []store.OrderRecord
		for _, current := range records {
			if orderdomain.State(current.State).Terminal() {
				continue
			}
			if targetOnly && current.OwnerType != string(orderdomain.OwnerTarget) {
				continue
			}
			if !targetOnly &&
				current.OwnerType == string(orderdomain.OwnerOperator) &&
				current.OwnerID == actionID {
				continue
			}
			selected = append(selected, current)
		}
		if len(selected) == 0 {
			continue
		}
		for _, current := range selected {
			if err := s.stopOrder(ctx, current); err != nil {
				return fmt.Errorf("%s: %w", member.ExchangeAccountID, err)
			}
		}
		if err := s.Syncer.SyncAccount(ctx, member.ExchangeAccountID); err != nil {
			return fmt.Errorf("%s: %w", member.ExchangeAccountID, err)
		}
		confirmed, err := s.Store.ListOrdersForAccount(
			ctx, spaceID, member.ExchangeAccountID, 1,
		)
		if err != nil {
			return err
		}
		for _, latest := range confirmed {
			if orderdomain.State(latest.State).Terminal() {
				continue
			}
			if targetOnly && latest.OwnerType != string(orderdomain.OwnerTarget) {
				continue
			}
			if !targetOnly &&
				latest.OwnerType == string(orderdomain.OwnerOperator) &&
				latest.OwnerID == actionID {
				continue
			}
			return fmt.Errorf(
				"%w: account %s order %s remains %s",
				ErrCancelUnconfirmed,
				member.ExchangeAccountID,
				latest.OrderID,
				latest.State,
			)
		}
	}
	return nil
}

func (s *Service) stopOrder(ctx context.Context, current store.OrderRecord) error {
	switch orderdomain.State(current.State) {
	case orderdomain.Pending:
		_, err := s.Orders.DiscardPending(ctx, current.SpaceID, current.OrderID)
		return err
	case orderdomain.Submitting, orderdomain.SubmitUnknown:
		resolved, err := s.Orders.ResolveUnknown(ctx, current.SpaceID, current.OrderID)
		if err != nil || resolved.State.Terminal() {
			return err
		}
		latest, err := s.Store.GetOrder(ctx, current.SpaceID, current.OrderID)
		if err != nil {
			return err
		}
		return s.stopOrder(ctx, latest)
	case orderdomain.Canceling, orderdomain.CancelUnknown:
		_, err := s.Orders.RecoverCancel(ctx, current.SpaceID, current.OrderID)
		return err
	default:
		_, err := s.Orders.Cancel(ctx, current.SpaceID, current.OrderID)
		return err
	}
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}
