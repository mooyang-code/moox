package operator

import (
	"context"
	"errors"
	"fmt"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// 单个执行账户的清仓：整理账户、撤单、按轮下单、账户级的重试与等待。

func (s *Service) flattenAccount(
	ctx context.Context,
	command FlattenCommand,
	scope flattenScope,
	tradingAccountID string,
	deadline time.Time,
) (FlattenAccountResult, bool) {
	result := FlattenAccountResult{
		TradingAccountID: tradingAccountID,
		Status:           string(operatordomain.StatusRunning),
	}
	recoveryCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ctx = recoveryCtx
	if err := ctx.Err(); err != nil {
		result.addError(&orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_budget", Err: err})
		return result, false
	}
	// 资金设置每个账户每轮只读一次，平仓、剩余统计、减仓目标共用：读不出来（排除字段坏了、数据库忙）
	// 就不下单，不能悄悄按默认设置把底仓的对冲腿平掉。
	settings, err := s.fundSettingsOf(ctx, command.SpaceID, tradingAccountID)
	if err != nil {
		result.Status = string(operatordomain.StatusFailed)
		result.Error = err.Error()
		result.cause = err
		if errors.Is(err, store.ErrFundSettingsUnreadable) {
			// 设置坏了重试也一样：直接以失败结束，不让动作一直 RUNNING、operator 每秒重试 30 分钟。
			result.cause = flattenFinal{err}
			result.decided, result.retry = true, false
		}
		return result, false
	}
	scope.settings = settings
	if s.stopIfUnblocked(ctx, command, scope, tradingAccountID, &result) {
		return result, true
	}
	if beforeCancel, err := s.syncAndCancel(ctx, command, scope, tradingAccountID); err != nil {
		// 同步、查可撤单失败时还没动过单，记失败；撤单、确认失败时记部分完成并列出剩余。
		result.Status = string(operatordomain.StatusPartial)
		if beforeCancel {
			result.Status = string(operatordomain.StatusFailed)
		}
		result.Error = err.Error()
		result.cause = err
		if !beforeCancel {
			result.Remaining = s.remainingForAccount(
				ctx, command.SpaceID, tradingAccountID, nil, &result, scope,
			)
		}
		return result, false
	}
	if err := s.recoverFlattenChildren(ctx, command, tradingAccountID, &result); err != nil {
		result.addError(err)
		result.Remaining = s.remainingForAccount(ctx, command.SpaceID, tradingAccountID, nil, &result, scope)
		return result, false
	}
	account, err := s.Store.GetTradingAccount(
		ctx, command.SpaceID, tradingAccountID,
	)
	if err != nil {
		result.Status = string(operatordomain.StatusPartial)
		result.Error = err.Error()
		result.cause = err
		return result, false
	}
	if scope.reducing && scope.ensureTargets != nil {
		if err := scope.ensureTargets(ctx, tradingAccountID, scope.settings); err != nil {
			result.addError(err)
			result.Status = string(operatordomain.StatusPartial)
			result.Remaining = s.remainingForAccount(ctx, command.SpaceID, tradingAccountID, nil, &result, scope)
			return result, false
		}
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
		// 剩下的预算连发一笔单的余量都不够：这一轮到此为止，下一次调用接着来（不空转同步）。
		if attempt > 0 && time.Until(deadline) < flattenNewOrderReserve {
			break
		}
		// 上一笔 IOC 刚结束、两笔之间的间隔没到：直接睡到间隔结束再往下做，中间不同步。睡醒后还要等一个轮间等待、留一个发单余量，
		// 这些加起来超过本账户的预算就不睡了，结束这一轮。必须先睡再查名单：睡眠期间解除禁止，睡醒后不能再下一笔。
		if until := result.pacingUntil; attempt > 0 && !until.IsZero() {
			result.pacingUntil = time.Time{}
			if until.Add(s.retryWait()).Add(flattenNewOrderReserve).After(deadline) {
				break
			}
			if err := sleepUntil(ctx, until); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, &orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_wait", Err: err})
				break
			}
		}
		if attempt > 0 && s.stopIfUnblocked(ctx, command, scope, tradingAccountID, &result) {
			return result, true
		}
		if attempt > 0 {
			if err := s.waitFlattenRetry(ctx); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, &orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_wait", Err: err})
				break
			}
			if _, err := s.syncAndCancel(ctx, command, scope, tradingAccountID); err != nil {
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
		result.pacedOnly = false
		reasons := make(map[string]flattenReason)
		canOrder, sessionReason := execution.CanOrder(s.Exchanges, account.Exchange, time.Now())
		switch {
		case len(account.Markets) == 0:
			result.addError(flattenFinal{errors.New("账户没有开通任何市场")})
		case !canOrder:
			result.addError(flattenFinal{errors.New(sessionReason)})
		default:
			// 持仓来自余额的市场（现货、股票）和来自持仓表的市场（合约）各清各的。
			if account.HasBalanceMarket() {
				s.flattenSpot(ctx, command, scope, account, &result, reasons)
			}
			if account.HasPositionMarket() {
				s.flattenSwap(ctx, command, scope, account, &result, reasons)
			}
		}
		if ctx.Err() != nil {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_deadline", Err: ctx.Err()})
			break
		}
		// 只被“IOC 间隔未到”挡住的这一轮什么都没做，不做收尾同步（下一轮睡醒后再同步）。
		if !result.pacedOnly {
			if err := s.syncLight(ctx, tradingAccountID); err != nil {
				result.Error = joinError(result.Error, err.Error())
				result.cause = errors.Join(result.cause, err)
			}
		}
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, reasons, &result, scope,
		)
		if err := s.confirmFlattenChildren(ctx, command, tradingAccountID, &result); err != nil {
			result.addError(err)
		}
		if result.Error == "" && len(result.Remaining) == 0 {
			if submissionAccountError(result.cause) {
				result.cause = nil
			}
			result.Status = string(operatordomain.StatusCompleted)
			result.decided = true
			return result, true
		}
		if !flattenRoundRetryable(result) {
			result.Status = string(operatordomain.StatusPartial)
			result.decided = true
			return result, true
		}
	}
	// 走到这里是预算用完或本轮遇到可重试的问题，交给下一次运行继续。
	result.Status = string(operatordomain.StatusPartial)
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
			// 过了下架时间，待提交的子单不再发出，直接丢弃。
			if s.pastDeadline(command) {
				_, err = s.Orders.DiscardPending(ctx, command.SpaceID, child.OrderID)
				break
			}
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

// syncAndCancel 清仓前整理账户：轻量同步、撤掉范围内不是本动作的挂单、撤过单则读全部挂单确认、确认撤干净。
// beforeCancel 为真表示失败发生在撤单之前（同步、查可撤单失败），还没动过单。
func (s *Service) syncAndCancel(
	ctx context.Context,
	command FlattenCommand,
	scope flattenScope,
	tradingAccountID string,
) (beforeCancel bool, err error) {
	if err := s.syncLight(ctx, tradingAccountID); err != nil {
		return true, err
	}
	canceling, err := s.hasCancelableOrders(ctx, command.SpaceID, tradingAccountID, command.ActionID, scope)
	if err != nil {
		return true, err
	}
	if err := s.cancelAccountOrders(ctx, command.SpaceID, tradingAccountID, command.ActionID, scope); err != nil {
		return false, err
	}
	if err := s.syncAfterCancel(ctx, tradingAccountID, canceling); err != nil {
		return false, err
	}
	return false, s.confirmAccountCancellations(ctx, command.SpaceID, tradingAccountID, command.ActionID, scope)
}

// cancelableOrders 列出账户上清仓前要撤的单：范围内、还没结束、不是本动作自己的子单。
// 清仓前是否有单要撤、撤单、撤后确认三处共用这一个过滤条件。
func (s *Service) cancelableOrders(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
	scope flattenScope,
) ([]store.OrderRecord, error) {
	records, err := s.Store.ListActiveOrdersForAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return nil, err
	}
	var cancelable []store.OrderRecord
	for _, current := range records {
		if orderdomain.State(current.State).Terminal() || !scope.orderInScope(current) ||
			current.OwnerType == string(orderdomain.OwnerOperator) && current.OwnerID == actionID {
			continue
		}
		cancelable = append(cancelable, current)
	}
	return cancelable, nil
}

// hasCancelableOrders 报告清仓前是否有要撤的单：有撤单时撤后要读全部挂单确认，否则轻量同步就够。
func (s *Service) hasCancelableOrders(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
	scope flattenScope,
) (bool, error) {
	orders, err := s.cancelableOrders(ctx, spaceID, tradingAccountID, actionID, scope)
	return len(orders) > 0, err
}

// syncAfterCancel 撤过单时读全部挂单确认；没撤单时紧接着的那次轻量同步不会带来新信息，直接跳过。
func (s *Service) syncAfterCancel(ctx context.Context, tradingAccountID string, canceled bool) error {
	if canceled {
		return s.Syncer.SyncAccount(ctx, tradingAccountID)
	}
	return nil
}

func (s *Service) cancelAccountOrders(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
	scope flattenScope,
) error {
	records, err := s.cancelableOrders(ctx, spaceID, tradingAccountID, actionID, scope)
	if err != nil {
		return err
	}
	var cancelErrors []error
	for _, current := range records {
		err := s.stopOrder(ctx, current)
		if exchange.IsInstrumentNotFound(err) {
			// 交易所确认标的不存在（已下架）：这笔单撤不掉也查不到（多半是之前动作留下的结果未知子单）。不算撤单失败，
			// 撤单后的同步（syncAfterCancel，此时一定有要撤的单）会把它按过期结束并记关注项，随后的撤单确认就过了。
			continue
		}
		if err != nil {
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
	scope flattenScope,
) error {
	records, err := s.cancelableOrders(ctx, spaceID, tradingAccountID, actionID, scope)
	if err != nil || len(records) == 0 {
		return err
	}
	return fmt.Errorf(
		"%w: order %s remains %s",
		ErrCancelUnconfirmed,
		records[0].OrderID,
		records[0].State,
	)
}

func (s *Service) flattenAttempts() int {
	if s.FlattenMaxAttempts > 0 {
		return s.FlattenMaxAttempts
	}
	return int(^uint(0) >> 1)
}

// retryWait 是清仓一轮和下一轮之间的等待时间（负数表示不等，零用默认 200 毫秒）。
func (s *Service) retryWait() time.Duration {
	switch {
	case s.FlattenRetryInterval < 0:
		return 0
	case s.FlattenRetryInterval == 0:
		return 200 * time.Millisecond
	}
	return s.FlattenRetryInterval
}

func (s *Service) waitFlattenRetry(ctx context.Context) error {
	interval := s.retryWait()
	if interval <= 0 {
		return nil
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

// sleepUntil 睡到指定时间，ctx 结束时提前返回。
func sleepUntil(ctx context.Context, until time.Time) error {
	wait := time.Until(until)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
