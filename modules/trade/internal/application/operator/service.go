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
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

var (
	ErrServiceConfig       = errors.New("人工操作服务未配置")
	ErrInvalidCommand      = errors.New("人工操作参数不合法")
	ErrCancelUnconfirmed   = errors.New("撤单还没有得到交易所确认")
	ErrInvalidActionResult = errors.New("已保存的人工操作结果无法解析")
)

// 人工单失败时写进操作 last_error 的原因，前端原样显示。
const (
	manualNotMemberText = "执行账户已不是这个组合的启用成员，人工单没有发出"
	manualDeadlineText  = "超过了人工下单的提交期限，订单没有发出，需要的话重新下单"
)

type Quote = targetapp.Quote

type PriceSource interface {
	LatestPrice(context.Context, string, exchange.SymbolKey) (Quote, error)
}

type AccountSyncer interface {
	SyncAccount(context.Context, string) error
}

// LightSyncer 只读持仓和账户恢复准入，同步器实现时清仓用它代替增量同步，省掉读全部挂单的权重。
type LightSyncer interface {
	SyncAccountLight(context.Context, string) error
}

// syncLight 优先用轻量同步。
func (s *Service) syncLight(ctx context.Context, tradingAccountID string) error {
	if light, ok := s.Syncer.(LightSyncer); ok {
		return light.SyncAccountLight(ctx, tradingAccountID)
	}
	return s.Syncer.SyncAccount(ctx, tradingAccountID)
}

type OrderService interface {
	Place(context.Context, string, orderdomain.OrderSpec) (orderdomain.Order, error)
	Submit(context.Context, string, string) (orderdomain.Order, error)
	Cancel(context.Context, string, string) (orderdomain.Order, error)
	DiscardPending(context.Context, string, string) (orderdomain.Order, error)
	ResolveUnknown(context.Context, string, string) (orderdomain.Order, error)
	ExpireUnknown(context.Context, string, string, string) (orderdomain.Order, error)
	RecoverCancel(context.Context, string, string) (orderdomain.Order, error)
}

type Service struct {
	Store              *store.Store
	Orders             OrderService
	Syncer             AccountSyncer
	Prices             PriceSource
	Exchanges          execution.ExchangeCatalog
	Now                func() time.Time
	ManualSubmitWindow time.Duration

	FlattenMaxAttempts   int
	FlattenRetryInterval time.Duration
	FlattenTimeout       time.Duration

	// Wake 在下架清仓结束时唤醒目标重新收敛，为空时不唤醒。
	Wake func()
	// DelistAttemptInterval 是两笔下架清仓 IOC 单之间的最小间隔：零用默认的 3 秒，负数不限制（测试用）。
	DelistAttemptInterval time.Duration
}

// ManualOrderCommand 是一笔人工下单：给 LogicalAccountID 组合里的执行账户 TradingAccountID 下单。
type ManualOrderCommand struct {
	SpaceID          string
	ActionID         string
	LogicalAccountID string
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
	DeadlineAt       int64
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
	DeadlineAt       int64   `json:"deadline_at,omitempty"`
	LogicalAccountID string  `json:"logical_account_id"`
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

// manualOrderActionResult 是人工下单动作的进度与结果。Takeover 在建动作时按组合的控制模式定下（见 PlaceOrder），之后不再改。
type manualOrderActionResult struct {
	Takeover   bool                   `json:"takeover,omitempty"`
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

// ErrRecordedFailure 标记从库里还原出来的操作失败：文案是当时落库的原因，回给前端时照原样返回。
var ErrRecordedFailure = errors.New("已记录的操作失败")

type recordedFailure struct{ message string }

// RecordedFailure 用落库的失败原因还原一个错误。
func RecordedFailure(message string) error { return recordedFailure{message: message} }

func (e recordedFailure) Error() string        { return e.message }
func (e recordedFailure) Is(target error) bool { return target == ErrRecordedFailure }

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
	return manualFailureError{message: message, cause: recordedFailure{message: message}}
}

// PlaceOrder 是人工下单的唯一入口，动作类型 MANUAL_ORDER。走哪条流程在第一次受理时按组合的控制模式定下，
// 记进动作结果（takeover），同一个 action_id 重试、后台恢复都沿用它，不因为期间控制模式变了换一条：
//   - 接管（策略驱动的组合）：受理时暂停组合，下单前撤掉活动的 TARGET 订单，RPC 里同步提交到交易所，完成后组合保持暂停；
//   - 直接下单（自主下单的组合，MANUAL）：不暂停、不撤别的订单，本地受理即返回，交易所提交由后台工作线程完成。
//
// 两条流程共同的前提：执行账户是 command.LogicalAccountID 的启用成员（页面是在这个组合里发起的，不能暂停到别的组合上）；
// 新建动作时执行账户已启用、当前在交易时段内。
func (s *Service) PlaceOrder(ctx context.Context, command ManualOrderCommand) (ManualOrderResult, error) {
	return s.placeOrder(ctx, command, false)
}

// placeOrder 是 PlaceOrder 的实现。resume 为真表示后台工作线程在恢复运行中的动作：直接下单的流程也在这里把子单推进到交易所。
func (s *Service) placeOrder(
	ctx context.Context,
	command ManualOrderCommand,
	resume bool,
) (result ManualOrderResult, retErr error) {
	// RPC 和后台恢复都只给有限的时长；运行中的动作落了库，这一次超时不影响下一次接着做。
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var knownAction store.OperatorActionRecord
	var knownOrderID string
	takeover := false
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
		// 接管流程是同步提交：只是暂时没做完（行情、账户就绪等）的，动作仍在运行、由后台接着做，对调用方算受理成功。
		// 直接下单本来就由后台提交，原样返回这次的原因。
		if takeover && result.Action.Status == string(operatordomain.StatusRunning) && allSubmissionErrors(retErr, func(err error) bool {
			_, deferred := err.(submissionDeferredError)
			return deferred
		}) {
			retErr = nil
		}
	}()
	if s == nil || s.Store == nil || s.Orders == nil || s.Prices == nil {
		return ManualOrderResult{}, ErrServiceConfig
	}
	requestJSON, err := manualOrderRequestJSON(command)
	if err != nil {
		return ManualOrderResult{}, err
	}
	expected := store.OperatorActionRecord{
		SpaceID: command.SpaceID, ActionID: command.ActionID,
		LogicalAccountID: command.LogicalAccountID,
		ActionType:       string(operatordomain.ActionManualOrder), Reason: strings.TrimSpace(command.Reason),
		RequestJSON: requestJSON, Status: string(operatordomain.StatusRunning),
	}
	existing, found, err := s.existingAction(ctx, expected)
	if err != nil {
		return ManualOrderResult{}, err
	}
	if found {
		if existing.Status != string(operatordomain.StatusRunning) {
			return s.loadManualOrderResult(ctx, existing)
		}
		knownAction = existing
		takeover = recordedTakeover(existing)
	} else if err := s.checkManualSession(ctx, command.TradingAccountID); err != nil {
		return ManualOrderResult{}, err
	}
	unlock, err := s.Store.LockLogicalAccountContext(ctx, command.SpaceID, command.LogicalAccountID)
	if err != nil {
		if found {
			current, diagnosticErr := s.deferActionLock(ctx, knownAction, err)
			return submissionIdentity(current), diagnosticErr
		}
		return ManualOrderResult{}, err
	}
	defer unlock()
	// 拿到锁之后重读：等锁期间别的调用可能已经建了、做完了这个动作。
	action, found, err := s.existingAction(ctx, expected)
	if err != nil {
		return ManualOrderResult{}, err
	}
	if found {
		knownAction = action
		progress, progressErr := s.manualProgress(ctx, &action)
		if progressErr != nil {
			return ManualOrderResult{}, progressErr
		}
		takeover = progress.Takeover
		// 先找回已经落库的子单，再做撤单、取行情这些新工作；Place 按客户端单号幂等，比对完整的下单参数。
		if result, handled, err := s.recoverChild(ctx, action, command, takeover || resume); handled || err != nil {
			return result, err
		}
	}
	logical, member, err := s.Store.FindLogicalAccountByTradingAccount(ctx, command.SpaceID, command.TradingAccountID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return ManualOrderResult{}, err
	}
	if membershipErr := manualMembershipError(err, logical, member, command.LogicalAccountID, found, takeover); membershipErr != nil {
		if found {
			return s.failManualAction(ctx, action, membershipErr, []OperatorAccountError{{
				TradingAccountID: command.TradingAccountID, Error: membershipErr.Error(),
			}})
		}
		return ManualOrderResult{}, membershipErr
	}
	if !found {
		takeover = logical.ControlMode != "MANUAL"
		if action, err = s.createManualAction(ctx, expected, command, takeover); err != nil {
			return ManualOrderResult{}, err
		}
		knownAction = action
		// 建动作与上面的崩溃缺口：上一次可能已经建了子单但没来得及记进动作。
		if result, handled, err := s.recoverChild(ctx, action, command, takeover || resume); handled || err != nil {
			return result, err
		}
	}
	if takeover {
		if s.Syncer == nil {
			return ManualOrderResult{Action: action}, ErrServiceConfig
		}
		accountErrors := s.cancelLogicalAccountOrders(ctx, command.SpaceID, command.LogicalAccountID, command.ActionID, true)
		if len(accountErrors) > 0 {
			cause := errors.Join(accountErrorsAsErrors(accountErrors)...)
			result, saveErr := s.submissionDiagnostic(ctx, action, cause, accountErrors...)
			if saveErr != nil {
				return result, errors.Join(cause, saveErr)
			}
			if !submissionAccountError(cause) {
				return result, cause
			}
			return result, nil
		}
	}
	instrument, err := s.Store.GetInstrumentByIDForAccount(ctx, command.SpaceID, command.TradingAccountID, command.InstrumentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return s.failManualAction(ctx, action, fmt.Errorf("%w: 执行账户没有标的 %s", ErrInvalidCommand, command.InstrumentID), nil)
		}
		return ManualOrderResult{Action: action}, err
	}
	quote, err := s.Prices.LatestPrice(ctx, command.TradingAccountID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
	if err != nil {
		return s.deferSubmission(ctx, action, err)
	}
	placed, err := s.Orders.Place(ctx, command.SpaceID, orderdomain.OrderSpec{
		ClientOrderSpec: command.clientSpec(),
		ReferencePrice:  quote.Price, ReferencePriceAt: quote.UpdatedAt,
		Owner: command.owner(),
	})
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
	if takeover || resume {
		return s.advanceManualChild(ctx, action, progress)
	}
	return s.loadManualOrderResult(ctx, action)
}

// manualMembershipError 检查执行账户还能不能在这个组合里人工下单（调用方持有组合锁，成员关系不会再变）。
// 直接下单的动作还要求组合仍是自主下单：组合改回策略驱动后再不暂停就下单，会和策略的订单打架。
func manualMembershipError(
	lookupErr error,
	logical store.LogicalAccountRecord,
	member store.LogicalAccountMemberRecord,
	logicalAccountID string,
	found, takeover bool,
) error {
	switch {
	case lookupErr != nil || !member.Enabled:
		if found {
			return fmt.Errorf("%w: %s", ErrInvalidCommand, manualNotMemberText)
		}
		return fmt.Errorf("%w: 执行账户不是这个组合的启用成员", ErrInvalidCommand)
	case logical.LogicalAccountID != logicalAccountID:
		if found {
			return fmt.Errorf("%w: 执行账户已移到别的组合，人工单没有发出", ErrInvalidCommand)
		}
		return fmt.Errorf("%w: 执行账户不在这个组合里", ErrInvalidCommand)
	case found && !takeover && logical.ControlMode != "MANUAL":
		return fmt.Errorf("%w: 组合已改成策略驱动，人工单没有发出", ErrInvalidCommand)
	}
	return nil
}

// createManualAction 建动作：执行账户必须已启用；接管流程在同一个事务里暂停组合。动作结果先记下提交期限和流程。
func (s *Service) createManualAction(
	ctx context.Context,
	expected store.OperatorActionRecord,
	command ManualOrderCommand,
	takeover bool,
) (store.OperatorActionRecord, error) {
	account, err := s.Store.GetTradingAccount(ctx, command.SpaceID, command.TradingAccountID)
	if err != nil {
		return store.OperatorActionRecord{}, err
	}
	if account.Status != "ENABLED" {
		return store.OperatorActionRecord{}, fmt.Errorf("%w: %w", ErrInvalidCommand, tradingaccount.ErrAccountNotExecutable)
	}
	if takeover && s.Syncer == nil {
		return store.OperatorActionRecord{}, ErrServiceConfig
	}
	expected.CreatedAt = s.clock().UTC()
	expected.UpdatedAt = expected.CreatedAt
	if expected.Source == "" {
		expected.Source = string(operatordomain.SourceUser)
	}
	deadline := command.DeadlineAt
	if deadline == 0 {
		deadline = s.manualDeadlineFrom(expected.CreatedAt)
	}
	progress, err := json.Marshal(manualOrderActionResult{DeadlineAt: deadline, Takeover: takeover})
	if err != nil {
		return store.OperatorActionRecord{}, err
	}
	raw := string(progress)
	expected.ResultJSON = &raw
	var action store.OperatorActionRecord
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if takeover {
			if err := tx.PauseLogicalAccount(command.SpaceID, command.LogicalAccountID, store.UserPause(strings.TrimSpace(command.Reason))); err != nil {
				return err
			}
		}
		var ensureErr error
		action, _, ensureErr = tx.EnsureOperatorAction(expected)
		return ensureErr
	})
	return action, err
}

// recordedTakeover 读动作结果里落库的流程；结果读不出来时按直接下单算，拿到锁之后 manualProgress 会照常报错。
func recordedTakeover(action store.OperatorActionRecord) bool {
	var progress manualOrderActionResult
	if action.ResultJSON != nil {
		_ = json.Unmarshal([]byte(*action.ResultJSON), &progress)
	}
	return progress.Takeover
}

func (c ManualOrderCommand) clientSpec() orderdomain.ClientOrderSpec {
	return orderdomain.ClientOrderSpec{
		TradingAccountID: c.TradingAccountID, ClientOrderID: c.ClientOrderID, InstrumentID: c.InstrumentID,
		Type: c.Type, FillPolicy: c.FillPolicy, Side: c.Side, PositionSide: c.PositionSide,
		Quantity: c.Quantity, LimitPrice: c.LimitPrice,
	}
}

func (c ManualOrderCommand) owner() orderdomain.OrderOwner {
	return orderdomain.OrderOwner{Type: orderdomain.OwnerOperator, OwnerID: c.ActionID, LogicalAccountID: c.LogicalAccountID}
}

func (s *Service) ResumeOperatorAction(
	ctx context.Context,
	action store.OperatorActionRecord,
) error {
	if action.Status != string(operatordomain.StatusRunning) {
		return nil
	}
	if action.ActionType == string(operatordomain.ActionManualOrder) {
		command, err := manualOrderCommand(action)
		if err != nil {
			return err
		}
		result, err := s.placeOrder(ctx, command, true)
		return submissionRecoveryError(result, err)
	}
	if err := s.validate(); err != nil {
		return err
	}
	switch action.ActionType {
	case string(operatordomain.ActionFlatten), string(operatordomain.ActionReduce), string(operatordomain.ActionDelistFlatten):
		command, err := decodeFlattenRequest(action)
		if err != nil {
			return err
		}
		result, err := s.FlattenLogicalAccount(ctx, command)
		return submissionRecoveryError(ManualOrderResult{Action: result.Action}, err)
	case string(operatordomain.ActionCancelOrder):
		var request cancelOrderRequest
		if err := json.Unmarshal([]byte(action.RequestJSON), &request); err != nil {
			return err
		}
		result, err := s.CancelOrder(ctx, CancelOrderCommand{
			SpaceID: action.SpaceID, ActionID: action.ActionID,
			OrderID: request.OrderID, Reason: action.Reason,
		})
		return submissionRecoveryError(ManualOrderResult{Action: result.Action}, err)
	default:
		return fmt.Errorf("%w: 不支持的操作类型 %q", ErrInvalidCommand, action.ActionType)
	}
}

func (s *Service) actionOlderThan(action store.OperatorActionRecord, age time.Duration) bool {
	if action.CreatedAt.IsZero() {
		return false
	}
	return s.clock().Sub(action.CreatedAt) >= age
}

// checkManualSession 拒绝不在交易时段的手动下单（A 股）：模拟撮合时段外不撮合，实盘券商也不收，直接说清楚时段。
func (s *Service) checkManualSession(ctx context.Context, tradingAccountID string) error {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if ok, _ := execution.CanOrder(s.Exchanges, account.Exchange, s.clock()); ok {
		return nil
	}
	message := "不在交易时段"
	if hours := execution.SessionHours(s.Exchanges, account.Exchange); hours != "" {
		message += "（" + hours + "）"
	}
	return fmt.Errorf("%w: %s", ErrInvalidCommand, message)
}

func (s *Service) manualDeadlineFrom(created time.Time) int64 {
	window := s.ManualSubmitWindow
	if window <= 0 {
		window = 60 * time.Second
	}
	return created.Add(window).UnixMilli()
}

func (s *Service) manualExpired(progress manualOrderActionResult) bool {
	return s.clock().UnixMilli() >= progress.DeadlineAt
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
		var request manualOrderRequest
		if err := json.Unmarshal([]byte(action.RequestJSON), &request); err != nil {
			return progress, err
		}
		progress.DeadlineAt = request.DeadlineAt
		if progress.DeadlineAt == 0 {
			progress.DeadlineAt = s.manualDeadlineFrom(action.CreatedAt)
		}
		if err := s.saveManualProgress(ctx, action, progress); err != nil {
			return progress, err
		}
	}
	return progress, nil
}

// Recover the durable child before cancellation or fresh market-data work. Place
// is idempotent and compares the complete client spec, including operator owner.
func (s *Service) recoverManualChild(ctx context.Context, action store.OperatorActionRecord, command ManualOrderCommand) (ManualOrderResult, bool, error) {
	return s.recoverChild(ctx, action, command, true)
}

func (s *Service) recoverChild(ctx context.Context, action store.OperatorActionRecord, command ManualOrderCommand, advance bool) (result ManualOrderResult, handled bool, retErr error) {
	var trustedChild *store.OrderRecord
	persistedAction := action
	defer func() {
		if retErr != nil && trustedChild != nil {
			if result.Order.OrderID == "" {
				result.Order = *trustedChild
			}
			if result.Action.ActionID == "" {
				result.Action = persistedAction
			}
		}
	}()
	if action.Status != string(operatordomain.StatusRunning) {
		result, err := s.loadManualOrderResult(ctx, action)
		return result, true, err
	}
	progress, err := s.manualProgress(ctx, &action)
	if err != nil {
		return ManualOrderResult{}, true, err
	}
	persistedAction = action
	child, err := s.Store.GetOrderByClientID(ctx, command.SpaceID, command.TradingAccountID, exchange.MarketOfInstrumentID(command.InstrumentID), command.ClientOrderID)
	if errors.Is(err, gorm.ErrRecordNotFound) && progress.OrderID == "" {
		if s.manualExpired(progress) {
			result, err := s.failManualAction(ctx, action, errors.New(manualDeadlineText), nil)
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
	if matchesRecoveryChild(child, action, command) {
		trustedChild = &child
	}
	_, err = s.Orders.Place(ctx, command.SpaceID, orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{TradingAccountID: command.TradingAccountID, ClientOrderID: command.ClientOrderID, InstrumentID: command.InstrumentID, Type: command.Type, FillPolicy: command.FillPolicy, Side: command.Side, PositionSide: command.PositionSide, Quantity: command.Quantity, LimitPrice: command.LimitPrice},
		Owner:           orderdomain.OrderOwner{Type: orderdomain.OwnerOperator, OwnerID: command.ActionID, LogicalAccountID: action.LogicalAccountID},
	})
	if err != nil {
		if errors.Is(err, orderapp.ErrIdempotencyConflict) && submissionBusinessError(err) {
			result, failErr := s.failManualAction(ctx, action, err, nil)
			return result, true, failErr
		}
		result, recoverErr := s.submissionOrderError(ctx, action, err)
		return result, true, recoverErr
	}
	progress.OrderID = child.OrderID
	if err := s.saveManualProgress(ctx, &action, progress); err != nil {
		return ManualOrderResult{}, true, err
	}
	persistedAction = action
	if !advance {
		result, err := s.loadManualOrderResult(ctx, action)
		return result, true, err
	}
	result, err = s.advanceManualChild(ctx, action, progress)
	return result, true, err
}

// A client ID alone is not authority to return an unrelated order. Validate
// persisted ownership and all user-controlled order fields before retaining it
// across a failed idempotency lookup or action-link write.
func matchesRecoveryChild(child store.OrderRecord, action store.OperatorActionRecord, command ManualOrderCommand) bool {
	if child.SpaceID != action.SpaceID || child.TradingAccountID != command.TradingAccountID || child.ClientOrderID != command.ClientOrderID ||
		child.OwnerType != string(orderdomain.OwnerOperator) || child.OwnerID != action.ActionID || child.LogicalAccountID != action.LogicalAccountID || child.InstanceID != "" ||
		child.InstrumentID != command.InstrumentID || child.OrderType != string(command.Type) || child.TimeInForce != string(command.FillPolicy) ||
		child.Side != string(command.Side) || child.PositionSide != string(command.PositionSide) {
		return false
	}
	quantity, err := shared.ParseDecimal(child.Quantity)
	if err != nil || quantity.Cmp(command.Quantity) != 0 {
		return false
	}
	if child.LimitPrice == nil || command.LimitPrice == nil {
		return child.LimitPrice == nil && command.LimitPrice == nil
	}
	price, err := shared.ParseDecimal(*child.LimitPrice)
	return err == nil && price.Cmp(*command.LimitPrice) == 0
}

func (s *Service) advanceManualChild(ctx context.Context, action store.OperatorActionRecord, progress manualOrderActionResult) (ManualOrderResult, error) {
	child, err := s.Store.GetOrder(ctx, action.SpaceID, progress.OrderID)
	if err != nil {
		return ManualOrderResult{}, err
	}
	var callErr error
	if child.State == string(orderdomain.Submitting) || child.State == string(orderdomain.SubmitUnknown) {
		_, callErr = s.Orders.ResolveUnknown(ctx, action.SpaceID, child.OrderID)
		if ctx.Err() != nil {
			if callErr == nil {
				callErr = &orderapp.AccountExecutionError{TradingAccountID: child.TradingAccountID, Operation: "resolve_deadline", Err: ctx.Err()}
			}
			return s.submissionOrderError(ctx, action, callErr)
		}
		child, err = s.Store.GetOrder(ctx, action.SpaceID, child.OrderID)
		if err != nil {
			return ManualOrderResult{}, err
		}
		if callErr != nil && !submissionAccountError(callErr) &&
			(child.State == string(orderdomain.Pending) || child.State == string(orderdomain.Submitting) || child.State == string(orderdomain.SubmitUnknown)) {
			return s.submissionOrderError(ctx, action, callErr)
		}
	}
	if child.State == string(orderdomain.Pending) {
		// Callers hold the action's logical-account lock, which also serializes
		// membership changes. Only unsent orders require this renewed authority.
		owner, member, membershipErr := s.Store.FindLogicalAccountByTradingAccount(ctx, action.SpaceID, child.TradingAccountID)
		if membershipErr != nil && !errors.Is(membershipErr, gorm.ErrRecordNotFound) {
			return ManualOrderResult{}, membershipErr
		}
		if membershipErr != nil || !member.Enabled || owner.LogicalAccountID != action.LogicalAccountID || (!progress.Takeover && owner.ControlMode != "MANUAL") {
			if _, discardErr := s.Orders.DiscardPending(ctx, action.SpaceID, child.OrderID); discardErr != nil {
				return s.submissionOrderError(ctx, action, discardErr)
			}
			return s.failManualAction(ctx, action, errors.New("组合成员关系变了（执行账户被移出或停用，或组合换了控制方式），人工单没有发出"), nil)
		}
		if s.manualExpired(progress) {
			_, err = s.Orders.DiscardPending(ctx, action.SpaceID, child.OrderID)
			if err != nil {
				return s.submissionOrderError(ctx, action, err)
			}
			return s.failManualAction(ctx, action, errors.New(manualDeadlineText), nil)
		}
		if callErr == nil {
			submitCtx, cancel := context.WithTimeout(ctx, time.UnixMilli(progress.DeadlineAt).Sub(s.clock()))
			_, callErr = s.Orders.Submit(submitCtx, action.SpaceID, child.OrderID)
			cancel()
		}
	}
	if ctx.Err() != nil {
		if callErr == nil {
			callErr = &orderapp.AccountExecutionError{TradingAccountID: child.TradingAccountID, Operation: "submit_deadline", Err: ctx.Err()}
		}
		return s.submissionOrderError(ctx, action, callErr)
	}
	child, err = s.Store.GetOrder(ctx, action.SpaceID, child.OrderID)
	if err != nil {
		return ManualOrderResult{}, err
	}
	if callErr != nil && !submissionAccountError(callErr) &&
		(child.State == string(orderdomain.Pending) || child.State == string(orderdomain.Submitting) || child.State == string(orderdomain.SubmitUnknown)) {
		return s.submissionOrderError(ctx, action, callErr)
	}
	// Exchange work is over. Only persist the observed terminal outcome.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	switch orderdomain.State(child.State) {
	case orderdomain.Pending, orderdomain.Submitting, orderdomain.SubmitUnknown:
		if callErr == nil {
			callErr = errors.New("人工单已提交，正在等交易所确认")
		}
		result, saveErr := s.deferManualAction(ctx, action, callErr, nil)
		if saveErr != nil {
			return result, saveErr
		}
		return result, nil
	case orderdomain.Rejected:
		if manualErrorCode(callErr) != "" && submissionBusinessError(callErr) {
			return s.failManualAction(ctx, action, callErr, nil)
		}
		result, saveErr := s.failManualAction(ctx, action, errors.New(child.RejectReason), nil)
		if callErr != nil && !submissionAccountError(callErr) {
			return result, errors.Join(saveErr, callErr)
		}
		return result, saveErr
	case orderdomain.Canceled:
		if child.ExchangeOrderID == "" {
			return s.failManualAction(ctx, action, errors.New("人工单在交易所受理前就取消了，没有发出"), nil)
		}
	case orderdomain.Open, orderdomain.PartiallyFilled, orderdomain.Filled,
		orderdomain.Canceling, orderdomain.CancelUnknown, orderdomain.PartiallyCanceled, orderdomain.Expired:
	default:
		return ManualOrderResult{Action: action, Order: child}, fmt.Errorf("invalid manual child state %q", child.State)
	}
	previousAction := action
	action.Status = string(operatordomain.StatusCompleted)
	action.LastError = ""
	if err := s.saveManualProgress(ctx, &action, progress); err != nil {
		return submissionIdentity(previousAction), errors.Join(callErr, err)
	}
	result, loadErr := s.loadManualOrderResult(ctx, action)
	if callErr != nil && !submissionAccountError(callErr) {
		return result, errors.Join(loadErr, callErr)
	}
	return result, loadErr
}

func (s *Service) deferManualAction(ctx context.Context, action store.OperatorActionRecord, cause error, accounts []OperatorAccountError) (ManualOrderResult, error) {
	progress, err := s.manualProgress(ctx, &action)
	if err != nil {
		return ManualOrderResult{}, err
	}
	action.Status = string(operatordomain.StatusRunning)
	action.LastError = cause.Error()
	progress.Accounts = accounts
	if err := s.saveManualProgress(ctx, &action, progress); err != nil {
		return ManualOrderResult{}, err
	}
	return s.loadManualOrderResult(ctx, action)
}

func manualOrderRequestJSON(command ManualOrderCommand) (string, error) {
	if blank(command.SpaceID) || blank(command.ActionID) || blank(command.LogicalAccountID) ||
		blank(command.TradingAccountID) || blank(command.ClientOrderID) ||
		blank(command.InstrumentID) || blank(command.Reason) ||
		(command.Type != exchange.OrderTypeMarket &&
			command.Type != exchange.OrderTypeLimit) ||
		!command.Side.Valid() ||
		command.Quantity.Cmp(shared.Zero()) <= 0 || command.DeadlineAt < 0 {
		return "", ErrInvalidCommand
	}
	var limitPrice *string
	if command.LimitPrice != nil {
		value := command.LimitPrice.String()
		limitPrice = &value
	}
	data, err := json.Marshal(manualOrderRequest{
		DeadlineAt:       command.DeadlineAt,
		LogicalAccountID: command.LogicalAccountID,
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
		DeadlineAt: request.DeadlineAt,
		SpaceID:    action.SpaceID, ActionID: action.ActionID,
		LogicalAccountID: request.LogicalAccountID,
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
	if action.Status != string(operatordomain.StatusRunning) && action.Status != string(operatordomain.StatusCompleted) && action.Status != string(operatordomain.StatusFailed) {
		return invalidResult("unsupported manual action status")
	}
	if action.ResultJSON == nil {
		if action.Status == string(operatordomain.StatusRunning) {
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
	if action.Status == string(operatordomain.StatusCompleted) && (strings.TrimSpace(result.OrderID) == "" || action.LastError != "") {
		return invalidResult("completed action requires child order and no error")
	}
	if action.Status == string(operatordomain.StatusFailed) && strings.TrimSpace(action.LastError) == "" {
		return invalidResult("failed action requires an error")
	}
	if result.OrderID == "" {
		if action.Status == string(operatordomain.StatusRunning) {
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
	if err == nil && action.Status == string(operatordomain.StatusFailed) && action.LastError != "" {
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
	previousAction := action
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	action.Status = string(operatordomain.StatusFailed)
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
		return submissionIdentity(previousAction), errors.Join(cause, err)
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
	action.UpdatedAt = s.clock().UTC()
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
		return []OperatorAccountError{{Error: err.Error(), cause: err}}
	}
	var accountErrors []OperatorAccountError
	for _, member := range members {
		var currentErrors []error
		if err := ctx.Err(); err != nil {
			accountErrors = appendAccountErrors(accountErrors, member.TradingAccountID, []error{&orderapp.AccountExecutionError{TradingAccountID: member.TradingAccountID, Operation: "manual_cancel_budget", Err: err}})
			break
		}
		if err := s.Syncer.SyncAccount(ctx, member.TradingAccountID); err != nil {
			currentErrors = append(currentErrors, fmt.Errorf("撤单前同步失败：%w", err))
			accountErrors = appendAccountErrors(accountErrors, member.TradingAccountID, currentErrors)
			continue
		}
		records, err := s.Store.ListActiveOrdersForAccount(ctx, spaceID, member.TradingAccountID)
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
					fmt.Errorf("撤单 %s 失败：%w", current.OrderID, err),
				)
			}
		}
		if ctx.Err() != nil {
			currentErrors = append(currentErrors, &orderapp.AccountExecutionError{TradingAccountID: member.TradingAccountID, Operation: "manual_cancel_budget", Err: ctx.Err()})
			accountErrors = appendAccountErrors(accountErrors, member.TradingAccountID, currentErrors)
			break
		}
		if err := s.Syncer.SyncAccount(ctx, member.TradingAccountID); err != nil {
			currentErrors = append(currentErrors, fmt.Errorf("撤单后确认同步失败：%w", err))
			accountErrors = appendAccountErrors(accountErrors, member.TradingAccountID, currentErrors)
			continue
		}
		confirmed, err := s.Store.ListActiveOrdersForAccount(ctx, spaceID, member.TradingAccountID)
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
				fmt.Errorf("%w（订单 %s）", ErrCancelUnconfirmed, latest.OrderID),
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
