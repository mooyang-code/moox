package target

import (
	"context"
	"errors"
	"fmt"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/application/wallet"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// 本文件是 Converge 的各个步骤：撤旧单与冲突检查、处理本目标在途子单、逐标的算动作、执行阶段、收尾。

// stageRun 是一轮收敛里执行阶段累积的状态。
type stageRun struct {
	// blocked 是本轮定下的阻塞原因，写进目标的 blocked_targets，非空时目标定为 BLOCKED。
	blocked []store.BlockedTarget
	// deferredItems 是被每小时上限、市场规则或划转顺延的标的：写进 blocked_targets 让界面看得到原因，但不算阻塞——
	// 全是顺延时目标定为 CONVERGED（计划 4.4），不触发“目标持续阻塞”告警；有真实阻塞时真实原因排在前面。
	deferredItems []store.BlockedTarget
	// placedIDs 是本轮已落库、等着统一提交的子单。
	placedIDs []string
	// deferred 表示批额度用完，剩下的标的留到下一批。
	deferred bool
	// capDeferred 表示有标的被单币每小时上限顺延到下一个整点。
	capDeferred bool
	// marketDeferred 表示有标的按市场规则顺延（停牌、涨跌停、T+1）：定时器不扫已收敛的目标，要自己定时唤醒再看一次。
	marketDeferred bool
	// transferWaiting 表示有执行账户的现货买单在等划转（划转、对账、等快照、失败后退避）：它们这轮不下单，
	// 其余账户照常下单；只要还有账户在等，目标不能定终态，也不进入归集。
	transferWaiting bool
	transferRetry   time.Duration
	// openBuyAccounts 是本目标里还有现货买单在等这笔资金的执行账户（顺延到下一个整点、等划转、因合约钱包可划转资金不足被缩小或挡下）：
	// 这些账户不归集，否则刚划进现货钱包的资金又被划回合约钱包，剩下的买单再也买不了。
	// 因为被拒单、反向次数超限等非资金原因被挡的买单不算：资金用不上，应该照常归集。
	openBuyAccounts map[string]struct{}
}

// holdCollect 记下现货买单（阶段 5）里还在等资金的账户。
func (run *stageRun) holdCollect(stage int, action laneAction) {
	if stage == stageSpotBuy {
		run.openBuyAccounts[action.member.account.TradingAccountID] = struct{}{}
	}
}

// stopWhenInactive 处理组合已暂停（自动执行不是 ACTIVE）：撤掉本目标留下的一笔在途单，原因写进目标；没有在途单就只报暂停。
func (e *Executor) stopWhenInactive(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	logicalAccount store.LogicalAccountRecord,
	target *store.LogicalAccountTargetRecord,
) (Result, error) {
	orders, _, listErr := e.Store.ListOrders(
		ctx,
		spaceID,
		store.OrderQuery{
			LogicalAccountID: logicalAccountID,
			OnlyOpen:         true,
			Limit:            1000,
		},
	)
	if listErr != nil {
		return Result{}, listErr
	}
	for _, current := range orders {
		if current.OwnerType != string(orderdomain.OwnerTarget) {
			continue
		}
		if err := e.stopOrder(ctx, *target, current); err != nil {
			return Result{}, err
		}
		target.LastError = logicalAccount.PauseReason
		if err := e.updateTarget(ctx, target, StatusPending); err != nil {
			return Result{}, err
		}
		return Result{Status: StatusPaused, Action: "stop"}, nil
	}
	return Result{Status: StatusPaused}, nil
}

// releaseConflictsAndStale 处理成员账户上的在途单：来历说不清的单（外部单、操作单、别的组合或别的实例的目标单）暂停组合；
// 上一份目标留下的单按第 1 阶段处理。handled 为真表示本轮到此为止，直接返回 result。
func (e *Executor) releaseConflictsAndStale(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	logicalAccount store.LogicalAccountRecord,
	target *store.LogicalAccountTargetRecord,
	orders []store.OrderRecord,
	flattens delistFlattens,
) (Result, bool, error) {
	flattenChildren := flattenChildCache{}
	hasStale := false
	for _, current := range orders {
		if current.OwnerType == string(orderdomain.OwnerOperator) {
			// 清仓一族（下架清仓、整组清仓、减仓）的子单不算冲突，不管动作是否还在运行：运行中的下架清仓执行器不碰那个标的
			// （loadMembers 里已跳过）；结束了的动作已经收拾过待提交、结果未知的子单，剩下的挂单中、撤单中的单会自己结束。
			exempt, err := e.flattenChild(ctx, spaceID, current.OwnerID, flattens, flattenChildren)
			if err != nil {
				return Result{}, false, err
			}
			if exempt {
				continue
			}
		}
		switch {
		case current.OwnerType == string(orderdomain.OwnerExternal),
			current.OwnerType == string(orderdomain.OwnerOperator),
			// 别的组合的目标单出现在成员账户上，或者本目标号却不是本实例下的：说不清来历，暂停。
			current.OwnerType == string(orderdomain.OwnerTarget) &&
				(current.LogicalAccountID != logicalAccountID ||
					current.OwnerID == target.TargetID && current.RunnerID != target.RunnerID):
			reason := fmt.Sprintf(
				"%s 订单 %s 与自动执行冲突",
				current.OwnerType, current.OrderID,
			)
			if err := e.pauseLogicalAccount(ctx, logicalAccount, *target, reason); err != nil {
				return Result{}, false, err
			}
			return Result{Status: StatusPaused, Action: "pause"}, true, nil
		case current.OwnerType == string(orderdomain.OwnerTarget) &&
			current.OwnerID != target.TargetID:
			hasStale = true
		}
	}
	// 上一份目标（包括上一个策略实例、上一个会话）留下的单按第 1 阶段处理，一轮内处理完：挂单批量撤，待提交的丢弃，
	// 结果未知的先解析，撤单结果未知的先确认；市价单等它自己结束。当前目标自己的单不动。
	if hasStale {
		if err := e.checkTargetExecutable(ctx, logicalAccount, *target); err != nil {
			return Result{}, false, err
		}
		if err := e.releaseTargetOrders(ctx, spaceID, logicalAccountID, target.TargetID); err != nil {
			return Result{}, false, err
		}
		if err := e.updateTarget(ctx, target, StatusConverging); err != nil {
			return Result{}, false, err
		}
		// 还有没结束的旧单（市价单、撤单中）时过一会儿再看，不等定时器。
		return Result{Status: StatusConverging, Action: "stop", RetryAfter: transferPollInterval}, true, nil
	}
	return Result{}, false, nil
}

// stopForBlockedExposure 处理成员账户有阻塞敞口（不能交易的持仓）：撤掉本目标的在途单后定为 BLOCKED。
func (e *Executor) stopForBlockedExposure(
	ctx context.Context,
	target *store.LogicalAccountTargetRecord,
	orders []store.OrderRecord,
	blocked []store.BlockedTarget,
) (Result, error) {
	for _, current := range orders {
		if current.OwnerType != string(orderdomain.OwnerTarget) ||
			current.OwnerID != target.TargetID {
			continue
		}
		if err := e.stopOrder(ctx, *target, current); err != nil {
			return Result{}, err
		}
		target.BlockedTargets = blocked
		target.LastError = ""
		if err := e.updateTarget(ctx, target, StatusBlocked); err != nil {
			return Result{}, err
		}
		return Result{Status: StatusBlocked, Action: "stop"}, nil
	}
	target.BlockedTargets = blocked
	target.LastError = ""
	if err := e.updateTarget(ctx, target, StatusBlocked); err != nil {
		return Result{}, err
	}
	return Result{Status: StatusBlocked}, nil
}

// handleTargetChildren 按状态处理本目标自己的在途子单：待提交的交给 handlePendingChild，提交结果未知的批量解析，
// 超过寿命的挂单撤掉。有在途子单时本轮只处理这些单，不发新批（handled 为真，直接返回 result）。
// 没有在途子单时返回本目标簿上的挂单（resting），后面从差额里扣。
func (e *Executor) handleTargetChildren(
	ctx context.Context,
	spaceID string,
	logicalAccount store.LogicalAccountRecord,
	target *store.LogicalAccountTargetRecord,
	orders []store.OrderRecord,
	desired map[string]desiredTarget,
	members []memberState,
	env *laneEnv,
	orderSettings ordersettings.Settings,
) (map[string]*restingLane, Result, bool, error) {
	inFlight := false
	// resting 是本目标簿上有限价 GTC 挂单的标的：挂单不阻塞其他标的，剩余量从这个标的的差额里扣除。
	resting := make(map[string]*restingLane)
	var expiredResting []store.OrderRecord
	var unknown []string
	for _, current := range orders {
		if current.OwnerType != string(orderdomain.OwnerTarget) ||
			current.OwnerID != target.TargetID {
			continue
		}
		switch orderdomain.State(current.State) {
		case orderdomain.Pending:
			result, handled, pendingErr := e.handlePendingChild(ctx, logicalAccount, target, current, desired, members, env)
			if handled {
				return nil, result, true, pendingErr
			}
			if pendingErr != nil {
				return nil, Result{}, false, pendingErr
			}
			inFlight = inFlight || result.Action == "submit"
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			unknown = append(unknown, current.OrderID)
			inFlight = true
		default:
			if restingOrder(current) {
				lane := resting[current.InstrumentID]
				if lane == nil {
					lane = &restingLane{signedRemaining: shared.Zero()}
					resting[current.InstrumentID] = lane
				}
				lane.add(current)
				if expires := restingExpiresAt(current, orderSettings); !expires.IsZero() && !e.now().Before(expires) {
					expiredResting = append(expiredResting, current)
				}
			} else {
				inFlight = true
			}
		}
	}
	if len(unknown) > 0 {
		// 全部未知结果一次解析完，结束后只做一次轻量同步。
		if _, err := e.Orders.ResolveUnknownBatch(ctx, spaceID, unknown); err != nil {
			return nil, Result{}, false, err
		}
	}
	// 超过挂单寿命的挂单撤掉，等撤单结束后剩余量重新计入差额。
	for _, current := range expiredResting {
		if err := e.stopOrder(ctx, *target, current); err != nil {
			return nil, Result{}, false, err
		}
		inFlight = true
	}
	if inFlight {
		if err := e.updateTarget(ctx, target, StatusConverging); err != nil {
			return nil, Result{}, false, err
		}
		return nil, Result{Status: StatusConverging, Action: "wait"}, true, nil
	}
	return resting, Result{}, false, nil
}

// planActions 逐标的算出这一轮要下的动作：挂单的剩余量从差额里扣掉，反向或超出差额的挂单撤掉；按预算挑出改成挂单的标的；
// 批间隔未到、接口限流暂停时本轮不发新批。early 非空表示本轮到此为止，直接返回 *early。
// blocked 是逐标的算出来的阻塞原因，后面的执行阶段在它上面继续追加。
func (e *Executor) planActions(
	ctx context.Context,
	spaceID string,
	logicalAccount store.LogicalAccountRecord,
	target *store.LogicalAccountTargetRecord,
	members []memberState,
	desired map[string]desiredTarget,
	env *laneEnv,
	resting map[string]*restingLane,
	orderSettings ordersettings.Settings,
) (actions []laneAction, blocked []store.BlockedTarget, early *Result, err error) {
	for _, instrumentID := range laneIDs(desired, members) {
		action, complete, reason, actionErr := e.laneAction(
			env, instrumentID, desired[instrumentID], members,
		)
		if actionErr != nil {
			return nil, nil, nil, actionErr
		}
		if reason != "" {
			blocked = append(blocked, store.BlockedTarget{
				InstrumentID: instrumentID,
				Quantity:     desired[instrumentID].Quantity.String(),
				Reason:       reason,
			})
			continue
		}
		if !complete {
			actions = append(actions, action)
		}
	}
	// 挂单的剩余量从差额里扣掉；反向或超出差额的挂单（买多、卖多的风险）撤掉，下一轮再按最新状态重算。
	var staleResting []store.OrderRecord
	actions, staleResting = reconcileResting(actions, resting, blocked)
	if len(staleResting) > 0 {
		for _, current := range staleResting {
			if err := e.stopOrder(ctx, *target, current); err != nil {
				return nil, nil, nil, err
			}
		}
		target.BlockedTargets = blocked
		if err := e.updateTarget(ctx, target, StatusConverging); err != nil {
			return nil, nil, nil, err
		}
		early := Result{Status: StatusConverging, Action: "wait"}
		return nil, nil, &early, nil
	}
	// 按方向的金额预算挑出这一轮改成挂单的标的。
	if !orderSettings.RestingRatio.IsZero() && len(actions) > 0 {
		// 本目标在某个标的上挂过单、挂单已经结束（到期被撤、超出差额被撤）：这个标的不再挂单，改吃单。
		rested, err := e.Store.TargetEndedRestingInstruments(ctx, spaceID, logicalAccount.LogicalAccountID, target.TargetID)
		if err != nil {
			return nil, nil, nil, err
		}
		for index := range actions {
			_, actions[index].rested = rested[actions[index].instrument.InstrumentID]
		}
		picked := pickResting(env, *target, actions, desired, members, e.restingNotional(ctx))
		for index := range actions {
			_, actions[index].resting = picked[actions[index].instrument.InstrumentID]
		}
	}
	if len(actions) > 0 {
		if allowed, retryAfter := e.batchAllowed(logicalAccount.LogicalAccountID, members, env.settings); !allowed {
			// 批间隔未到，或交易所接口限流暂停、权重用到 90%：本轮不发新批，也不算阻塞。
			target.BlockedTargets = blocked
			if err := e.updateTarget(ctx, target, StatusConverging); err != nil {
				return nil, nil, nil, err
			}
			pace := Result{Status: StatusConverging, Action: "pace", RetryAfter: retryAfter}
			return nil, nil, &pace, nil
		}
	}
	return actions, blocked, nil, nil
}

// prepareSpotBuys 是第 5 阶段（现货买入）下单前的准备：
// 缩小前的原始差额就够不上最小下单额（或交易规则下下不了单且已有同向持仓）的买单视为已满足；因为非资金原因注定下不了的买单
// 直接记 BLOCKED：都不下单、不计入划转需求，否则会为不会下的单划转资金，或把本来已满足的差额判成 BLOCKED。
// 之后普通账户把资金划进现货钱包（阶段 4），合约钱包可划额度不够时现货买入按比例缩小。返回这一轮真正要下的买单。
func (e *Executor) prepareSpotBuys(
	ctx context.Context,
	env *laneEnv,
	spaceID string,
	target store.LogicalAccountTargetRecord,
	stageActions []laneAction,
	desired map[string]desiredTarget,
	members []memberState,
	run *stageRun,
) ([]laneAction, error) {
	const stage = stageSpotBuy
	needed := stageActions[:0:0]
	for _, action := range stageActions {
		if needsWalletTransfer(action.member.account) {
			verdict, err := e.spotBuyPrecheck(ctx, env, spaceID, target, action, desired, members)
			if err != nil {
				return nil, err
			}
			switch {
			case verdict.satisfied:
				continue
			case verdict.noTransfer:
				action.noTransfer = true
			case verdict.capDeferred:
				// 每小时上限已用完：顺延到下一个整点，不为它划转；账户的资金到整点还要用，不归集。
				run.capDeferred = true
				run.deferredItems = append(run.deferredItems, store.BlockedTarget{
					InstrumentID: action.instrument.InstrumentID, Quantity: action.delta.String(), Reason: reasonHourlyCapDeferred, Deferred: true,
				})
				run.holdCollect(stage, action)
				continue
			case verdict.blockedReason != "":
				run.blocked = append(run.blocked, store.BlockedTarget{
					InstrumentID: action.instrument.InstrumentID, Quantity: action.delta.String(), Reason: verdict.blockedReason,
				})
				continue
			}
		}
		needed = append(needed, action)
	}
	stageActions = needed
	// 阶段 4：普通账户把资金划进现货钱包，划转完成后才买入。统一账户、OKX、模拟盘没有这一步。
	outcome, err := e.walletTransferBeforeSpotBuy(ctx, spaceID, target, stageActions)
	if err != nil {
		return nil, err
	}
	// 合约钱包可划额度不够时，现货买入按比例缩小，剩下的留到下一份目标重算。
	scaledActions := scaleBuys(stageActions, outcome.BuyScale, outcome.Reasons)
	for _, action := range stageActions {
		accountID := action.member.account.TradingAccountID
		if reason, waiting := outcome.Waiting[accountID]; waiting {
			// 这个账户在划转、对账、等快照或失败后退避：它的买单这轮不下，不算阻塞；同组合其他账户照常下单。
			run.transferWaiting = true
			run.openBuyAccounts[accountID] = struct{}{}
			if reason != "" {
				run.deferredItems = append(run.deferredItems, store.BlockedTarget{
					InstrumentID: action.instrument.InstrumentID, Quantity: action.blockedQuantity(), Reason: reason, Deferred: true,
				})
			}
			continue
		}
		kept := false
		for _, scaled := range scaledActions {
			kept = kept || (scaled.instrument.InstrumentID == action.instrument.InstrumentID &&
				scaled.member.account.TradingAccountID == accountID)
		}
		if !kept {
			reason := outcome.Reasons[accountID]
			if reason == "" {
				reason = wallet.ReasonShortfallNoBuy
			}
			if wallet.IsFundsShortage(reason) {
				run.openBuyAccounts[accountID] = struct{}{}
			}
			run.blocked = append(run.blocked, store.BlockedTarget{
				InstrumentID: action.instrument.InstrumentID, Quantity: action.blockedQuantity(),
				Reason: reason,
			})
		}
	}
	run.transferRetry = max(run.transferRetry, outcome.RetryAfter)
	stageActions = stageActions[:0:0]
	for _, action := range scaledActions {
		if _, waiting := outcome.Waiting[action.member.account.TradingAccountID]; !waiting {
			stageActions = append(stageActions, action)
		}
	}
	return stageActions, nil
}

// runStages 执行阶段（阶段 1 撤旧单在 Converge 里已做）：2 减仓、3 合约加仓、4 划转、5 现货买入、6 归集（归集在 finishConverge）。
// 阶段不单独存储，每轮从当前差额推出：前一阶段还有能下的单就只做它，没有了才进入下一阶段。
// 被阻塞、下不了单的标的在本阶段视为已处理，不卡住后面的阶段；批额度顺延的标的留在本阶段，后面的阶段不能插队。
// 下好的子单记在 run.placedIDs，由 finishConverge 统一提交。handled 为真表示本轮到此为止，直接返回 result。
func (e *Executor) runStages(
	ctx context.Context,
	spaceID string,
	logicalAccount store.LogicalAccountRecord,
	target *store.LogicalAccountTargetRecord,
	members []memberState,
	desired map[string]desiredTarget,
	env *laneEnv,
	actions []laneAction,
	run *stageRun,
) (Result, bool, error) {
	quota := e.batchQuota(members)
	// 本轮建子单时同一执行账户的模拟盘保证金估值只算一次快照；发送阶段仍用原 ctx。
	placeCtx := orderapp.WithSnapshotReuse(ctx)
	for _, stage := range []int{stageReduce, stageSwapAdd, stageSpotBuy} {
		if len(run.placedIDs) > 0 || run.deferred {
			break
		}
		var stageActions []laneAction
		for _, action := range actions {
			if actionStage(action) == stage {
				stageActions = append(stageActions, action)
			}
		}
		if len(stageActions) == 0 {
			continue
		}
		if stage == stageSpotBuy {
			prepared, err := e.prepareSpotBuys(ctx, env, spaceID, *target, stageActions, desired, members, run)
			if err != nil {
				return Result{}, false, err
			}
			stageActions = prepared
		}
		for _, action := range stageActions {
			if quotaExhausted(quota) {
				// 所有执行账户的本批额度都用完：剩下的标的留到下一批，不再逐个查询。
				run.deferred = true
				break
			}
			if quota[action.member.account.TradingAccountID] <= 0 {
				// 首选成员额度用完：本轮跳过，不换成员。
				run.deferred = true
				continue
			}
			instrumentID := action.instrument.InstrumentID
			unreflected, checkErr := e.positionUnreflected(ctx, spaceID, members, instrumentID)
			if checkErr != nil {
				return Result{}, false, checkErr
			}
			if unreflected {
				if e.Observe != nil {
					e.Observe.Blocked(action.member.account.TradingAccountID, "duplicate_blocked")
				}
				// 持仓回退到某笔已成交子单下单前的值：整条标的本轮跳过，不换成员，等同步纠正后重算。
				// 这是暂时状态，买单马上要继续：不归集，否则资金归集回合约钱包后，同步追上来买单就没钱了。
				run.blocked = append(run.blocked, store.BlockedTarget{
					InstrumentID: instrumentID,
					Quantity:     action.blockedQuantity(),
					Reason:       reasonPositionUnreflected,
				})
				run.holdCollect(stage, action)
				continue
			}
			placement, placeErr := e.placeActionMode(
				placeCtx, spaceID, *target, action, members, quota, env, false,
			)
			if placeErr != nil {
				if len(run.placedIDs) > 0 {
					// 已经落库的子单照常发出，错误留到下一轮。
					break
				}
				failure, failureErr := e.placementFailure(ctx, logicalAccount, target, placeErr)
				return failure, true, failureErr
			}
			if placement.deferred {
				run.deferred = true
				continue
			}
			if placement.deferReason != "" {
				// 停牌、涨跌停、T+1：这个标的在本阶段视为已处理，原因写进 blocked_targets，不算阻塞；目标有效期内过一会儿再看一次。
				run.marketDeferred = true
				run.holdCollect(stage, action)
				run.deferredItems = append(run.deferredItems, store.BlockedTarget{
					InstrumentID: instrumentID, Quantity: action.blockedQuantity(), Reason: placement.deferReason, Deferred: true,
				})
				continue
			}
			if placement.capDeferred {
				// 每小时额度用完：这个标的在本阶段视为已处理，不卡后面的阶段，留到下一个整点；
				// 原因写进目标的 blocked_targets，界面看得到为什么没下单。
				run.capDeferred = true
				run.holdCollect(stage, action)
				run.deferredItems = append(run.deferredItems, store.BlockedTarget{
					InstrumentID: instrumentID, Quantity: action.blockedQuantity(), Reason: reasonHourlyCapDeferred, Deferred: true,
				})
				continue
			}
			if action.scaled && (placement.belowConfiguredMin || placement.tooSmall) {
				// 缩小后的买单太小下不了：不是“已满足”，原因是合约钱包划转的资金不够，要让界面看得到。
				run.blocked = append(run.blocked, store.BlockedTarget{
					InstrumentID: instrumentID, Quantity: action.blockedQuantity(), Reason: action.scaleReason,
				})
				if wallet.IsFundsShortage(action.scaleReason) {
					run.holdCollect(stage, action)
				}
				continue
			}
			if placement.tooSmall && laneFinished(desired[instrumentID], members, instrumentID) {
				continue
			}
			if placement.belowConfiguredMin {
				continue
			}
			if placement.reason != "" {
				run.blocked = append(run.blocked, store.BlockedTarget{
					InstrumentID: instrumentID,
					Quantity:     action.blockedQuantity(),
					Reason:       placement.reason,
				})
				continue
			}
			if placement.placed {
				run.placedIDs = append(run.placedIDs, placement.orderID)
			}
		}
	}
	return Result{}, false, nil
}

// finishConverge 在执行阶段之后收尾：提交这一轮下好的子单；批额度顺延、有账户在等划转时目标保持收敛中；
// 第 6 阶段把现货钱包多出来的资金归集回合约钱包，归集完成才定终态；最后按阻塞原因定 BLOCKED 或 CONVERGED，并算下一次唤醒时间。
func (e *Executor) finishConverge(
	ctx context.Context,
	spaceID string,
	logicalAccount store.LogicalAccountRecord,
	target *store.LogicalAccountTargetRecord,
	members []memberState,
	run *stageRun,
	resting map[string]*restingLane,
	orderSettings ordersettings.Settings,
) (Result, error) {
	if len(run.placedIDs) > 0 {
		e.noteBatch(logicalAccount.LogicalAccountID)
		results, batchErr := e.Orders.SubmitBatch(ctx, spaceID, run.placedIDs)
		failure, handled, otherErr := e.batchFailure(ctx, logicalAccount, target, results)
		if handled {
			return failure, otherErr
		}
		// 其它错误只留给下一轮重试，目标状态、阻塞列表照常刷新。
		if batchErr == nil {
			batchErr = otherErr
		} else if otherErr != nil {
			batchErr = errors.Join(batchErr, otherErr)
		}
		if batchErr != nil && len(results) == 0 {
			return Result{}, batchErr
		}
		target.BlockedTargets = listedBlocked(run.blocked, run.deferredItems)
		target.LastError = ""
		if err := e.updateTarget(ctx, target, StatusConverging); err != nil {
			return Result{}, errors.Join(batchErr, err)
		}
		return Result{Status: StatusConverging, Action: "place"}, batchErr
	}
	if run.deferred {
		target.BlockedTargets = listedBlocked(run.blocked, run.deferredItems)
		if err := e.updateTarget(ctx, target, StatusConverging); err != nil {
			return Result{}, err
		}
		return Result{Status: StatusConverging, Action: "pace"}, nil
	}
	if run.transferWaiting {
		// 有账户的现货买单还在等划转（含失败后退避重试）：目标保持收敛中，不归集、不定终态；退避期内不请求交易所。
		target.BlockedTargets = listedBlocked(run.blocked, run.deferredItems)
		if err := e.updateTarget(ctx, target, StatusConverging); err != nil {
			return Result{}, err
		}
		return Result{Status: StatusConverging, Action: "transfer", RetryAfter: max(transferPollInterval, run.transferRetry)}, nil
	}
	// 阶段 6：普通账户把现货钱包里多出来的资金归集回合约钱包，归集完成才定终态。
	// 本目标里还有没做完的现货买单的账户不归集（见 run.openBuyAccounts）。
	if collecting, err := e.walletCollect(ctx, spaceID, *target, members, run.openBuyAccounts); err != nil {
		return Result{}, err
	} else if collecting {
		target.BlockedTargets = listedBlocked(run.blocked, run.deferredItems)
		if err := e.updateTarget(ctx, target, StatusConverging); err != nil {
			return Result{}, err
		}
		return Result{Status: StatusConverging, Action: "collect", RetryAfter: transferPollInterval}, nil
	}
	target.BlockedTargets = listedBlocked(run.blocked, run.deferredItems)
	target.LastError = ""
	// 只剩挂单（或阻塞加挂单）时，定时器不再扫这个目标：挂单成交会唤醒目标，下一份目标到达时在阶段 1 撤掉旧挂单，
	// 一直没有成交也没有新目标则到有效期末被唤醒，走过期分支撤掉本目标的挂单，所以都要设唤醒时间。
	var retryAfter time.Duration
	if len(resting) > 0 {
		retryAfter = e.restingWake(*target, resting, orderSettings)
	}
	// 有标的因每小时上限顺延：定时器不会再扫已收敛的目标，到下一个整点自己唤醒继续下顺延的部分；同时有挂单时取较早的唤醒时间。
	if run.capDeferred {
		if next := nextHourRetryDelay(e.now()); retryAfter == 0 || next < retryAfter {
			retryAfter = next
		}
	}
	// 停牌复牌、涨跌停打开都可能在目标有效期内发生：过 60 秒再收敛一次，到期后走过期分支。
	if run.marketDeferred && (retryAfter == 0 || marketDeferralRetry < retryAfter) {
		retryAfter = marketDeferralRetry
	}
	if len(run.blocked) > 0 {
		if err := e.updateTarget(ctx, target, StatusBlocked); err != nil {
			return Result{}, err
		}
		return Result{Status: StatusBlocked, RetryAfter: retryAfter}, nil
	}
	if err := e.updateTarget(ctx, target, StatusConverged); err != nil {
		return Result{}, err
	}
	return Result{Status: StatusConverged, RetryAfter: retryAfter}, nil
}
