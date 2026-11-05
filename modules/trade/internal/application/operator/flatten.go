package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/exposure"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

const flattenActionTimeout = 30 * time.Minute

type FlattenCommand struct {
	SpaceID          string
	ActionID         string
	LogicalAccountID string
	// TradingAccountID 非空时只平组合里的这一个执行账户，其他成员的单和仓不动。
	TradingAccountID string
	Reason           string
}

type RemainingPosition struct {
	InstrumentID string `json:"instrument_id,omitempty"`
	Asset        string `json:"asset,omitempty"`
	Quantity     string `json:"quantity"`
	Reason       string `json:"reason"`
	// final 为真表示重试也平不掉（见 flattenReason）。
	final bool
}

// flattenReason 是一条剩余仓位的原因。final 为真表示同样的条件下重试只会得到同样的结果：
// 无可交易标的、数量不符合交易所规则、零成交就终结的子单、永久校验错误等；没列为终结的一律可重试。
type flattenReason struct {
	text  string
	final bool
}

// flattenFinal 标记终结类错误，本轮只剩这类错误和终结原因时清仓直接结束为部分完成。
type flattenFinal struct{ error }

func (e flattenFinal) Unwrap() error { return e.error }

func isFlattenFinal(err error) bool {
	_, final := err.(flattenFinal)
	return final
}

type FlattenAccountResult struct {
	TradingAccountID string              `json:"trading_account_id"`
	Status           string              `json:"status"`
	ChildOrderIDs    []string            `json:"child_order_ids,omitempty"`
	Remaining        []RemainingPosition `json:"remaining_positions,omitempty"`
	// Dust 是下不了单的现货零头，不算剩余，不阻止完成。
	Dust  []RemainingPosition `json:"dust_positions,omitempty"`
	Error string              `json:"error,omitempty"`
	cause error
	// decided 为真时由 retry 决定动作是否继续；否则按 cause 是否只有业务错误判断。
	decided bool
	retry   bool
	// quotes 是本轮现货清仓拿到的报价（按资产），收尾判断零头时复用，不再逐个请求。
	quotes map[string]shared.Decimal
}

type FlattenResult struct {
	Action   store.OperatorActionRecord `json:"-"`
	Accounts []FlattenAccountResult     `json:"accounts"`
}

type flattenRequest struct {
	LogicalAccountID string `json:"logical_account_id"`
	TradingAccountID string `json:"trading_account_id,omitempty"`
}

// ErrNotInLogicalAccount 表示执行账户不属于任何组合账户：MooX 只通过组合账户交易，这样的账户没有可平的仓。
var ErrNotInLogicalAccount = errors.New("执行账户不属于任何组合账户")

// FlattenTradingAccount 只平组合账户里的一个执行账户：撤这个账户的单、平这个账户的仓，组合账户照样暂停。
// 同组合只允许一个运行中清仓、锁的短超时、终结与可重试的分类，都和组合清仓相同。
func (s *Service) FlattenTradingAccount(ctx context.Context, command FlattenCommand) (FlattenResult, error) {
	if err := s.validate(); err != nil {
		return FlattenResult{}, err
	}
	if blank(command.TradingAccountID) {
		return FlattenResult{}, ErrInvalidCommand
	}
	logical, _, err := s.Store.FindLogicalAccountMembership(ctx, command.SpaceID, command.TradingAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return FlattenResult{}, ErrNotInLogicalAccount
	}
	if err != nil {
		return FlattenResult{}, err
	}
	command.LogicalAccountID = logical.LogicalAccountID
	return s.FlattenLogicalAccount(ctx, command)
}

// decodeFlattenRequest 从动作的请求 JSON 恢复清仓命令（工作线程恢复时用）。
func decodeFlattenRequest(action store.OperatorActionRecord) (FlattenCommand, error) {
	var request flattenRequest
	if err := json.Unmarshal([]byte(action.RequestJSON), &request); err != nil {
		return FlattenCommand{}, err
	}
	return FlattenCommand{
		SpaceID: action.SpaceID, ActionID: action.ActionID,
		LogicalAccountID: action.LogicalAccountID, TradingAccountID: request.TradingAccountID,
		Reason: action.Reason,
	}, nil
}

func (s *Service) FlattenLogicalAccount(
	ctx context.Context,
	command FlattenCommand,
) (FlattenResult, error) {
	if err := s.validate(); err != nil {
		return FlattenResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.flattenTimeout())
	defer cancel()
	requestJSON, err := flattenRequestJSON(command)
	if err != nil {
		return FlattenResult{}, err
	}
	expectedAction := store.OperatorActionRecord{
		SpaceID: command.SpaceID, ActionID: command.ActionID,
		LogicalAccountID: command.LogicalAccountID,
		ActionType:       "FLATTEN", Reason: strings.TrimSpace(command.Reason),
		RequestJSON: requestJSON, Status: "RUNNING",
	}
	existing, found, err := s.existingAction(ctx, expectedAction)
	if err != nil {
		return FlattenResult{}, err
	}
	if found && existing.Status == string(operatordomain.StatusCompleted) {
		return decodeFlattenResult(existing)
	}
	resuming := found && existing.Status == string(operatordomain.StatusRunning)
	// 同一组合只允许一个运行中的清仓，否则两个清仓会互相撤对方的子单。运行中的清仓多半正持着锁，所以锁前先查一次；
	// 请求的动作本身正在运行（工作线程用原动作号恢复）时不查，否则遗留的两个运行中动作会互相指向对方。
	if !resuming {
		if other, ok, err := s.otherRunningFlatten(ctx, command); err != nil || ok {
			return other, err
		}
	}
	lockCtx, lockCancel := context.WithTimeout(ctx, flattenLockWait)
	unlock, err := s.Store.LockLogicalAccountContext(lockCtx, command.SpaceID, command.LogicalAccountID)
	lockCancel()
	if err != nil {
		if resuming {
			// 运行中的动作由工作线程继续，调用方看到“运行中”即可。
			current, diagnosticErr := s.deferActionLock(ctx, existing, err)
			if _, deferred := diagnosticErr.(submissionDeferredError); deferred {
				diagnosticErr = nil
			}
			return FlattenResult{Action: current}, diagnosticErr
		}
		return FlattenResult{Action: existing}, ErrLogicalAccountBusy
	}
	defer unlock()
	existing, found, err = s.existingAction(ctx, expectedAction)
	if err != nil {
		return FlattenResult{Action: existing}, err
	}
	if found && existing.Status == string(operatordomain.StatusCompleted) {
		return decodeFlattenResult(existing)
	}
	var action store.OperatorActionRecord
	var other *store.OperatorActionRecord
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		// 锁内再查一次，防止两个请求同时通过锁前的检查；恢复自身动作时同样不查。
		if !resuming {
			running, err := tx.RunningOperatorActionsOfType(command.SpaceID, command.LogicalAccountID, "FLATTEN")
			if err != nil {
				return err
			}
			for index := range running {
				if running[index].ActionID != command.ActionID {
					other = &running[index]
					return nil
				}
			}
		}
		if err := tx.SetLogicalAccountAutomation(
			command.SpaceID,
			command.LogicalAccountID,
			"PAUSED",
			strings.TrimSpace(command.Reason),
		); err != nil {
			return err
		}
		var ensureErr error
		action, _, ensureErr = tx.EnsureOperatorAction(expectedAction)
		if ensureErr != nil {
			return ensureErr
		}
		if action.Status != string(operatordomain.StatusRunning) {
			action.Status = string(operatordomain.StatusRunning)
			action.LastError = ""
			return tx.UpdateOperatorAction(action)
		}
		return nil
	})
	if err != nil {
		return FlattenResult{Action: action}, err
	}
	if other != nil {
		result, _, err := runningFlattenFor(*other, command)
		return result, err
	}
	members, err := s.Store.ListLogicalAccountMembers(
		ctx, command.SpaceID, command.LogicalAccountID, true,
	)
	if err != nil {
		return FlattenResult{Action: action}, err
	}
	if target := strings.TrimSpace(command.TradingAccountID); target != "" {
		selected := members[:0]
		for _, member := range members {
			if member.TradingAccountID == target {
				selected = append(selected, member)
			}
		}
		if len(selected) == 0 {
			// 成员在发起之后被移出了组合：没有可平的，按失败结束。
			result := FlattenResult{Action: action}
			return result, errors.Join(ErrNotInLogicalAccount, s.persistFlattenProgress(ctx, &result, string(operatordomain.StatusFailed)))
		}
		members = selected
	}
	result := FlattenResult{
		Action:   action,
		Accounts: make([]FlattenAccountResult, 0, len(members)),
	}
	// 预算从收到请求算起，等锁用掉的时间要扣除，加上收尾不能超过网关的调用超时。
	budgetEnd, _ := ctx.Deadline()
	executable := 0
	var sharedErrors []error
	retry := false
	for index, member := range members {
		// 每个账户分到剩余时间的一份，前面的账户耗尽预算时后面的账户仍能执行。
		deadline := time.Now().Add(time.Until(budgetEnd) / time.Duration(len(members)-index))
		accountResult, wasExecutable := s.flattenAccount(
			ctx, command, member.TradingAccountID, deadline,
		)
		if wasExecutable {
			executable++
		}
		needsRetry := accountResult.retry
		if !accountResult.decided {
			needsRetry = accountResult.cause != nil && !submissionBusinessError(accountResult.cause)
		}
		if accountResult.cause != nil {
			if accountResult.Error == "" {
				accountResult.Error = accountResult.cause.Error()
			}
			if !allSubmissionErrors(accountResult.cause, func(err error) bool {
				return submissionAccountError(err) || submissionBusinessError(err) || isFlattenFinal(err)
			}) {
				sharedErrors = append(sharedErrors, accountResult.cause)
			}
		}
		if needsRetry {
			retry = true
			if accountResult.Status == "COMPLETED" {
				accountResult.Status = "RUNNING"
			}
		}
		result.Accounts = append(result.Accounts, accountResult)
		if err := s.persistFlattenProgress(ctx, &result, "RUNNING"); err != nil {
			return result, errors.Join(errors.Join(sharedErrors...), err)
		}
	}
	status := flattenStatus(result.Accounts, executable)
	if retry {
		status = "RUNNING"
		// 账户一直异常时清仓不能永远运行，否则恢复自动执行会被挡住。剩余持仓已写入结果，要继续就重新发起。
		if s.actionOlderThan(result.Action, flattenActionTimeout) {
			status = "PARTIAL"
		}
	}
	if err := s.persistFlattenProgress(ctx, &result, status); err != nil {
		return result, errors.Join(errors.Join(sharedErrors...), err)
	}
	return result, errors.Join(sharedErrors...)
}

// errWaitingBalance：账户在等余额确认，本轮暂停下单，属可重试。
var errWaitingBalance = errors.New("账户等待余额确认，本轮暂停下单")

// flattenLockWait 是清仓等组合账户锁的时长。锁是轮询抢占，目标执行器一整轮都持锁，太短实盘下容易报“正忙”。
const flattenLockWait = 2500 * time.Millisecond

// ErrLogicalAccountBusy 表示组合账户锁被占用，这次清仓没有执行。
var ErrLogicalAccountBusy = errors.New("组合账户正忙，稍后再发起清仓")

// otherRunningFlatten 返回同一组合里另一个运行中的清仓。
func (s *Service) otherRunningFlatten(ctx context.Context, command FlattenCommand) (FlattenResult, bool, error) {
	running, err := s.Store.ListRunningOperatorActions(ctx, command.SpaceID, command.LogicalAccountID)
	if err != nil {
		return FlattenResult{}, false, err
	}
	for _, action := range running {
		if action.ActionType == "FLATTEN" && action.ActionID != command.ActionID {
			return runningFlattenFor(action, command)
		}
	}
	return FlattenResult{}, false, nil
}

// runningFlattenFor 决定另一个运行中清仓能不能代表这次请求：整组清仓覆盖所有成员，同一账户的清仓覆盖这个账户，
// 返回它的进度；只平别的成员的清仓不覆盖这次请求，返回“组合账户正忙”，否则前端会把那次的完成当成本账户平完了。
func runningFlattenFor(other store.OperatorActionRecord, command FlattenCommand) (FlattenResult, bool, error) {
	var request flattenRequest
	_ = json.Unmarshal([]byte(other.RequestJSON), &request)
	if request.TradingAccountID == "" || request.TradingAccountID == strings.TrimSpace(command.TradingAccountID) {
		return runningFlattenResult(other), true, nil
	}
	return FlattenResult{Action: other}, true, ErrLogicalAccountBusy
}

// runningFlattenResult 返回运行中清仓已落库的进度；诊断信息不当作这次调用的错误。
func runningFlattenResult(action store.OperatorActionRecord) FlattenResult {
	result, _ := decodeFlattenResult(action)
	result.Action = action
	return result
}

func flattenRequestJSON(command FlattenCommand) (string, error) {
	if blank(command.SpaceID) || blank(command.ActionID) ||
		blank(command.LogicalAccountID) || blank(command.Reason) {
		return "", ErrInvalidCommand
	}
	data, err := json.Marshal(flattenRequest{
		LogicalAccountID: command.LogicalAccountID,
		TradingAccountID: strings.TrimSpace(command.TradingAccountID),
	})
	return string(data), err
}

func (s *Service) flattenAccount(
	ctx context.Context,
	command FlattenCommand,
	tradingAccountID string,
	deadline time.Time,
) (FlattenAccountResult, bool) {
	result := FlattenAccountResult{
		TradingAccountID: tradingAccountID,
		Status:           "RUNNING",
	}
	recoveryCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ctx = recoveryCtx
	if err := ctx.Err(); err != nil {
		result.addError(&orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_budget", Err: err})
		return result, false
	}
	if err := s.syncLight(ctx, tradingAccountID); err != nil {
		result.Status = "FAILED"
		result.Error = err.Error()
		result.cause = err
		return result, false
	}
	canceling, err := s.hasCancelableOrders(ctx, command.SpaceID, tradingAccountID, command.ActionID)
	if err != nil {
		result.Status = "FAILED"
		result.Error = err.Error()
		result.cause = err
		return result, false
	}
	if err := s.cancelAccountOrders(
		ctx,
		command.SpaceID,
		tradingAccountID,
		command.ActionID,
	); err != nil {
		result.Status = "PARTIAL"
		result.Error = err.Error()
		result.cause = err
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, nil, &result,
		)
		return result, false
	}
	if err := s.syncAfterCancel(ctx, tradingAccountID, canceling); err != nil {
		result.Status = "PARTIAL"
		result.Error = err.Error()
		result.cause = err
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, nil, &result,
		)
		return result, false
	}
	if err := s.confirmAccountCancellations(
		ctx, command.SpaceID, tradingAccountID, command.ActionID,
	); err != nil {
		result.Status = "PARTIAL"
		result.Error = err.Error()
		result.cause = err
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, nil, &result,
		)
		return result, false
	}
	if err := s.recoverFlattenChildren(ctx, command, tradingAccountID, &result); err != nil {
		result.addError(err)
		result.Remaining = s.remainingForAccount(ctx, command.SpaceID, tradingAccountID, nil, &result)
		return result, false
	}
	account, err := s.Store.GetTradingAccount(
		ctx, command.SpaceID, tradingAccountID,
	)
	if err != nil {
		result.Status = "PARTIAL"
		result.Error = err.Error()
		result.cause = err
		return result, false
	}
	attempts := s.flattenAttempts()
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_budget", Err: err})
			break
		}
		if attempt > 0 && !time.Now().Before(deadline) {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_budget", Err: context.DeadlineExceeded})
			break
		}
		if attempt > 0 {
			if err := s.waitFlattenRetry(ctx); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, &orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_wait", Err: err})
				break
			}
			if err := s.syncLight(ctx, tradingAccountID); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, err)
				break
			}
			canceling, err := s.hasCancelableOrders(ctx, command.SpaceID, tradingAccountID, command.ActionID)
			if err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, err)
				break
			}
			if err := s.cancelAccountOrders(
				ctx, command.SpaceID, tradingAccountID, command.ActionID,
			); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, err)
				break
			}
			if err := s.syncAfterCancel(ctx, tradingAccountID, canceling); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, err)
				break
			}
			if err := s.confirmAccountCancellations(
				ctx, command.SpaceID, tradingAccountID, command.ActionID,
			); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, err)
				break
			}
			account, err = s.Store.GetTradingAccount(
				ctx, command.SpaceID, tradingAccountID,
			)
			if err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, err)
				break
			}
		}
		// 每轮只看本轮的错误：前几轮留下的“子单在途”之类不能让只剩终结原因的清仓继续空转。
		result.Error = ""
		result.cause = nil
		result.Dust = nil
		result.quotes = nil
		reasons := make(map[string]flattenReason)
		switch {
		case len(account.Markets) == 0:
			result.addError(flattenFinal{errors.New("账户没有开通任何市场")})
		case !execution.InTradingSession(s.Exchanges, account.Exchange, time.Now()):
			result.addError(flattenFinal{errors.New("当前不在交易时段")})
		default:
			// 持仓来自余额的市场（现货、股票）和来自持仓表的市场（合约）各清各的。
			if account.HasBalanceMarket() {
				s.flattenSpot(ctx, command, account, &result, reasons)
			}
			if account.HasPositionMarket() {
				s.flattenSwap(ctx, command, account, &result, reasons)
			}
		}
		if ctx.Err() != nil {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_deadline", Err: ctx.Err()})
			break
		}
		if err := s.syncLight(ctx, tradingAccountID); err != nil {
			result.Error = joinError(result.Error, err.Error())
			result.cause = errors.Join(result.cause, err)
		}
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, reasons, &result,
		)
		if err := s.confirmFlattenChildren(ctx, command, tradingAccountID, &result); err != nil {
			result.addError(err)
		}
		if result.Error == "" && len(result.Remaining) == 0 {
			if submissionAccountError(result.cause) {
				result.cause = nil
			}
			result.Status = "COMPLETED"
			result.decided = true
			return result, true
		}
		if !flattenRoundRetryable(result) {
			result.Status = "PARTIAL"
			result.decided = true
			return result, true
		}
	}
	// 走到这里是预算用完或本轮遇到可重试的问题，交给下一次运行继续。
	result.Status = "PARTIAL"
	result.decided, result.retry = true, true
	return result, true
}

// flattenRoundRetryable 判断本轮结束后还值不值得重试：剩余仓位有可重试原因，或本轮有非终结的错误。
func flattenRoundRetryable(result FlattenAccountResult) bool {
	for _, remaining := range result.Remaining {
		if !remaining.final {
			return true
		}
	}
	return !allSubmissionErrors(result.cause, isFlattenFinal)
}

func (s *Service) recoverFlattenChildren(ctx context.Context, command FlattenCommand, accountID string, result *FlattenAccountResult) error {
	orders, err := s.Store.ListActiveOrdersForAccount(ctx, command.SpaceID, accountID)
	if err != nil {
		return err
	}
	for _, child := range orders {
		if child.OwnerType != string(orderdomain.OwnerOperator) || child.OwnerID != command.ActionID || orderdomain.State(child.State).Terminal() {
			continue
		}
		result.ChildOrderIDs = appendUnique(result.ChildOrderIDs, child.OrderID)
		switch orderdomain.State(child.State) {
		case orderdomain.Pending:
			_, err = s.Orders.Submit(ctx, command.SpaceID, child.OrderID)
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			_, err = s.Orders.ResolveUnknown(ctx, command.SpaceID, child.OrderID)
		case orderdomain.Canceling, orderdomain.CancelUnknown:
			_, err = s.Orders.RecoverCancel(ctx, command.SpaceID, child.OrderID)
		}
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return &orderapp.AccountExecutionError{TradingAccountID: accountID, Operation: "flatten_recovery", Err: ctx.Err()}
		}
	}
	return s.confirmFlattenChildren(ctx, command, accountID, result)
}

func (s *Service) confirmFlattenChildren(ctx context.Context, command FlattenCommand, accountID string, result *FlattenAccountResult) error {
	orders, err := s.Store.ListActiveOrdersForAccount(ctx, command.SpaceID, accountID)
	if err != nil {
		return err
	}
	for _, child := range orders {
		if child.OwnerType == string(orderdomain.OwnerOperator) && child.OwnerID == command.ActionID && !orderdomain.State(child.State).Terminal() {
			result.ChildOrderIDs = appendUnique(result.ChildOrderIDs, child.OrderID)
			return &orderapp.AccountExecutionError{TradingAccountID: accountID, Operation: "flatten_confirmation", Err: fmt.Errorf("flatten child %s remains %s", child.OrderID, child.State)}
		}
	}
	return nil
}

// hasCancelableOrders 报告清仓前是否有要撤的单：有撤单时撤后要读全部挂单确认，否则轻量同步就够。
func (s *Service) hasCancelableOrders(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
) (bool, error) {
	records, err := s.Store.ListActiveOrdersForAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return false, err
	}
	for _, current := range records {
		if orderdomain.State(current.State).Terminal() ||
			current.OwnerType == string(orderdomain.OwnerOperator) && current.OwnerID == actionID {
			continue
		}
		return true, nil
	}
	return false, nil
}

// syncAfterCancel 撤过单时读全部挂单确认；没撤单时紧接着的那次轻量同步不会带来新信息，直接跳过。
func (s *Service) syncAfterCancel(ctx context.Context, tradingAccountID string, canceled bool) error {
	if canceled {
		return s.Syncer.SyncAccount(ctx, tradingAccountID)
	}
	return nil
}

// flattenNewOrderReserve 是剩余预算不足时不再发新单的余量，留给结果落库和收尾同步。
const flattenNewOrderReserve = time.Second

// waitFlattenReady 在每笔清仓子单前等账户就绪：实盘每笔成交都会关闭准入，等轻量同步确认余额后才能下下一笔。
// 等待中的未就绪不算错误；预算不够就返回 false，结束本轮，由下一轮继续。
func (s *Service) waitFlattenReady(ctx context.Context, spaceID, tradingAccountID string) bool {
	for {
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < flattenNewOrderReserve {
			return false
		}
		account, err := s.Store.GetTradingAccount(ctx, spaceID, tradingAccountID)
		if err != nil {
			return false
		}
		if account.Ready {
			return true
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func (s *Service) cancelAccountOrders(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
) error {
	records, err := s.Store.ListActiveOrdersForAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return err
	}
	var cancelErrors []error
	for _, current := range records {
		if orderdomain.State(current.State).Terminal() ||
			current.OwnerType == string(orderdomain.OwnerOperator) &&
				current.OwnerID == actionID {
			continue
		}
		if err := s.stopOrder(ctx, current); err != nil {
			cancelErrors = append(cancelErrors, fmt.Errorf(
				"cancel order %s: %w",
				current.OrderID,
				err,
			))
		}
	}
	return errors.Join(cancelErrors...)
}

func (s *Service) confirmAccountCancellations(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
) error {
	records, err := s.Store.ListActiveOrdersForAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return err
	}
	for _, current := range records {
		if orderdomain.State(current.State).Terminal() ||
			current.OwnerType == string(orderdomain.OwnerOperator) &&
				current.OwnerID == actionID {
			continue
		}
		return fmt.Errorf(
			"%w: order %s remains %s",
			ErrCancelUnconfirmed,
			current.OrderID,
			current.State,
		)
	}
	return nil
}

func (s *Service) flattenSwap(
	ctx context.Context,
	command FlattenCommand,
	account store.TradingAccountRecord,
	result *FlattenAccountResult,
	reasons map[string]flattenReason,
) {
	positions, err := s.Store.ListPositions(
		ctx, command.SpaceID, account.TradingAccountID, exchange.SymbolKey{},
	)
	if err != nil {
		result.addError(err)
		return
	}
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		result.addError(err)
		return
	}
	bySymbol := make(map[exchange.SymbolKey]store.InstrumentRecord, len(instruments))
	for _, instrument := range instruments {
		if exposure.Tradable(instrument, account.SettlementAsset) {
			bySymbol[exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol)] = instrument
		}
	}
	for _, position := range positions {
		quantity, parseErr := shared.ParseDecimal(position.SignedQuantity)
		if parseErr != nil {
			result.addError(parseErr)
			continue
		}
		if quantity.IsZero() {
			continue
		}
		instrument, found := bySymbol[exchange.NewSymbolKey(position.MarketType, position.ExchangeSymbol)]
		if !found {
			reasons[position.ExchangeSymbol] = flattenReason{text: "持仓没有对应的可交易标的", final: true}
			continue
		}
		// 永续平仓单 reduceOnly，不查最小名义金额，只有数量不符合交易所规则才平不掉。
		if reason := quantityRuleReason(quantity.Abs(), instrument); reason != "" {
			reasons[position.ExchangeSymbol] = flattenReason{text: reason, final: true}
			continue
		}
		side := exchange.SideSell
		if quantity.IsNegative() {
			side = exchange.SideBuy
		}
		if !s.waitFlattenReady(ctx, command.SpaceID, account.TradingAccountID) {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: account.TradingAccountID, Operation: "flatten_wait_ready", Err: errWaitingBalance})
			return
		}
		if err := s.placeOrContinueFlattenChild(
			ctx, command, account, instrument, side, quantity.Abs(), result,
		); err != nil {
			reasons[position.ExchangeSymbol] = flattenReason{text: err.Error(), final: isFlattenFinal(err)}
			result.addError(err)
		}
	}
}

func (s *Service) flattenSpot(
	ctx context.Context,
	command FlattenCommand,
	account store.TradingAccountRecord,
	result *FlattenAccountResult,
	reasons map[string]flattenReason,
) {
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		result.addError(err)
		return
	}
	managed, err := s.Store.ManagedAssets(ctx, command.SpaceID, account.TradingAccountID)
	if err != nil {
		result.addError(err)
		return
	}
	byAsset := exposure.SpotInstruments(instruments, account.SettlementAsset)
	for _, balance := range account.Snapshot.Balances {
		if _, ok := managed[balance.Asset]; !ok || balance.Asset == account.SettlementAsset {
			continue
		}
		key := "asset:" + balance.Asset
		total, err := decimalOrZero(balance.Total)
		if err != nil {
			result.addError(err)
			continue
		}
		if total.Cmp(shared.Zero()) <= 0 {
			continue
		}
		instrument, found := byAsset[balance.Asset]
		if !found {
			reasons[key] = flattenReason{text: "资产没有对应结算资产的标的", final: true}
			continue
		}
		quote, quoteErr := s.Prices.LatestPrice(
			ctx, account.TradingAccountID, instrument.ExchangeSymbol,
		)
		price := shared.Zero()
		if quoteErr == nil {
			price = quote.Price
			if result.quotes == nil {
				result.quotes = make(map[string]shared.Decimal)
			}
			result.quotes[balance.Asset] = price
		}
		// 零头按总量判断，不算剩余，由剩余统计列进 Dust。
		if exposure.SpotDust(total, price, instrument) {
			continue
		}
		if quoteErr != nil {
			reasons[key] = flattenReason{text: quoteErr.Error()}
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: account.TradingAccountID, Operation: "flatten_quote", Err: quoteErr})
			continue
		}
		available, err := decimalOrZero(balance.Available)
		if err != nil {
			result.addError(err)
			continue
		}
		step, _, ruleErr := baseQuantityRules(instrument)
		if ruleErr != nil {
			reasons[key] = flattenReason{text: ruleErr.Error(), final: true}
			result.addError(flattenFinal{ruleErr})
			continue
		}
		// 总量不是零头而可用余额卖不出去，说明还有余额被挂单占着，下一轮再试。
		quantity := exposure.FloorToStep(available, step)
		if quantityRuleReason(quantity, instrument) != "" || exposure.SpotDust(quantity, price, instrument) {
			reasons[key] = flattenReason{text: "可用余额不足，部分余额仍被挂单占用"}
			continue
		}
		if !s.waitFlattenReady(ctx, command.SpaceID, account.TradingAccountID) {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: account.TradingAccountID, Operation: "flatten_wait_ready", Err: errWaitingBalance})
			return
		}
		if err := s.placeOrContinueFlattenChildWithQuote(
			ctx, command, account, instrument,
			exchange.SideSell, quantity, quote, result,
		); err != nil {
			reasons[key] = flattenReason{text: err.Error(), final: isFlattenFinal(err)}
			result.addError(err)
		}
	}
}

func (s *Service) placeOrContinueFlattenChild(
	ctx context.Context,
	command FlattenCommand,
	account store.TradingAccountRecord,
	instrument store.InstrumentRecord,
	side exchange.Side,
	quantity shared.Decimal,
	result *FlattenAccountResult,
) error {
	quote, err := s.Prices.LatestPrice(
		ctx, account.TradingAccountID, instrument.ExchangeSymbol,
	)
	if err != nil {
		return &orderapp.AccountExecutionError{TradingAccountID: account.TradingAccountID, Operation: "flatten_quote", Err: err}
	}
	return s.placeOrContinueFlattenChildWithQuote(
		ctx, command, account, instrument, side, quantity, quote, result,
	)
}

func (s *Service) placeOrContinueFlattenChildWithQuote(
	ctx context.Context,
	command FlattenCommand,
	account store.TradingAccountRecord,
	instrument store.InstrumentRecord,
	side exchange.Side,
	quantity shared.Decimal,
	quote Quote,
	result *FlattenAccountResult,
) error {
	clientOrderID := flattenClientOrderIDForSpec(
		command.ActionID,
		account.TradingAccountID,
		instrument.ExchangeSymbol,
		side,
		quantity,
	)
	existing, found, err := s.flattenChild(
		ctx, command.SpaceID, account.TradingAccountID,
		command.ActionID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol), clientOrderID,
	)
	if err != nil {
		return err
	}
	if found {
		result.ChildOrderIDs = appendUnique(
			result.ChildOrderIDs, existing.OrderID,
		)
		switch orderdomain.State(existing.State) {
		case orderdomain.Pending:
			_, err = s.Orders.Submit(ctx, command.SpaceID, existing.OrderID)
			return err
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			_, err = s.Orders.ResolveUnknown(ctx, command.SpaceID, existing.OrderID)
			return err
		case orderdomain.Canceling, orderdomain.CancelUnknown:
			_, err = s.Orders.RecoverCancel(ctx, command.SpaceID, existing.OrderID)
			return err
		case orderdomain.Rejected, orderdomain.Canceled,
			orderdomain.PartiallyCanceled, orderdomain.Expired:
			ended := manualFailureError{message: fmt.Sprintf("existing flatten child ended %s", existing.State), cause: ErrInvalidCommand}
			// 零成交就终结的子单，同一单号重试只会得到同样结果；部分成交的等持仓同步后按新数量换单号再下。
			if filled, parseErr := decimalOrZero(existing.FilledQuantity); parseErr == nil && filled.IsZero() {
				return flattenFinal{ended}
			}
			return ended
		default:
			return nil
		}
	}
	spec := orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			TradingAccountID: account.TradingAccountID,
			ClientOrderID:    clientOrderID,
			InstrumentID:     instrument.InstrumentID, Type: exchange.OrderTypeMarket,
			Side: side, Quantity: quantity,
		},
		ReferencePrice: quote.Price, ReferencePriceAt: quote.UpdatedAt,
		ReducePositionOnly: exchange.RuleFor(instrument.MarketType).ReduceOnly,
		Owner: orderdomain.OrderOwner{
			Type: orderdomain.OwnerOperator, OwnerID: command.ActionID,
			LogicalAccountID: command.LogicalAccountID,
		},
	}
	spec.PositionSide = exchange.RuleFor(instrument.MarketType).PositionSide
	placed, err := s.Orders.Place(ctx, command.SpaceID, spec)
	if err != nil {
		// 资金不足（预留不足时也返回它）和只减仓被拒可能只是持仓、余额还没同步，按可重试处理。
		if orderapp.IsPermanentValidationError(err) &&
			!errors.Is(err, orderapp.ErrInsufficientFunds) && !errors.Is(err, orderapp.ErrReduceOnly) {
			return flattenFinal{err}
		}
		return err
	}
	result.ChildOrderIDs = appendUnique(
		result.ChildOrderIDs, string(placed.ID),
	)
	_, err = s.Orders.Submit(ctx, command.SpaceID, string(placed.ID))
	return err
}

func (s *Service) flattenChild(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
	key exchange.SymbolKey,
	clientOrderID string,
) (store.OrderRecord, bool, error) {
	records, err := s.Store.ListOrdersForLane(
		ctx, spaceID, tradingAccountID, key,
	)
	if err != nil {
		return store.OrderRecord{}, false, err
	}
	var active []store.OrderRecord
	var matching *store.OrderRecord
	for _, current := range records {
		if current.OwnerType == string(orderdomain.OwnerOperator) &&
			current.OwnerID == actionID {
			if !orderdomain.State(current.State).Terminal() {
				active = append(active, current)
			}
			if current.ClientOrderID == clientOrderID {
				value := current
				matching = &value
			}
		}
	}
	if len(active) > 1 {
		return store.OrderRecord{}, false,
			fmt.Errorf("multiple active flatten children exist for %s", key)
	}
	if len(active) == 1 {
		return active[0], true, nil
	}
	if matching != nil {
		return *matching, true, nil
	}
	return store.OrderRecord{}, false, nil
}

func (s *Service) remainingForAccount(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	reasons map[string]flattenReason,
	result *FlattenAccountResult,
) []RemainingPosition {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	account, err := s.Store.GetTradingAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		result.addError(err)
		return []RemainingPosition{{
			Quantity: "unknown", Reason: err.Error(),
		}}
	}
	var remaining []RemainingPosition
	if account.HasPositionMarket() {
		positions, listErr := s.Store.ListPositions(
			ctx, spaceID, tradingAccountID, exchange.SymbolKey{},
		)
		if listErr != nil {
			result.addError(listErr)
			return []RemainingPosition{{
				Quantity: "unknown", Reason: listErr.Error(),
			}}
		}
		for _, position := range positions {
			quantity, parseErr := decimalOrZero(position.SignedQuantity)
			if parseErr != nil {
				result.addError(parseErr)
				continue
			}
			if quantity.IsZero() {
				continue
			}
			reason := reasonFor(reasons, position.ExchangeSymbol, "position remains after final sync")
			instrumentID := position.InstrumentID
			if instrumentID == "" {
				instrumentID = position.ExchangeSymbol
			}
			remaining = append(remaining, RemainingPosition{
				InstrumentID: instrumentID, Quantity: quantity.String(), Reason: reason.text, final: reason.final,
			})
		}
	}
	if account.HasBalanceMarket() {
		managed, managedErr := s.Store.ManagedAssets(ctx, spaceID, tradingAccountID)
		if managedErr != nil {
			result.addError(managedErr)
			return nil
		}
		instruments, listErr := s.Store.ListInstrumentsForAccount(ctx, tradingAccountID)
		if listErr != nil {
			result.addError(listErr)
			return nil
		}
		byAsset := exposure.SpotInstruments(instruments, account.SettlementAsset)
		result.Dust = nil
		for _, balance := range account.Snapshot.Balances {
			if _, ok := managed[balance.Asset]; !ok || balance.Asset == account.SettlementAsset {
				continue
			}
			quantity, parseErr := decimalOrZero(balance.Total)
			if parseErr != nil {
				result.addError(parseErr)
				continue
			}
			if quantity.Cmp(shared.Zero()) <= 0 {
				continue
			}
			if instrument, found := byAsset[balance.Asset]; found {
				// 只用本轮已拿到的报价；没有报价时只按最小数量判断，收尾不再发请求。
				if exposure.SpotDust(quantity, result.quotes[balance.Asset], instrument) {
					result.Dust = append(result.Dust, RemainingPosition{
						Asset: balance.Asset, Quantity: quantity.String(), Reason: "零头，低于交易所最小数量或最小金额",
					})
					continue
				}
			}
			reason := reasonFor(
				reasons, "asset:"+balance.Asset, "asset remains after final sync",
			)
			remaining = append(remaining, RemainingPosition{
				Asset: balance.Asset, Quantity: quantity.String(), Reason: reason.text, final: reason.final,
			})
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		return remaining[i].InstrumentID+remaining[i].Asset <
			remaining[j].InstrumentID+remaining[j].Asset
	})
	return remaining
}

func (r *FlattenAccountResult) addError(err error) {
	r.Error = joinError(r.Error, err.Error())
	r.cause = errors.Join(r.cause, err)
}

func (s *Service) persistFlattenProgress(
	ctx context.Context,
	result *FlattenResult,
	status string,
) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	data, err := json.Marshal(struct {
		Accounts []FlattenAccountResult `json:"accounts"`
	}{Accounts: result.Accounts})
	if err != nil {
		return err
	}
	raw := string(data)
	action := result.Action
	action.Status = status
	action.ResultJSON = &raw
	action.LastError = ""
	for _, account := range result.Accounts {
		if account.Error != "" {
			action.LastError = joinError(
				action.LastError,
				account.TradingAccountID+": "+account.Error,
			)
		}
	}
	if err := s.updateAction(ctx, action); err != nil {
		return err
	}
	result.Action = action
	return nil
}

func decodeFlattenResult(action store.OperatorActionRecord) (FlattenResult, error) {
	result := FlattenResult{Action: action}
	if action.ResultJSON == nil {
		if action.LastError == "" {
			return result, nil
		}
		return result, errors.New(action.LastError)
	}
	var persisted struct {
		Accounts []FlattenAccountResult `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(*action.ResultJSON), &persisted); err != nil {
		return result, err
	}
	result.Accounts = persisted.Accounts
	return result, nil
}

func flattenStatus(accounts []FlattenAccountResult, executable int) string {
	if executable == 0 {
		return string(operatordomain.StatusFailed)
	}
	for _, account := range accounts {
		if account.Status != "COMPLETED" {
			return string(operatordomain.StatusPartial)
		}
	}
	return string(operatordomain.StatusCompleted)
}

func flattenClientOrderIDForSpec(
	actionID string,
	accountID string,
	symbol string,
	side exchange.Side,
	quantity shared.Decimal,
) string {
	sum := sha256.Sum256([]byte(
		actionID + "\x00" + accountID + "\x00" + symbol + "\x00" +
			string(side) + "\x00" + quantity.String(),
	))
	return "mf" + hex.EncodeToString(sum[:15])
}

func (s *Service) flattenAttempts() int {
	if s.FlattenMaxAttempts > 0 {
		return s.FlattenMaxAttempts
	}
	return int(^uint(0) >> 1)
}

func (s *Service) waitFlattenRetry(ctx context.Context) error {
	interval := s.FlattenRetryInterval
	if interval < 0 {
		return nil
	}
	if interval == 0 {
		interval = 200 * time.Millisecond
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) flattenTimeout() time.Duration {
	if s.FlattenTimeout > 0 {
		return s.FlattenTimeout
	}
	// 要小于网关给清仓接口的 12 秒调用超时（服务目录 method_timeouts_ms），未完成的部分由工作线程继续。
	return 8 * time.Second
}

func quantityRuleReason(
	quantity shared.Decimal,
	instrument store.InstrumentRecord,
) string {
	step, minimum, err := baseQuantityRules(instrument)
	if err != nil {
		return err.Error()
	}
	if quantity.Cmp(shared.Zero()) <= 0 ||
		quantity.Cmp(minimum) < 0 ||
		!quantity.Div(step).IsInteger() {
		return "数量低于最小值或不符合步长"
	}
	return ""
}

func baseQuantityRules(
	instrument store.InstrumentRecord,
) (shared.Decimal, shared.Decimal, error) {
	step, err := shared.ParseDecimal(instrument.ExchangeQuantityStep)
	if err != nil || step.Cmp(shared.Zero()) <= 0 {
		return shared.Decimal{}, shared.Decimal{},
			errors.New("invalid exchange quantity step")
	}
	minimum, err := decimalOrZero(instrument.MinExchangeQuantity)
	if err != nil || minimum.IsNegative() {
		return shared.Decimal{}, shared.Decimal{},
			errors.New("invalid minimum exchange quantity")
	}
	if exchange.RuleFor(instrument.MarketType).ContractValue {
		contractValue, parseErr := shared.ParseDecimal(instrument.ContractValue)
		if parseErr != nil || contractValue.Cmp(shared.Zero()) <= 0 {
			return shared.Decimal{}, shared.Decimal{},
				errors.New("invalid SWAP contract value")
		}
		step = step.Mul(contractValue)
		minimum = minimum.Mul(contractValue)
	}
	return step, minimum, nil
}

func decimalOrZero(raw string) (shared.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return shared.Zero(), nil
	}
	return shared.ParseDecimal(raw)
}

func reasonFor(values map[string]flattenReason, key, fallback string) flattenReason {
	if reason := values[key]; reason.text != "" {
		return reason
	}
	return flattenReason{text: fallback}
}

func joinError(current, next string) string {
	if current == "" {
		return next
	}
	if next == "" {
		return current
	}
	return current + "; " + next
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}
