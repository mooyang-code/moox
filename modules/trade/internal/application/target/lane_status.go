package target

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// LaneState 是执行器对一条标的的结论：还会不会为它下单，不下单是因为什么。
type LaneState int

const (
	// LaneUnknown：判断要用的报价取不到，说不准。只在只读缓存的查询里出现，执行器自己取不到报价时照常下单。
	LaneUnknown LaneState = iota
	// LaneWillOrder：有差额，执行器在组合活动且目标有效时会为它下单。
	LaneWillOrder
	// LaneInPlace：已到位（差额为 0、在调仓阈值之内、目标是保持当前持仓，或剩下的零头不到交易所最小数量、最小名义金额）。
	LaneInPlace
	// LaneBelowMinNotional：差额低于设置的最小下单额，视为已满足。
	LaneBelowMinNotional
	// LaneMarketDeferred：按市场规则暂时不能交易（停牌、涨停、跌停、当日买入次日可卖），Reason 是具体原因。
	LaneMarketDeferred
	// LaneBelowLot：整手交易的市场里差额不足一手，视为已满足。
	LaneBelowLot
	// LaneBlocked：有差额但下不了单（没有成员支持、开新仓不到交易所最小值），要人处理，Reason 是原因。
	LaneBlocked
)

// LaneStatus 是一条标的的结论。
type LaneStatus struct {
	InstrumentID string
	State        LaneState
	Reason       string
	// quoteMissing 表示得出“还会下单”时有判断因为取不到报价没做：执行器照常下单，查询把它报成未知。
	quoteMissing bool
}

// pending 报告这条标的是否还有差额没处理（执行器还会下单，或者下不了单要人看）。
func (s LaneStatus) pending() bool {
	return s.State == LaneWillOrder || s.State == LaneBlocked
}

// laneQuoteMaxAge 是查询（LaneStatuses）能接受的报价缓存的最长时间：目标没收敛时执行器不到一分钟就会再取一次价，
// 缓存比这更旧说明执行器没在跑（暂停、过期、休市），这时不拿旧价下结论。
const laneQuoteMaxAge = 5 * time.Minute

// laneInputs 是判断各条标的要用的输入，HasDifference 和 LaneStatuses 用同一份。
type laneInputs struct {
	members []memberState
	desired map[string]desiredTarget
	env     *laneEnv
}

func (e *Executor) loadLaneInputs(ctx context.Context, target store.LogicalAccountTargetRecord) (laneInputs, error) {
	flattens, err := e.runningDelistFlattens(ctx, target.SpaceID, target.LogicalAccountID)
	if err != nil {
		return laneInputs{}, err
	}
	members, err := e.loadMembers(ctx, target.SpaceID, target.LogicalAccountID, flattens)
	if err != nil {
		return laneInputs{}, err
	}
	desired, err := desiredTargets(target)
	if err != nil {
		return laneInputs{}, err
	}
	constrainDesired(desired, members)
	logical, err := e.Store.GetLogicalAccount(ctx, target.SpaceID, target.LogicalAccountID)
	if err != nil {
		return laneInputs{}, err
	}
	orderSettings, err := loadOrderSettings(logical)
	if err != nil {
		return laneInputs{}, err
	}
	env := e.newLaneEnv(ctx, target.SpaceID, target.LogicalAccountID, orderSettings, members, desired)
	return laneInputs{members: members, desired: desired, env: env}, nil
}

// HasDifference 判断目标和实际持仓是否还有能下单的差额，用于过期目标的告警。
func (e *Executor) HasDifference(ctx context.Context, target store.LogicalAccountTargetRecord) (bool, error) {
	inputs, err := e.loadLaneInputs(ctx, target)
	if err != nil {
		return false, err
	}
	for _, instrumentID := range laneIDs(inputs.desired, inputs.members) {
		status, err := e.laneStatus(ctx, inputs, instrumentID)
		if err != nil {
			return false, err
		}
		if status.pending() {
			return true, nil
		}
	}
	return false, nil
}

// LaneStatuses 按标的返回执行器现在的结论，给管理台的持仓页用：和 HasDifference 是同一个判断（laneStatus），
// 页面不再自己算“是否到位”。只读库、只读已有的报价缓存，不向交易所发请求；判断要用的报价不在缓存里时返回 LaneUnknown。
func (e *Executor) LaneStatuses(ctx context.Context, target store.LogicalAccountTargetRecord) ([]LaneStatus, error) {
	reader := &Executor{Store: e.Store.ReadOnly(), Prices: e.Prices, Exchanges: e.Exchanges, Now: e.Now}
	ctx = exchange.WithCachedQuotes(ctx, laneQuoteMaxAge)
	inputs, err := reader.loadLaneInputs(ctx, target)
	if err != nil {
		return nil, err
	}
	ids := laneIDs(inputs.desired, inputs.members)
	statuses := make([]LaneStatus, 0, len(ids))
	for _, instrumentID := range ids {
		status, err := reader.laneStatus(ctx, inputs, instrumentID)
		if err != nil {
			return nil, err
		}
		if status.State == LaneWillOrder && status.quoteMissing {
			status.State = LaneUnknown
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// laneStatus 判断一条标的：先按调仓阈值和成员持仓算下一步动作（laneAction，和 Converge 共用），
// 再和下单时一样逐个看候选成员（placementCandidates），每个成员按 checkCandidate 的次序：
//  1. 数量按步长取整后不到交易所最小数量（childQuantity）：太小，换下一个成员；
//  2. 停牌、涨跌停、当日买入次日可卖（deferralReason）：整个标的顺延；
//  3. 金额不到交易所最小名义金额（合约全平例外）：太小，换下一个成员；
//  4. 金额低于设置的最小下单额（合约减仓、清仓例外）：太小，换下一个成员；
//  5. 都不是：这个成员能下，结论是还会下单。
//
// 全部成员都太小时：有成员是低于设置的最小下单额，或剩下的是零头（laneFinished：目标为 0、已有同向持仓或整手市场），
// 收敛时当作已满足；从空仓开新仓才算下不了单。本目标的拒单记录、伪市价次数、每小时上限只有下单时才知道，
// 结果写在目标的 blocked_targets 里。2～4 要用报价，取不到时按“还会下单”处理，并在 quoteMissing 里记下。
func (e *Executor) laneStatus(ctx context.Context, inputs laneInputs, instrumentID string) (LaneStatus, error) {
	status := LaneStatus{InstrumentID: instrumentID}
	desired := inputs.desired[instrumentID]
	action, complete, reason, err := e.laneAction(inputs.env, instrumentID, desired, inputs.members)
	switch {
	case err != nil:
		return status, err
	case complete:
		status.State = LaneInPlace
		return status, nil
	case reason != "":
		status.State, status.Reason = LaneBlocked, reason
		return status, nil
	}
	status.State = LaneWillOrder
	status.quoteMissing = inputs.env.quoteMissing[instrumentID]
	// firstSmall 是第一个成员太小的原因；configuredSmall 表示有成员是低于设置的最小下单额。
	firstSmall, configuredSmall := "", false
	for _, candidate := range placementCandidates(action, inputs.members) {
		small, decided := e.candidateStatus(ctx, inputs.env, candidate, &status)
		if decided {
			return status, nil
		}
		if firstSmall == "" {
			firstSmall = small
		}
		configuredSmall = configuredSmall || small == smallConfiguredMinimum
	}
	switch {
	case configuredSmall:
		status.State = LaneBelowMinNotional
	case !laneFinished(desired, inputs.members, instrumentID):
		status.State, status.Reason = LaneBlocked, firstSmall
	case exchange.RuleFor(action.instrument.MarketType).BoardLot:
		status.State = LaneBelowLot
	default:
		status.State = LaneInPlace
	}
	return status, nil
}

const (
	smallExchangeNotional  = "金额低于交易所最小名义金额"
	smallConfiguredMinimum = "金额低于配置的最小下单额"
)

// candidateStatus 看一个候选成员下不下得了这一笔。太小下不了时返回原因、decided 为假，调用方换下一个成员；
// 其余情况（能下、按市场规则顺延、取不到报价说不准）把结论写进 status，decided 为真。
func (e *Executor) candidateStatus(ctx context.Context, env *laneEnv, candidate laneAction, status *LaneStatus) (small string, decided bool) {
	quantity, small, err := childQuantity(candidate)
	if err != nil {
		// 标的的数量规则读不出来：下单时会报错，这里不下结论。
		return "", true
	}
	if small != "" {
		return small, false
	}
	quote, quoted := e.laneQuote(ctx, candidate)
	if !quoted {
		// 合约全平只查最小数量，不用报价；其余都要看金额或涨跌停。
		status.quoteMissing = status.quoteMissing || !isFullClose(candidate, quantity)
		return "", true
	}
	if why := deferralReason(candidate, quote); why != "" {
		status.State, status.Reason = LaneMarketDeferred, why
		return "", true
	}
	minNotional, err := candidate.instrument.MinNotionalAt(quote.Price)
	if err != nil {
		return "", true
	}
	below, err := belowMinimumNotional(quantity, quote.Price, minNotional)
	if err != nil {
		return "", true
	}
	if below && !isFullClose(candidate, quantity) {
		return smallExchangeNotional, false
	}
	if env.belowConfiguredMinimum(candidate, quantity, quote.Price) {
		return smallConfiguredMinimum, false
	}
	return "", true
}

// laneQuote 取这一笔动作所在成员、标的的报价；取不到或价格不是正数时 ok 为假。
func (e *Executor) laneQuote(ctx context.Context, action laneAction) (Quote, bool) {
	if e.Prices == nil {
		return Quote{}, false
	}
	quote, err := e.Prices.LatestPrice(ctx, action.member.account.TradingAccountID,
		exchange.NewSymbolKey(action.instrument.MarketType, action.instrument.ExchangeSymbol))
	if err != nil || quote.Price.Cmp(shared.Zero()) <= 0 {
		return Quote{}, false
	}
	return quote, true
}
