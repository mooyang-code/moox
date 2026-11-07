package target

import (
	"fmt"
	"strings"

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
// 这个标的本目标里最近连续的、没有全部成交的 IOC 限价子单的笔数。一笔子单全部成交就清零，
// 所以拆单时每一片都从首次滑点开始，不会因为前面的片耗掉重试次数。
func pseudoMarketAttempts(children []store.TargetChildOrder) int {
	attempts := 0
	for index := len(children) - 1; index >= 0; index-- {
		child := children[index]
		if child.OrderType != string(exchange.OrderTypeLimit) || child.TimeInForce != string(exchange.FillPolicyIOC) {
			break
		}
		quantity, quantityErr := shared.ParseDecimal(child.Quantity)
		filled, filledErr := shared.ParseDecimal(child.FilledQuantity)
		if quantityErr == nil && filledErr == nil && filled.Cmp(quantity) >= 0 {
			break
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

// warnOnce 同一个键只告警一次（进程内）：伪市价用满次数这类每轮都会再次判到的情况不刷屏。
func (e *Executor) warnOnce(key, message string) {
	e.batchMu.Lock()
	if e.warned == nil {
		e.warned = make(map[string]struct{})
	}
	_, repeated := e.warned[key]
	e.warned[key] = struct{}{}
	e.batchMu.Unlock()
	if !repeated {
		log.Warnf("交易关注项：%s", strings.TrimSpace(message))
	}
}
