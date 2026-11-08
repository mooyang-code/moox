package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// 挂单：限价 + GTC，买单挂买一价，卖单挂卖一价。
//   - 挑选：每轮按每个方向（买、卖）的金额预算挑选标的，每个标的每轮最多一片，用“组合 ID + 目标 ID”做种子，结果可复现；
//   - 不阻塞：本目标的挂单不让其他标的等待；挂单的剩余量当作“已在路上”，从差额里扣掉，避免再下一笔吃单造成超买；
//   - 到期撤单：超过挂单寿命的挂单撤掉，剩余量重新计入差额，同一目标里这个标的之后改吃单；寿命为 0 时保留到下一份目标到达或目标过期；
//   - 建仓、清仓不挂单。

// BookSource 是能给出买一价、卖一价的报价来源。挂单要用；报价来源没有实现它时，挂单退回普通下单。
type BookSource interface {
	Book(ctx context.Context, tradingAccountID string, key exchange.SymbolKey) (bid, ask shared.Decimal, err error)
}

// bookQuoteMaxAge 是买一、卖一报价的最长允许时延。
const bookQuoteMaxAge = 10 * time.Second

// Book 实现 BookSource：取适配器的买一、卖一。
func (s ExchangePriceSource) Book(ctx context.Context, tradingAccountID string, key exchange.SymbolKey) (shared.Decimal, shared.Decimal, error) {
	if s.Adapters == nil {
		return shared.Zero(), shared.Zero(), ErrExecutorConfig
	}
	adapter, err := s.Adapters.Adapter(tradingAccountID)
	if err != nil {
		return shared.Zero(), shared.Zero(), err
	}
	quote, err := adapter.GetQuote(ctx, key)
	if err != nil {
		return shared.Zero(), shared.Zero(), err
	}
	if !quote.SourceTime.IsZero() && !execution.QuoteFresh(s.Exchanges, exchangeOf(adapter), quote.SourceTime, s.now(), bookQuoteMaxAge) {
		return shared.Zero(), shared.Zero(), fmt.Errorf("买一卖一报价已过期")
	}
	return quote.Bid, quote.Ask, nil
}

// restingLane 是一个标的上本目标簿上的挂单汇总。
type restingLane struct {
	orders []store.OrderRecord
	// signedRemaining 是挂单剩余量的合计：买为正、卖为负。
	signedRemaining shared.Decimal
}

func (lane *restingLane) add(record store.OrderRecord) {
	remaining := shared.Zero()
	if quantity, err := shared.ParseDecimal(record.Quantity); err == nil {
		remaining = quantity
		if filled, filledErr := shared.ParseDecimal(record.FilledQuantity); filledErr == nil {
			remaining = quantity.Sub(filled)
		}
	}
	if remaining.IsNegative() {
		remaining = shared.Zero()
	}
	if record.Side == string(exchange.SideSell) {
		remaining = remaining.Neg()
	}
	lane.orders = append(lane.orders, record)
	lane.signedRemaining = lane.signedRemaining.Add(remaining)
}

// restingExpiresAt 返回挂单的到期时间；寿命为 0（保留到下一份目标）时返回零值。
func restingExpiresAt(record store.OrderRecord, settings ordersettings.Settings) time.Time {
	if settings.RestingTTL <= 0 || record.SubmittedAt <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(record.SubmittedAt).Add(settings.RestingTTL)
}

// reconcileResting 用本目标的挂单调整这一轮的动作：
//   - 同向且没超出差额：挂单剩余量从差额里扣掉，扣完没有要补的量就不再下单；
//   - 反向、或者合计超出差额（包括标的已经到位、没有动作）：挂着只会买多或卖多，撤掉，剩余量重新计入差额；
//   - 被阻塞的标的不动它的挂单。
//
// 返回要继续执行的动作（已扣掉挂单）和要撤的挂单。
func reconcileResting(
	actions []laneAction,
	resting map[string]*restingLane,
	blocked []store.BlockedTarget,
) (kept []laneAction, cancel []store.OrderRecord) {
	blockedLanes := make(map[string]struct{}, len(blocked))
	for _, item := range blocked {
		blockedLanes[item.InstrumentID] = struct{}{}
	}
	byLane := make(map[string]int, len(actions))
	for index, action := range actions {
		byLane[action.instrument.InstrumentID] = index
	}
	covered := make(map[int]struct{})
	for instrumentID, lane := range resting {
		if _, isBlocked := blockedLanes[instrumentID]; isBlocked {
			continue
		}
		index, hasAction := byLane[instrumentID]
		if !hasAction {
			cancel = append(cancel, lane.orders...)
			continue
		}
		action := actions[index]
		step := mustBaseStep(action.instrument)
		if lane.signedRemaining.IsZero() {
			continue
		}
		overshoot := lane.signedRemaining.IsNegative() != action.delta.IsNegative() ||
			lane.signedRemaining.Abs().Cmp(floorToStep(action.delta.Abs(), step)) > 0
		if overshoot {
			cancel = append(cancel, lane.orders...)
			continue
		}
		actions[index].delta = action.delta.Sub(lane.signedRemaining)
		actions[index].hasResting = true
		_, minimum, err := baseQuantityRules(action.instrument)
		if err == nil && floorToStep(actions[index].delta.Abs(), step).Cmp(minimum) < 0 {
			covered[index] = struct{}{}
		}
	}
	for index, action := range actions {
		if _, done := covered[index]; !done {
			kept = append(kept, action)
		}
	}
	return kept, cancel
}

// restingEligible 判断动作能不能改成挂单：只有加仓、减仓、调仓（持仓和目标同向且目标不为 0）才挂，
// 建仓（持仓为 0）、清仓（目标为 0）、反向开仓不挂；这个标的已经有挂单的、本目标在它上面挂过单而且挂单已结束的也不再挂。
func restingEligible(action laneAction, desired map[string]desiredTarget, members []memberState) bool {
	if action.hasResting || action.rested {
		return false
	}
	instrumentID := action.instrument.InstrumentID
	target, ok := desired[instrumentID]
	if !ok || target.Hold || target.Quantity.IsZero() {
		return false
	}
	confirmed := shared.Zero()
	for _, member := range members {
		position := member.positions[instrumentID]
		if !position.IsZero() && !sameSign(position, target.Quantity) {
			return false
		}
		confirmed = confirmed.Add(position)
	}
	return !confirmed.IsZero()
}

// pickResting 挑出这一轮改成挂单的标的（键：内部标的 ID）。
// 每个方向单独算预算 = 挂单比例 × 该方向可挂单的名义金额合计；标的按“组合 ID + 目标 ID + 标的”的哈希排序（结果可复现），
// 依次挑选，挑选后累计金额四舍五入不超过预算就挑，所以多个标的时挂单金额占比接近设置的比例。
func pickResting(
	env *laneEnv,
	target store.LogicalAccountTargetRecord,
	actions []laneAction,
	desired map[string]desiredTarget,
	members []memberState,
	notional func(laneAction) (shared.Decimal, bool),
) map[string]struct{} {
	picked := make(map[string]struct{})
	ratio := env.settings.RestingRatio
	if ratio.IsZero() {
		return picked
	}
	type candidate struct {
		instrumentID string
		order        string
		notional     shared.Decimal
	}
	byDirection := map[bool][]candidate{}
	for _, action := range actions {
		if !restingEligible(action, desired, members) {
			continue
		}
		value, ok := notional(action)
		if !ok || value.Cmp(shared.Zero()) <= 0 {
			continue
		}
		if value.Cmp(env.settings.MaxChildNotional) > 0 {
			value = env.settings.MaxChildNotional
		}
		sum := sha256.Sum256([]byte(target.LogicalAccountID + "|" + target.TargetID + "|" + action.instrument.InstrumentID))
		byDirection[action.delta.IsNegative()] = append(byDirection[action.delta.IsNegative()], candidate{
			instrumentID: action.instrument.InstrumentID, order: hex.EncodeToString(sum[:]), notional: value,
		})
	}
	half := shared.MustDecimal("0.5")
	for _, lanes := range byDirection {
		total := shared.Zero()
		for _, lane := range lanes {
			total = total.Add(lane.notional)
		}
		budget := total.Mul(ratio)
		sort.Slice(lanes, func(i, j int) bool { return lanes[i].order < lanes[j].order })
		used := shared.Zero()
		for _, lane := range lanes {
			if used.Add(lane.notional.Mul(half)).Cmp(budget) <= 0 {
				picked[lane.instrumentID] = struct{}{}
				used = used.Add(lane.notional)
			}
		}
	}
	return picked
}

// restingNotional 估算一个动作的名义金额（|差额| × 最新价），挂单挑选用；取不到报价的标的不参与挂单。
func (e *Executor) restingNotional(ctx context.Context) func(laneAction) (shared.Decimal, bool) {
	return func(action laneAction) (shared.Decimal, bool) {
		quote, err := e.Prices.LatestPrice(ctx, action.member.account.TradingAccountID,
			exchange.NewSymbolKey(action.instrument.MarketType, action.instrument.ExchangeSymbol))
		if err != nil || quote.Price.Cmp(shared.Zero()) <= 0 {
			return shared.Zero(), false
		}
		return action.delta.Abs().Mul(quote.Price), true
	}
}

// restingPrice 返回挂单价：买单挂买一价，卖单挂卖一价。报价来源没有买一卖一、取不到或为 0 时 ok 为假，退回普通下单。
func (e *Executor) restingPrice(ctx context.Context, candidate laneAction) (shared.Decimal, bool) {
	book, ok := e.Prices.(BookSource)
	if !ok {
		return shared.Zero(), false
	}
	bid, ask, err := book.Book(ctx, candidate.member.account.TradingAccountID,
		exchange.NewSymbolKey(candidate.instrument.MarketType, candidate.instrument.ExchangeSymbol))
	if err != nil {
		return shared.Zero(), false
	}
	price := bid
	if candidate.delta.IsNegative() {
		price = ask
	}
	return price, price.Cmp(shared.Zero()) > 0
}

// restingWake 返回只剩挂单时的唤醒间隔：目标有效期末和各挂单到期时间里最早的那个，至少 1 秒。
func (e *Executor) restingWake(target store.LogicalAccountTargetRecord, resting map[string]*restingLane, settings ordersettings.Settings) time.Duration {
	wake := e.restingRetryAfter(target)
	now := e.now()
	for _, lane := range resting {
		for _, record := range lane.orders {
			expires := restingExpiresAt(record, settings)
			if expires.IsZero() {
				continue
			}
			if remaining := expires.Sub(now); remaining < wake {
				wake = remaining
			}
		}
	}
	if wake < time.Second {
		return time.Second
	}
	return wake
}
