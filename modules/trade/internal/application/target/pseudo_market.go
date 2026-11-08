package target

import (
	"fmt"
	"strings"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/rs/xid"
	"trpc.group/trpc-go/trpc-go/log"
)

// 伪市价单：限价 + IOC，价格 = 参考价 ×（1 ± s），买入向上、卖出向下取整到价格精度。
// 没成交完的部分下一轮按最新持仓只补剩余量，滑点每次加一份 s，直到上限；用满次数仍没成交完就记“未到位”。

// pseudoMarketAttempts 返回下一笔伪市价单是第几次尝试（从 0 开始）：
// 这个标的本目标里（所有执行账户，换成员不重新计数）最近连续的、没有全部成交的 IOC 限价子单的笔数。
// 一笔 IOC 子单全部成交就清零，所以拆单时每一片都从首次滑点开始，不会因为前面的片耗掉重试次数；
// 非 IOC 的子单（撤掉的挂单、市价单）跳过，不打断计数，挂单和伪市价交替出现时次数不会被清零。
// 从没发到交易所的子单也跳过：提交结果未知、过了未知窗口查无此单退回待提交、随后被丢弃的单（已撤销、零成交、没有交易所单号），
// 不是一次真正的尝试，不能耗掉滑点次数。
func pseudoMarketAttempts(children []store.TargetChildOrder) int {
	attempts := 0
	for index := len(children) - 1; index >= 0; index-- {
		child := children[index]
		if child.OrderType != string(exchange.OrderTypeLimit) || child.TimeInForce != string(exchange.FillPolicyIOC) {
			continue
		}
		quantity, quantityErr := shared.ParseDecimal(child.Quantity)
		filled, filledErr := shared.ParseDecimal(child.FilledQuantity)
		if quantityErr == nil && filledErr == nil && filled.Cmp(quantity) >= 0 {
			break
		}
		if child.State == string(orderdomain.Canceled) && child.ExchangeOrderID == "" && filledErr == nil && filled.IsZero() {
			continue
		}
		attempts++
	}
	return attempts
}

// pseudoMarketPrice 返回第 attempt 次尝试的限价；价格精度不明（没有或不合法的 PriceTick）时不取整。
func pseudoMarketPrice(reference shared.Decimal, side exchange.Side, settings ordersettings.Settings, attempt int, priceTick string) shared.Decimal {
	slippage := settings.Slippage(attempt)
	one := shared.MustDecimal("1")
	tick, tickErr := shared.ParseDecimal(priceTick)
	validTick := tickErr == nil && tick.Cmp(shared.Zero()) > 0
	if side == exchange.SideBuy {
		price := reference.Mul(one.Add(slippage))
		if validTick {
			price = ceilToStep(price, tick)
		}
		return price
	}
	price := reference.Mul(one.Sub(slippage))
	if validTick {
		price = floorToStep(price, tick)
	}
	return price
}

// priceBandHeadroom 是价格偏离限制里实际使用的比例：交易所按自己的参考价（合约是标记价、现货是加权均价）判断，
// 和这里用的最新成交价有差距，只用允许偏离的 90%，避免贴着边界被拒单。
var priceBandHeadroom = shared.MustDecimal("0.9")

// ClampToPriceBand 把伪市价单的限价夹在交易所的价格偏离限制里（币安 PERCENT_PRICE / PERCENT_PRICE_BY_SIDE）：
// 买单不高于 参考价 ×（1 + (上限倍数 − 1) × 90%），卖单不低于 参考价 ×（1 − (1 − 下限倍数) × 90%），向内取整到价格精度。
// 标的没有给出限制（倍数为 0 或读不出来）时原样返回。
func ClampToPriceBand(price, reference shared.Decimal, side exchange.Side, instrument store.InstrumentRecord) shared.Decimal {
	one := shared.MustDecimal("1")
	tick, tickErr := shared.ParseDecimal(instrument.PriceTick)
	validTick := tickErr == nil && tick.Cmp(shared.Zero()) > 0
	if side == exchange.SideBuy {
		ratio, err := shared.ParseDecimal(instrument.MaxBuyPriceRatio)
		if err != nil || ratio.Cmp(one) <= 0 {
			return price
		}
		ceiling := reference.Mul(one.Add(ratio.Sub(one).Mul(priceBandHeadroom)))
		if validTick {
			ceiling = floorToStep(ceiling, tick)
		}
		if price.Cmp(ceiling) > 0 {
			return ceiling
		}
		return price
	}
	ratio, err := shared.ParseDecimal(instrument.MinSellPriceRatio)
	if err != nil || ratio.Cmp(shared.Zero()) <= 0 || ratio.Cmp(one) >= 0 {
		return price
	}
	floor := reference.Mul(one.Sub(one.Sub(ratio).Mul(priceBandHeadroom)))
	if validTick {
		floor = ceilToStep(floor, tick)
	}
	if price.Cmp(floor) < 0 {
		return floor
	}
	return price
}

// ceilToStep 把正数向上取整到步长的整数倍。
func ceilToStep(value, step shared.Decimal) shared.Decimal {
	floored := floorToStep(value, step)
	if floored.Cmp(value) == 0 {
		return floored
	}
	return floored.Add(step)
}

// childClientOrderID 生成子单的客户端订单号：带上伪市价的尝试次数，重试的单号和上一次不同。
func childClientOrderID(attempt int) string {
	return fmt.Sprintf("mt%sa%d", xid.New().String(), attempt)
}

// alreadyWarned 报告这个键是否已经告警过（不改变状态）。
func (e *Executor) alreadyWarned(key string) bool {
	e.batchMu.Lock()
	defer e.batchMu.Unlock()
	_, warned := e.warned[key]
	return warned
}

// warnOnce 同一个键只告警一次（进程内）：伪市价用满次数这类每轮都会再次判到的情况不刷屏。
func (e *Executor) warnOnce(key, message string) {
	e.batchMu.Lock()
	// 每个目标、每个标的一个键，目标换得快：积到上千条就整个清掉（最多多告警一次），不让它随进程寿命无限增长。
	if e.warned == nil || len(e.warned) >= 1000 {
		e.warned = make(map[string]struct{})
	}
	_, repeated := e.warned[key]
	e.warned[key] = struct{}{}
	e.batchMu.Unlock()
	if !repeated {
		log.Warnf("交易关注项：%s", strings.TrimSpace(message))
	}
}
