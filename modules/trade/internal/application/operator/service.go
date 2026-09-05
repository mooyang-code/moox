package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

var (
	ErrServiceConfig       = errors.New("trade operator: service is not configured")
	ErrInvalidCommand      = errors.New("trade operator: invalid command")
	ErrCancelUnconfirmed   = errors.New("trade operator: cancellation is not confirmed")
	ErrInvalidActionResult = errors.New("trade operator: invalid persisted action result")
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
	Store              *store.Store
	Orders             OrderService
	Syncer             AccountSyncer
	Prices             PriceSource
	Now                func() time.Time
	ManualSubmitWindow time.Duration

	FlattenMaxAttempts   int
	FlattenRetryInterval time.Duration
	FlattenTimeout       time.Duration
}

type ManualOrderCommand struct {
	SpaceID          string
	ActionID         string
	TradingAccountID string
	ClientOrderID    string
	InstrumentID     string
	Type             exchange.OrderType
	FillPolicy       exchange.FillPolicy
	Side             exchange.Side
	PositionSide     exchange.PositionSide
	Quantity         shared.Decimal
	LimitPrice       *shared.Decimal
	Reason           string
}

type ManualOrderResult struct {
	Action   store.OperatorActionRecord
	Order    store.OrderRecord
	Accounts []OperatorAccountError
}

type OperatorAccountError struct {
	TradingAccountID string `json:"trading_account_id"`
	Error            string `json:"error"`
	cause            error
}

type manualOrderRequest struct {
	TradingAccountID string  `json:"trading_account_id"`
	ClientOrderID    string  `json:"client_order_id"`
	InstrumentID     string  `json:"instrument_id"`
	OrderType        string  `json:"order_type"`
	FillPolicy       string  `json:"fill_policy,omitempty"`
	Side             string  `json:"side"`
	PositionSide     string  `json:"position_side,omitempty"`
	Quantity         string  `json:"quantity"`
	LimitPrice       *string `json:"limit_price,omitempty"`
}

type manualOrderActionResult struct {
	ErrorCode  string                 `json:"error_code,omitempty"`
	DeadlineAt int64                  `json:"deadline_at,omitempty"`
	OrderID    string                 `json:"order_id,omitempty"`
	Accounts   []OperatorAccountError `json:"accounts,omitempty"`
}

var manualFailureCodes = []struct {
	code  string
	cause error
}{
	{"INVALID_COMMAND", ErrInvalidCommand},
	{"IDEMPOTENCY_CONFLICT", orderapp.ErrIdempotencyConflict},
	{"CONFLICT", store.ErrConflict},
	{"INVALID_SPEC", orderdomain.ErrInvalidSpec},
	{"ACCOUNT_OWNERSHIP", orderapp.ErrAccountOwnership},
	{"INSTRUMENT_DISABLED", orderapp.ErrInstrumentDisabled},
	{"QUANTITY_RULE", orderapp.ErrQuantityRule},
	{"NOTIONAL_LIMIT", orderapp.ErrNotionalLimit},
	{"INSUFFICIENT_FUNDS", orderapp.ErrInsufficientFunds},
	{"LEVERAGE_LIMIT", orderapp.ErrLeverageLimit},
	{"REDUCE_ONLY", orderapp.ErrReduceOnly},
	{"CROSS_ZERO", orderapp.ErrCrossZero},
}

type manualFailureError struct {
	message string
	cause   error
}

func (e manualFailureError) Error() string { return e.message }
func (e manualFailureError) Unwrap() error { return e.cause }

func manualErrorCode(cause error) string {
	for _, entry := range manualFailureCodes {
		if errors.Is(cause, entry.cause) {
			return entry.code
		}
	}
	return ""
}

func manualPersistedError(message, code string) error {
	for _, entry := range manualFailureCodes {
		if code == entry.code {
			return manualFailureError{message: message, cause: entry.cause}
		}
	}
	return errors.New(message)
}

func (s *Service) PlaceManualOrder(
	ctx context.Context,
	command ManualOrderCommand,
) (ManualOrderResult, error) {
	// Both RPC calls and worker recovery remain owned by their caller, with a
	// finite attempt budget. Durable RUNNING actions survive a canceled attempt.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.validate(); err != nil {
		return ManualOrderResult{}, err
	}
	requestJSON, err := manualOrderRequestJSON(command)
	if err != nil {
		return ManualOrderResult{}, err
	}
	var runningAction *store.OperatorActionRecord
	if existing, getErr := s.Store.GetOperatorAction(
		ctx, command.SpaceID, command.ActionID,
	); getErr == nil {
		expected := store.OperatorActionRecord{
			SpaceID: command.SpaceID, ActionID: command.ActionID,
			LogicalAccountID: existing.LogicalAccountID,
			ActionType:       "MANUAL_ORDER", Reason: strings.TrimSpace(command.Reason),
			RequestJSON: requestJSON,
		}
		current, _, matchErr := s.existingAction(ctx, expected)
		if matchErr != nil {
			return ManualOrderResult{}, matchErr
		}
		if current.Status != "RUNNING" {
			return s.loadManualOrderResult(ctx, current)
		}
		runningAction = &current
	} else if !errors.Is(getErr, gorm.ErrRecordNotFound) {
		return ManualOrderResult{}, getErr
	}
	if runningAction != nil {
		unlock := s.Store.LockLogicalAccount(command.SpaceID, runningAction.LogicalAccountID)
		current, getErr := s.Store.GetOperatorAction(ctx, command.SpaceID, command.ActionID)
		if getErr != nil {
			unlock()
			return ManualOrderResult{}, getErr
		}
		result, handled, recoverErr := s.recoverManualChild(ctx, current, command)
		unlock()
		if handled || recoverErr != nil {
			return result, recoverErr
		}
	}
	logicalAccount, unlock, err := s.lockCurrentLogicalAccount(
		ctx, command.SpaceID, command.TradingAccountID,
	)
	if err != nil {
		if runningAction != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) && !errors.Is(err, ErrInvalidCommand) && !errors.Is(err, store.ErrConflict) {
				return ManualOrderResult{}, err
			}
			return s.failManualAction(
				ctx,
				*runningAction,
				fmt.Errorf("%w: execution account is no longer an enabled member", err),
				[]OperatorAccountError{{
					TradingAccountID: command.TradingAccountID,
					Error:            err.Error(),
				}},
			)
		}
		return ManualOrderResult{}, err
	}
	if runningAction != nil &&
		runningAction.LogicalAccountID != logicalAccount.LogicalAccountID {
		unlock()
		return s.failManualAction(
			ctx,
			*runningAction,
			ErrInvalidCommand,
			[]OperatorAccountError{{
				TradingAccountID: command.TradingAccountID,
				Error:            "execution account moved to another logical account",
			}},
		)
	}
	defer unlock()

	expectedAction := store.OperatorActionRecord{
		SpaceID: command.SpaceID, ActionID: command.ActionID,
		LogicalAccountID: logicalAccount.LogicalAccountID,
		ActionType:       "MANUAL_ORDER", Reason: strings.TrimSpace(command.Reason),
		RequestJSON: requestJSON, Status: "RUNNING",
	}
	existing, found, err := s.existingAction(ctx, expectedAction)
	if err != nil {
		return ManualOrderResult{}, err
	}
	if found && existing.Status != "RUNNING" {
		return s.loadManualOrderResult(ctx, existing)
	}
	var action store.OperatorActionRecord
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
		expectedAction.CreatedAt = time.Now().UTC()
		if s.Now != nil {
			expectedAction.CreatedAt = s.Now().UTC()
		}
		progress, _ := json.Marshal(manualOrderActionResult{DeadlineAt: s.manualDeadlineFrom(expectedAction.CreatedAt)})
		raw := string(progress)
		expectedAction.ResultJSON = &raw
		action, _, ensureErr = tx.EnsureOperatorAction(expectedAction)
		return ensureErr
	})
	if err != nil {
		return ManualOrderResult{}, err
	}
	if result, handled, recoverErr := s.recoverManualChild(ctx, action, command); handled || recoverErr != nil {
		return result, recoverErr
	}
	action, err = s.Store.GetOperatorAction(ctx, action.SpaceID, action.ActionID)
	if err != nil {
		return ManualOrderResult{}, err
	}
	accountErrors := s.cancelLogicalAccountOrders(
		ctx,
		command.SpaceID,
		logicalAccount.LogicalAccountID,
		command.ActionID,
		true,
	)
	if len(accountErrors) > 0 {
		return s.deferManualAction(
			ctx,
			action,
			errors.Join(accountErrorsAsErrors(accountErrors)...),
			accountErrors,
		)
	}
	instrument, err := s.Store.GetInstrumentByIDForAccount(
		ctx, logicalAccount.SpaceID, command.TradingAccountID, command.InstrumentID,
	)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			cause := fmt.Errorf("%w: instrument %q does not exist for execution account", ErrInvalidCommand, command.InstrumentID)
			return s.failManualAction(ctx, action, cause, nil)
		}
		return ManualOrderResult{Action: action}, err
	}
	quote, err := s.Prices.LatestPrice(
		ctx, command.TradingAccountID, instrument.ExchangeSymbol,
	)
	if err != nil {
		return s.deferManualAction(ctx, action, err, nil)
	}
	spec := orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			TradingAccountID: command.TradingAccountID,
			ClientOrderID:    command.ClientOrderID, InstrumentID: command.InstrumentID,
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
	if err != nil {
		if errors.Is(err, orderdomain.ErrReferencePriceStale) {
			return s.deferManualAction(ctx, action, err, nil)
		}
		for _, permanent := range []error{orderapp.ErrIdempotencyConflict, orderdomain.ErrInvalidSpec, orderapp.ErrAccountOwnership, orderapp.ErrInstrumentDisabled, orderapp.ErrQuantityRule, orderapp.ErrNotionalLimit, orderapp.ErrInsufficientFunds, orderapp.ErrLeverageLimit, orderapp.ErrReduceOnly, orderapp.ErrCrossZero} {
			if errors.Is(err, permanent) {
				return s.failManualAction(ctx, action, err, nil)
			}
		}
		result, saveErr := s.deferManualAction(ctx, action, err, nil)
		if saveErr != nil {
			return result, saveErr
		}
		return result, nil
	}
	progress, err := s.manualProgress(ctx, &action)
	if err != nil {
		return ManualOrderResult{}, err
	}
	progress.OrderID = string(placed.ID)
	if err := s.saveManualProgress(ctx, &action, progress); err != nil {
		return ManualOrderResult{}, err
	}
	return s.advanceManualChild(ctx, action, progress)
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

func (s *Service) manualDeadlineFrom(created time.Time) int64 {
	window := s.ManualSubmitWindow
	if window <= 0 {
		window = 60 * time.Second
	}
	return created.Add(window).UnixMilli()
}

func (s *Service) manualExpired(progress manualOrderActionResult) bool {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	return now.UnixMilli() >= progress.DeadlineAt
}

func (s *Service) saveManualProgress(ctx context.Context, action *store.OperatorActionRecord, progress manualOrderActionResult) error {
	raw, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	value := string(raw)
	action.ResultJSON = &value
	persistCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.updateAction(persistCtx, *action)
}

func (s *Service) manualProgress(ctx context.Context, action *store.OperatorActionRecord) (manualOrderActionResult, error) {
	var progress manualOrderActionResult
	if action.ResultJSON != nil {
		if err := json.Unmarshal([]byte(*action.ResultJSON), &progress); err != nil {
			return progress, err
		}
	}
	if progress.DeadlineAt == 0 {
		if action.CreatedAt.IsZero() {
			return progress, fmt.Errorf("%w: missing manual action creation time", store.ErrInvalidRecord)
		}
		progress.DeadlineAt = s.manualDeadlineFrom(action.CreatedAt)
		if err := s.saveManualProgress(ctx, action, progress); err != nil {
			return progress, err
		}
	}
	return progress, nil
}

// Recover the durable child before cancellation or fresh market-data work. Place
// is idempotent and compares the complete client spec, including operator owner.
func (s *Service) recoverManualChild(ctx context.Context, action store.OperatorActionRecord, command ManualOrderCommand) (ManualOrderResult, bool, error) {
	if action.Status != "RUNNING" {
		result, err := s.loadManualOrderResult(ctx, action)
		return result, true, err
	}
	progress, err := s.manualProgress(ctx, &action)
	if err != nil {
		return ManualOrderResult{}, true, err
	}
	child, err := s.Store.GetOrderByClientID(ctx, command.SpaceID, command.TradingAccountID, command.ClientOrderID)
	if errors.Is(err, gorm.ErrRecordNotFound) && progress.OrderID == "" {
		if s.manualExpired(progress) {
			result, err := s.failManualAction(ctx, action, errors.New("manual submission deadline exceeded"), nil)
			return result, true, err
		}
		return ManualOrderResult{}, false, nil
	}
	if err != nil {
		return ManualOrderResult{}, true, err
	}
	if progress.OrderID != "" && progress.OrderID != child.OrderID {
		result, failErr := s.failManualAction(ctx, action, store.ErrConflict, nil)
		return result, true, failErr
	}
	_, err = s.Orders.Place(ctx, command.SpaceID, orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{TradingAccountID: command.TradingAccountID, ClientOrderID: command.ClientOrderID, InstrumentID: command.InstrumentID, Type: command.Type, FillPolicy: command.FillPolicy, Side: command.Side, PositionSide: command.PositionSide, Quantity: command.Quantity, LimitPrice: command.LimitPrice},
		Owner:           orderdomain.OrderOwner{Type: orderdomain.OwnerOperator, OwnerID: command.ActionID, LogicalAccountID: action.LogicalAccountID},
	})
	if err != nil {
		if errors.Is(err, orderapp.ErrIdempotencyConflict) {
			result, failErr := s.failManualAction(ctx, action, err, nil)
			return result, true, failErr
		}
		return ManualOrderResult{}, true, err
	}
	progress.OrderID = child.OrderID
	if err := s.saveManualProgress(ctx, &action, progress); err != nil {
		return ManualOrderResult{}, true, err
	}
	result, err := s.advanceManualChild(ctx, action, progress)
	return result, true, err
}

func (s *Service) advanceManualChild(ctx context.Context, action store.OperatorActionRecord, progress manualOrderActionResult) (ManualOrderResult, error) {
	child, err := s.Store.GetOrder(ctx, action.SpaceID, progress.OrderID)
	if err != nil {
		return ManualOrderResult{}, err
	}
	var callErr error
	if child.State == string(orderdomain.Submitting) || child.State == string(orderdomain.SubmitUnknown) {
		_, callErr = s.Orders.ResolveUnknown(ctx, action.SpaceID, child.OrderID)
		child, err = s.Store.GetOrder(ctx, action.SpaceID, child.OrderID)
		if err != nil {
			return ManualOrderResult{}, err
		}
	}
	if child.State == string(orderdomain.Pending) {
		// Callers hold the action's logical-account lock, which also serializes
		// membership changes. Only unsent orders require this renewed authority.
		owner, member, membershipErr := s.Store.FindLogicalAccountByTradingAccount(ctx, action.SpaceID, child.TradingAccountID)
		if membershipErr != nil && !errors.Is(membershipErr, gorm.ErrRecordNotFound) {
			return ManualOrderResult{}, membershipErr
		}
		if membershipErr != nil || !member.Enabled || owner.LogicalAccountID != action.LogicalAccountID {
			if _, discardErr := s.Orders.DiscardPending(ctx, action.SpaceID, child.OrderID); discardErr != nil {
				return s.deferManualAction(ctx, action, discardErr, nil)
			}
			return s.failManualAction(ctx, action, errors.New("manual order logical account membership changed"), nil)
		}
		if s.manualExpired(progress) {
			_, err = s.Orders.DiscardPending(ctx, action.SpaceID, child.OrderID)
			if err != nil {
				return s.deferManualAction(ctx, action, err, nil)
			}
			return s.failManualAction(ctx, action, errors.New("manual submission deadline exceeded"), nil)
		}
		if callErr == nil {
			now := time.Now()
			if s.Now != nil {
				now = s.Now()
			}
			submitCtx, cancel := context.WithTimeout(ctx, time.UnixMilli(progress.DeadlineAt).Sub(now))
			_, callErr = s.Orders.Submit(submitCtx, action.SpaceID, child.OrderID)
			cancel()
		}
	}
	child, err = s.Store.GetOrder(ctx, action.SpaceID, child.OrderID)
	if err != nil {
		return ManualOrderResult{}, err
	}
	switch orderdomain.State(child.State) {
	case orderdomain.Pending, orderdomain.Submitting, orderdomain.SubmitUnknown:
		if callErr == nil {
			callErr = errors.New("manual submission awaiting confirmation")
		}
		result, saveErr := s.deferManualAction(ctx, action, callErr, nil)
		if saveErr != nil {
			return result, saveErr
		}
		return result, nil
	case orderdomain.Rejected:
		if manualErrorCode(callErr) != "" {
			return s.failManualAction(ctx, action, callErr, nil)
		}
		return s.failManualAction(ctx, action, errors.New(child.RejectReason), nil)
	case orderdomain.Canceled:
		if child.ExchangeOrderID == "" {
			return s.failManualAction(ctx, action, errors.New("manual order discarded before acceptance"), nil)
		}
	case orderdomain.Open, orderdomain.PartiallyFilled, orderdomain.Filled,
		orderdomain.Canceling, orderdomain.CancelUnknown, orderdomain.PartiallyCanceled, orderdomain.Expired:
	default:
		return ManualOrderResult{Action: action, Order: child}, fmt.Errorf("invalid manual child state %q", child.State)
	}
	action.Status = "COMPLETED"
	action.LastError = ""
	if err := s.saveManualProgress(ctx, &action, progress); err != nil {
		return ManualOrderResult{}, err
	}
	return s.loadManualOrderResult(ctx, action)
}

func (s *Service) deferManualAction(ctx context.Context, action store.OperatorActionRecord, cause error, accounts []OperatorAccountError) (ManualOrderResult, error) {
	progress, err := s.manualProgress(ctx, &action)
	if err != nil {
		return ManualOrderResult{}, err
	}
	action.Status = "RUNNING"
	action.LastError = cause.Error()
	progress.Accounts = accounts
	if err := s.saveManualProgress(ctx, &action, progress); err != nil {
		return ManualOrderResult{}, err
	}
	return s.loadManualOrderResult(ctx, action)
}

func manualOrderRequestJSON(command ManualOrderCommand) (string, error) {
	if blank(command.SpaceID) || blank(command.ActionID) ||
		blank(command.TradingAccountID) || blank(command.ClientOrderID) ||
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
		TradingAccountID: command.TradingAccountID,
		ClientOrderID:    command.ClientOrderID, InstrumentID: command.InstrumentID,
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
		TradingAccountID: request.TradingAccountID,
		ClientOrderID:    request.ClientOrderID, InstrumentID: request.InstrumentID,
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
	invalidResult := func(detail string) (ManualOrderResult, error) {
		return ManualOrderResult{Action: action}, fmt.Errorf("%w: %w: %s", ErrInvalidActionResult, store.ErrInvalidRecord, detail)
	}
	if action.Status != "RUNNING" && action.Status != "COMPLETED" && action.Status != "FAILED" {
		return invalidResult("unsupported manual action status")
	}
	if action.ResultJSON == nil {
		if action.Status == "RUNNING" {
			return ManualOrderResult{Action: action}, nil
		}
		return invalidResult("terminal manual action has no result")
	}
	var result *manualOrderActionResult
	if err := json.Unmarshal([]byte(*action.ResultJSON), &result); err != nil {
		return invalidResult("malformed result JSON")
	}
	if result == nil {
		return invalidResult("result is not an object")
	}
	if action.Status == "COMPLETED" && (strings.TrimSpace(result.OrderID) == "" || action.LastError != "") {
		return invalidResult("completed action requires child order and no error")
	}
	if action.Status == "FAILED" && strings.TrimSpace(action.LastError) == "" {
		return invalidResult("failed action requires an error")
	}
	if result.OrderID == "" {
		if action.Status == "RUNNING" {
			return ManualOrderResult{Action: action, Accounts: result.Accounts}, nil
		}
		if action.LastError != "" {
			return ManualOrderResult{
				Action: action, Accounts: result.Accounts,
			}, manualPersistedError(action.LastError, result.ErrorCode)
		}
		return invalidResult("terminal action has no child or error")
	}
	current, err := s.Store.GetOrder(ctx, action.SpaceID, result.OrderID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return invalidResult("linked child order is missing")
	}
	if err == nil && action.Status == "FAILED" && action.LastError != "" {
		err = manualPersistedError(action.LastError, result.ErrorCode)
	}
	return ManualOrderResult{
		Action: action, Order: current, Accounts: result.Accounts,
	}, err
}

func (s *Service) failManualAction(
	ctx context.Context,
	action store.OperatorActionRecord,
	cause error,
	accounts []OperatorAccountError,
) (ManualOrderResult, error) {
	action.Status = "FAILED"
	action.LastError = cause.Error()
	var progress manualOrderActionResult
	if action.ResultJSON != nil {
		if err := json.Unmarshal([]byte(*action.ResultJSON), &progress); err != nil {
			return ManualOrderResult{}, err
		}
	}
	progress.Accounts = accounts
	progress.ErrorCode = manualErrorCode(cause)
	result, marshalErr := json.Marshal(progress)
	if marshalErr == nil {
		raw := string(result)
		action.ResultJSON = &raw
	}
	if err := s.updateAction(ctx, action); err != nil {
		return ManualOrderResult{}, err
	}
	return s.loadManualOrderResult(ctx, action)
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
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
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
) []OperatorAccountError {
	members, err := s.Store.ListLogicalAccountMembers(
		ctx, spaceID, logicalAccountID, true,
	)
	if err != nil {
		return []OperatorAccountError{{Error: err.Error()}}
	}
	var accountErrors []OperatorAccountError
	for _, member := range members {
		var currentErrors []error
		if err := s.Syncer.SyncAccount(ctx, member.TradingAccountID); err != nil {
			currentErrors = append(currentErrors, fmt.Errorf("fresh sync: %w", err))
		}
		records, err := s.Store.ListOrdersForAccount(
			ctx, spaceID, member.TradingAccountID, 1,
		)
		if err != nil {
			currentErrors = append(currentErrors, err)
			accountErrors = appendAccountErrors(
				accountErrors, member.TradingAccountID, currentErrors,
			)
			continue
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
		for _, current := range selected {
			if err := s.stopOrder(ctx, current); err != nil {
				currentErrors = append(
					currentErrors,
					fmt.Errorf("stop order %s: %w", current.OrderID, err),
				)
			}
		}
		if err := s.Syncer.SyncAccount(ctx, member.TradingAccountID); err != nil {
			currentErrors = append(currentErrors, fmt.Errorf("confirm sync: %w", err))
		}
		confirmed, err := s.Store.ListOrdersForAccount(
			ctx, spaceID, member.TradingAccountID, 1,
		)
		if err != nil {
			currentErrors = append(currentErrors, err)
			accountErrors = appendAccountErrors(
				accountErrors, member.TradingAccountID, currentErrors,
			)
			continue
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
			currentErrors = append(
				currentErrors,
				fmt.Errorf(
					"%w: order %s remains %s",
					ErrCancelUnconfirmed,
					latest.OrderID,
					latest.State,
				),
			)
		}
		accountErrors = appendAccountErrors(
			accountErrors, member.TradingAccountID, currentErrors,
		)
	}
	return accountErrors
}

func (s *Service) existingAction(
	ctx context.Context,
	expected store.OperatorActionRecord,
) (store.OperatorActionRecord, bool, error) {
	current, err := s.Store.GetOperatorAction(
		ctx, expected.SpaceID, expected.ActionID,
	)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.OperatorActionRecord{}, false, nil
	}
	if err != nil {
		return store.OperatorActionRecord{}, false, err
	}
	if current.SpaceID != expected.SpaceID ||
		current.ActionID != expected.ActionID ||
		current.LogicalAccountID != expected.LogicalAccountID ||
		current.ActionType != expected.ActionType ||
		current.Reason != expected.Reason ||
		!equalJSON(current.RequestJSON, expected.RequestJSON) {
		return store.OperatorActionRecord{}, false, store.ErrConflict
	}
	return current, true, nil
}

func (s *Service) lockCurrentLogicalAccount(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
) (store.LogicalAccountRecord, func(), error) {
	for attempts := 0; attempts < 4; attempts++ {
		current, member, err := s.Store.FindLogicalAccountByTradingAccount(
			ctx, spaceID, tradingAccountID,
		)
		if err != nil {
			return store.LogicalAccountRecord{}, nil, err
		}
		if !member.Enabled {
			return store.LogicalAccountRecord{}, nil, ErrInvalidCommand
		}
		unlock := s.Store.LockLogicalAccount(spaceID, current.LogicalAccountID)
		confirmed, confirmedMember, err := s.Store.FindLogicalAccountByTradingAccount(
			ctx, spaceID, tradingAccountID,
		)
		if err == nil && confirmedMember.Enabled &&
			confirmed.LogicalAccountID == current.LogicalAccountID {
			return confirmed, unlock, nil
		}
		unlock()
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return store.LogicalAccountRecord{}, nil, err
		}
	}
	return store.LogicalAccountRecord{}, nil,
		fmt.Errorf("%w: logical account membership changed", store.ErrConflict)
}

func appendAccountErrors(
	values []OperatorAccountError,
	tradingAccountID string,
	errs []error,
) []OperatorAccountError {
	if len(errs) == 0 {
		return values
	}
	return append(values, OperatorAccountError{
		TradingAccountID: tradingAccountID,
		Error:            errors.Join(errs...).Error(),
		cause:            errors.Join(errs...),
	})
}

func accountErrorsAsErrors(values []OperatorAccountError) []error {
	errs := make([]error, 0, len(values))
	for _, value := range values {
		cause := value.cause
		if cause == nil {
			cause = errors.New(value.Error)
		}
		errs = append(errs, fmt.Errorf("%s: %w", value.TradingAccountID, cause))
	}
	return errs
}

func equalJSON(left, right string) bool {
	var leftValue any
	var rightValue any
	if json.Unmarshal([]byte(left), &leftValue) != nil ||
		json.Unmarshal([]byte(right), &rightValue) != nil {
		return left == right
	}
	return reflect.DeepEqual(leftValue, rightValue)
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
		if latest.State == string(orderdomain.Submitting) || latest.State == string(orderdomain.SubmitUnknown) {
			return ErrCancelUnconfirmed
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
