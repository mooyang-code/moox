package target

import (
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// A 股（市场规则里有涨跌停、T+1、整手的市场）只能顺延的原因。顺延不算阻塞，不触发“目标持续阻塞”告警，
// 也不算“持仓未到位”：Converge 和 HasDifference 都按 deferralReason 判断，口径一致。
// marketDeferralRetry 是有按市场规则顺延的项时，再看一次的间隔。
const marketDeferralRetry = 60 * time.Second

const (
	reasonHalted    = "停牌"
	reasonLimitUp   = "涨停"
	reasonLimitDown = "跌停"
	reasonT1        = "当日买入，次日可卖"
)

// deferralReason 返回这一笔动作因为市场规则只能顺延的原因；能下单时返回空串。
//   - 停牌：不能下单；
//   - 涨停时买、跌停时卖：现价已经在涨跌停价、对手方没有挂单，价格截在涨跌停价上也成交不了，提高滑点没用，
//     交给伪市价的次数上限会被记成“未到位”；
//   - 只因为 T+1 卖不出：当日买入的部分次日才能卖。
func deferralReason(action laneAction, quote Quote) string {
	rule := exchange.RuleFor(action.instrument.MarketType)
	if rule.PriceLimits {
		buying := !action.delta.IsNegative()
		switch {
		case quote.Halted:
			return reasonHalted
		case buying && quote.LimitUp.Cmp(shared.Zero()) > 0 && quote.Price.Cmp(quote.LimitUp) >= 0 && quote.Ask.Cmp(shared.Zero()) <= 0:
			return reasonLimitUp
		case !buying && quote.LimitDown.Cmp(shared.Zero()) > 0 && quote.Price.Cmp(quote.LimitDown) <= 0 && quote.Bid.Cmp(shared.Zero()) <= 0:
			return reasonLimitDown
		}
	}
	if rule.T1 && rule.PositionFromBalance && action.reducing {
		if quantity, _ := sellQuantity(action, action.delta.Abs()); quantity.IsZero() && t1Locked(action) {
			return reasonT1
		}
	}
	return ""
}

// t1Locked 报告这个成员是否“只因为 T+1”卖不出：现在一股都卖不出，而把当日买入的部分算回可卖后就能卖。
// 挂单冻结的部分不算 T+1。
func t1Locked(action laneAction) bool {
	id := action.instrument.InstrumentID
	bought := action.member.t1[id]
	if !exchange.RuleFor(action.instrument.MarketType).T1 || bought.Cmp(shared.Zero()) <= 0 {
		return false
	}
	want := action.delta.Abs()
	if now, _ := SellQuantity(action.instrument, want, action.member.sellable[id]); !now.IsZero() {
		return false
	}
	later, _ := SellQuantity(action.instrument, want, action.member.sellable[id].Add(bought))
	return !later.IsZero()
}

// sellQuantity 把一笔持仓来自余额的卖单截到这个成员的可卖数量，见 SellQuantity。
func sellQuantity(action laneAction, quantity shared.Decimal) (shared.Decimal, bool) {
	return SellQuantity(action.instrument, quantity, action.member.sellable[action.instrument.InstrumentID])
}

// SellQuantity 把一笔持仓来自余额的卖单截到可卖数量，目标执行器和手动清仓共用：
//   - 整手交易的市场（A 股）：要卖的数量不小于全部可卖数量时，直接卖出全部可卖数量，允许零股（oddFull 为真）；
//   - 否则按步长取整；取整后低于最小数量返回 0。
func SellQuantity(instrument store.InstrumentRecord, quantity, sellable shared.Decimal) (shared.Decimal, bool) {
	if sellable.Cmp(shared.Zero()) <= 0 {
		return shared.Zero(), false
	}
	if exchange.RuleFor(instrument.MarketType).BoardLot && quantity.Cmp(sellable) >= 0 {
		return sellable, true
	}
	if quantity.Cmp(sellable) > 0 {
		quantity = sellable
	}
	quantity = floorToStep(quantity, mustBaseStep(instrument))
	if belowMinimumQuantity(quantity, instrument) {
		return shared.Zero(), false
	}
	return quantity, false
}

// oddLotFullSell 报告一笔整手市场的卖单是不是“卖出全部可卖数量”：这时零股可以一次卖完，不按步长取整。
func oddLotFullSell(action laneAction) bool {
	if !action.reducing || !exchange.RuleFor(action.instrument.MarketType).BoardLot ||
		!exchange.RuleFor(action.instrument.MarketType).PositionFromBalance {
		return false
	}
	sellable := action.member.sellable[action.instrument.InstrumentID]
	return sellable.Cmp(shared.Zero()) > 0 && action.delta.Abs().Cmp(sellable) >= 0
}

// boardLotLane 报告标的是否按整手交易（A 股）：看成员标的表里的市场，买入不足一手的差额视为已满足。
func boardLotLane(members []memberState, instrumentID string) bool {
	for _, member := range members {
		if instrument, ok := member.instruments[instrumentID]; ok {
			return exchange.RuleFor(instrument.MarketType).BoardLot
		}
	}
	return false
}

// CageBasis 是价格笼子的基准价：买入用卖一、卖出用买一，拿不到时退回最新价。没有买一卖一的市场就是最新价。
func CageBasis(quote Quote, side exchange.Side) shared.Decimal {
	basis := quote.Ask
	if side == exchange.SideSell {
		basis = quote.Bid
	}
	if basis.Cmp(shared.Zero()) <= 0 {
		return quote.Price
	}
	return basis
}

// ClampToPriceLimits 把下单价截在涨跌停价之间（绝对价，取自报价）；没有涨跌停的市场原样返回。
func ClampToPriceLimits(price shared.Decimal, quote Quote) shared.Decimal {
	if quote.LimitUp.Cmp(shared.Zero()) > 0 && price.Cmp(quote.LimitUp) > 0 {
		price = quote.LimitUp
	}
	if quote.LimitDown.Cmp(shared.Zero()) > 0 && price.Cmp(quote.LimitDown) < 0 {
		price = quote.LimitDown
	}
	return price
}
