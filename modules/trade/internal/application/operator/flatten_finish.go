package operator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"trpc.group/trpc-go/trpc-go/log"
)

// 清仓动作的收尾：把进度落成最终状态，先收拾本动作还没结束的子单。

// flattenFinishingTimeout 是收尾（只收拾子单、不再下单）的上限：从进入收尾算起，满了仍收拾不完就不再等。
const flattenFinishingTimeout = 30 * time.Minute

// finishFlatten 把清仓进度落成 status。动作要离开运行（完成、部分完成、失败）时先收拾本动作还没结束的子单：
// 收拾不完（子单还在解析、丢弃失败、没能列全）就先保持运行并记下要结束成什么、什么时候进入的收尾，之后每次恢复只收拾、不下单，
// 收拾完才落成 status。收尾满 flattenFinishingTimeout 仍收拾不完，或者子单收拾不了（交易所确认标的不存在），
// 照样落成 status，这些子单记进结果的 ManualCheckOrderIDs 并记关注项，要人工核对。返回实际落库的状态。
//
// 收拾子单是尽力而为：期间遇到的任何错误（解析、丢弃失败，预算用完，列子单失败）都只记在本动作上，写进 last_error，
// 不作为调用错误返回，不影响 Trade 的整体就绪；没收拾掉的子单统一受收尾上限约束。数据库真坏了时，落库进度本身会失败并返回，
// 其他工作线程也会报出来，故障照样看得见。
func (s *Service) finishFlatten(
	ctx context.Context,
	command FlattenCommand,
	members []store.LogicalAccountMemberRecord,
	result *FlattenResult,
	status string,
) (string, error) {
	note := ""
	if status != string(operatordomain.StatusRunning) {
		outcome := s.settleFlattenChildren(ctx, command, members)
		if (outcome.incomplete || len(outcome.unsettled) > 0) && result.FinishingSince == 0 {
			result.FinishingSince = s.clock().UnixMilli()
		}
		timedOut := result.FinishingSince != 0 && s.clock().Sub(time.UnixMilli(result.FinishingSince)) >= flattenFinishingTimeout
		if (outcome.incomplete || len(outcome.unsettled) > 0) && !timedOut {
			result.ManualCheckOrderIDs = outcome.manualCheck
			result.FinishingStatus = status
			running := string(operatordomain.StatusRunning)
			return running, s.persistFlattenProgressNote(ctx, result, running, outcome.errorText())
		}
		if outcome.incomplete {
			// 收尾到上限仍没能列全子单：没列出来的尽量另列一次（其中待提交的顺手丢弃，释放资金预留），一起交给人工。
			outcome.unsettled = append(outcome.unsettled, s.flattenChildrenLeft(ctx, command, members, outcome)...)
		}
		result.ManualCheckOrderIDs = append(outcome.manualCheck, outcome.unsettled...)
		if len(result.ManualCheckOrderIDs) > 0 || outcome.incomplete {
			detail := fmt.Sprintf("组合 %s 的动作 %s：%d 笔子单没收拾掉，需人工核对", command.LogicalAccountID, command.ActionID, len(result.ManualCheckOrderIDs))
			if outcome.incomplete {
				detail = fmt.Sprintf("组合 %s 的动作 %s：收尾满 30 分钟仍没能列全执行账户 %s 的子单，%d 笔子单没收拾掉，需人工核对",
					command.LogicalAccountID, command.ActionID, strings.Join(outcome.unlisted, ","), len(result.ManualCheckOrderIDs))
			}
			telemetry.Note(telemetry.AttentionFlattenOrdersUnsettled, detail, s.clock())
			log.WarnContextf(ctx, "交易关注项：%s（%s）：%s", detail, strings.Join(result.ManualCheckOrderIDs, ","), outcome.errorText())
		}
		result.FinishingStatus, result.FinishingSince = "", 0
		note = outcome.errorText()
	}
	if err := s.persistFlattenProgressNote(ctx, result, status, note); err != nil {
		return status, err
	}
	// 下架清仓结束（完成、部分完成、失败）后唤醒目标：已收敛、已阻塞的目标不被定时器扫描，
	// 不唤醒的话周期长的策略要等下一份目标才恢复（解除禁止提前结束时尤其如此）。
	if status != string(operatordomain.StatusRunning) && command.actionType() == operatordomain.ActionDelistFlatten && s.Wake != nil {
		s.Wake()
	}
	return status, nil
}

// settleLocalTimeout 是收拾子单时只读写本地库的一步（列一个账户的子单、丢弃一笔待提交子单）的时限。
// 这些步骤另起上下文（不受本次调用剩余预算影响），每一步各用各的时限。
var settleLocalTimeout = 5 * time.Second

// settleMinResolveTime 是解析一笔结果未知子单至少要分到的时间；剩余预算不够时，没轮到的留到下次恢复。
const settleMinResolveTime = 2 * time.Second

// settleFlattenChildren 在动作结束前收拾本动作还没结束的子单，不让它们在动作结束后没人管。尽力而为，错误只记进结果，分三步：
//  1. 只读库，逐个账户列出本动作的子单。某个账户列不出来时跳过它、记 incomplete（结果不全），其余账户照常；
//  2. 待提交的逐笔丢弃（记为已撤销，从没发到交易所，释放资金预留），每笔另起短时限。某个账户等账户锁超时一次，
//     它余下的待提交子单这一轮不再试（锁被占着，再等也是白等）；服务关闭（调用上下文被取消）时跳过这一步。
//     子单已经不是待提交（例如在控制台手动撤掉了）算已收拾；
//  3. 逐笔解析提交中、结果未知的子单（要问交易所），解析后退回待提交的同样丢弃。本次剩余预算平均分给还要尝试的子单，
//     每笔至少 settleMinResolveTime，放不下的留到下次；某个账户这一轮已失败过，它余下的子单这一轮不再试。
//     挂单中、撤单中的不动，它们自己会结束。
//
// unsettled 是这次没收拾掉、下次恢复还能再试的；manualCheck 是收拾不了的（交易所确认标的不存在），不阻塞收尾，要人工核对。
func (s *Service) settleFlattenChildren(ctx context.Context, command FlattenCommand, members []store.LogicalAccountMemberRecord) settleOutcome {
	var outcome settleOutcome
	var pending, unknown []store.OrderRecord
	for _, member := range members {
		records, err := s.listFlattenChildren(ctx, command, member.TradingAccountID)
		if err != nil {
			outcome.incomplete = true
			outcome.unlisted = append(outcome.unlisted, member.TradingAccountID)
			outcome.errs = append(outcome.errs, err)
			continue
		}
		for _, child := range records {
			switch orderdomain.State(child.State) {
			case orderdomain.Pending:
				pending = append(pending, child)
			case orderdomain.Submitting, orderdomain.SubmitUnknown:
				unknown = append(unknown, child)
			}
		}
	}

	lockBusy := make(map[string]struct{})
	for _, child := range pending {
		if _, busy := lockBusy[child.TradingAccountID]; busy || errors.Is(ctx.Err(), context.Canceled) {
			outcome.unsettled = append(outcome.unsettled, child.OrderID)
			continue
		}
		if errors.Is(s.discardChild(ctx, command, child, &outcome), errDiscardLockBusy) {
			lockBusy[child.TradingAccountID] = struct{}{}
		}
	}

	// 第 2 步等锁超时的账户，解析同样要等这把锁，这一轮也不再试。
	failedAccounts := make(map[string]struct{}, len(lockBusy))
	for accountID := range lockBusy {
		failedAccounts[accountID] = struct{}{}
	}
	for i, child := range unknown {
		if _, failed := failedAccounts[child.TradingAccountID]; failed {
			outcome.unsettled = append(outcome.unsettled, child.OrderID)
			continue
		}
		itemCtx, cancelItem := ctx, context.CancelFunc(func() {})
		if deadline, ok := ctx.Deadline(); ok {
			left := time.Until(deadline)
			share := max(left/time.Duration(remainingTries(unknown[i:], failedAccounts)), settleMinResolveTime)
			if left < share {
				// 剩余预算放不下一笔：没轮到的留给下次恢复，到收尾上限时交给人工。
				outcome.unsettled = append(outcome.unsettled, orderIDs(unknown[i:])...)
				outcome.errs = append(outcome.errs, fmt.Errorf("本次收拾的时间用完，还有 %d 笔子单没解析", len(unknown)-i))
				break
			}
			itemCtx, cancelItem = context.WithTimeout(ctx, share)
		}
		resolved, resolveErr := s.Orders.ResolveUnknown(itemCtx, command.SpaceID, child.OrderID)
		cancelItem()
		if resolveErr != nil {
			if childUnsettleable(resolveErr) {
				outcome.manualCheck = append(outcome.manualCheck, child.OrderID)
			} else {
				outcome.unsettled = append(outcome.unsettled, child.OrderID)
				failedAccounts[child.TradingAccountID] = struct{}{}
			}
			outcome.errs = append(outcome.errs, fmt.Errorf("解析子单 %s：%w", child.OrderID, resolveErr))
			continue
		}
		switch resolved.State {
		case orderdomain.Pending:
			_ = s.discardChild(ctx, command, child, &outcome)
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			outcome.unsettled = append(outcome.unsettled, child.OrderID)
		}
	}
	return outcome
}

// remainingTries 数 children 里所属账户这一轮还没失败过、还会去解析的子单（至少 1）。
func remainingTries(children []store.OrderRecord, failedAccounts map[string]struct{}) int {
	count := 0
	for _, child := range children {
		if _, failed := failedAccounts[child.TradingAccountID]; !failed {
			count++
		}
	}
	return max(count, 1)
}

// listFlattenChildren 只读库，列出一个账户上本动作还没结束的子单，另起短时限的上下文（不受调用方预算影响）。
func (s *Service) listFlattenChildren(ctx context.Context, command FlattenCommand, tradingAccountID string) ([]store.OrderRecord, error) {
	listCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleLocalTimeout)
	defer cancel()
	records, err := s.Store.ListActiveOrdersForAccount(listCtx, command.SpaceID, tradingAccountID)
	if err != nil {
		return nil, fmt.Errorf("列执行账户 %s 的子单：%w", tradingAccountID, err)
	}
	var children []store.OrderRecord
	for _, child := range records {
		if child.OwnerType == string(orderdomain.OwnerOperator) && child.OwnerID == command.ActionID {
			children = append(children, child)
		}
	}
	return children, nil
}

// errDiscardLockBusy 表示丢弃子单时等交易账户锁没等到。
var errDiscardLockBusy = errors.New("等交易账户锁超时")

// discardChild 丢弃一笔待提交的子单，另起短时限（要等交易账户锁）。子单已经不是待提交算已收拾；
// 其余失败记进 unsettled 和错误说明，下次再试，并返回错误（等账户锁没等到时返回 errDiscardLockBusy）。
func (s *Service) discardChild(ctx context.Context, command FlattenCommand, child store.OrderRecord, outcome *settleOutcome) error {
	discardCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleLocalTimeout)
	defer cancel()
	_, discardErr := s.Orders.DiscardPending(discardCtx, command.SpaceID, child.OrderID)
	if discardErr == nil || errors.Is(discardErr, orderdomain.ErrInvalidTransition) {
		return nil
	}
	outcome.unsettled = append(outcome.unsettled, child.OrderID)
	outcome.errs = append(outcome.errs, fmt.Errorf("丢弃子单 %s：%w", child.OrderID, discardErr))
	var accountErr *orderapp.AccountExecutionError
	if errors.As(discardErr, &accountErr) && accountErr.Operation == orderapp.OperationDiscardPendingLock {
		return errDiscardLockBusy
	}
	return discardErr
}

// orderIDs 取一组订单的单号。
func orderIDs(records []store.OrderRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.OrderID)
	}
	return ids
}

// flattenChildrenLeft 在收尾到上限、子单没能列全时，尽量另列一次本动作还没结束、又不在 outcome 里的子单，交给人工核对。
// 待提交的顺手丢弃（从没发到交易所，释放资金预留），丢弃成功的不再交给人工；列不出来的账户跳过，只写日志。
func (s *Service) flattenChildrenLeft(ctx context.Context, command FlattenCommand, members []store.LogicalAccountMemberRecord, outcome settleOutcome) []string {
	seen := make(map[string]struct{}, len(outcome.unsettled)+len(outcome.manualCheck))
	for _, id := range append(append([]string(nil), outcome.unsettled...), outcome.manualCheck...) {
		seen[id] = struct{}{}
	}
	var left []string
	for _, member := range members {
		records, err := s.listFlattenChildren(ctx, command, member.TradingAccountID)
		if err != nil {
			log.WarnContextf(ctx, "动作 %s 收尾到上限，%v", command.ActionID, err)
			continue
		}
		for _, child := range records {
			state := orderdomain.State(child.State)
			if state != orderdomain.Pending && state != orderdomain.Submitting && state != orderdomain.SubmitUnknown {
				// 挂单中、撤单中的自己会结束，平时收尾也不管它们。
				continue
			}
			if _, found := seen[child.OrderID]; found {
				continue
			}
			seen[child.OrderID] = struct{}{}
			if state == orderdomain.Pending {
				var discarded settleOutcome
				if s.discardChild(ctx, command, child, &discarded) == nil {
					continue
				}
				log.WarnContextf(ctx, "动作 %s 收尾到上限，%s", command.ActionID, discarded.errorText())
			}
			left = append(left, child.OrderID)
		}
	}
	return left
}

// settleOutcome 是一次收拾子单的结果。
type settleOutcome struct {
	unsettled   []string
	manualCheck []string
	errs        []error
	// incomplete 表示有账户的子单没列出来（结果不全），unlisted 是这些账户。
	incomplete bool
	unlisted   []string
}

// errorText 是写进动作 last_error 的收拾失败原因；没有错误时为空。
func (o settleOutcome) errorText() string {
	if len(o.errs) == 0 {
		return ""
	}
	return errors.Join(o.errs...).Error()
}

// childUnsettleable 报告子单解析失败是否属于等多久都收拾不了的：只有交易所确认标的不存在（币安 -1121、OKX 51001，标的已下架）。
// 取不到账户的执行通道（没有会话）不算：重启后会话还没建好时只是暂时的，当场放行会把本可解析的子单丢给人工；
// 它和凭证失效、超时、限流一样按收尾上限处理。
func childUnsettleable(err error) bool {
	return exchange.IsInstrumentNotFound(err)
}
