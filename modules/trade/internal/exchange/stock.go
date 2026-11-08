package exchange

import (
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// A 股费用：佣金按比例、最低 5 元；印花税 0.05% 只收卖方；过户费 0.001% 买卖都收。
// 佣金比例取自账户配置（模拟账户复用 taker 费率字段），其余是常量。
var (
	stockMinCommission   = shared.MustDecimal("5")
	stockStampDutyRate   = shared.MustDecimal("0.0005")
	stockTransferFeeRate = shared.MustDecimal("0.00001")
)

// StockFee 是一笔 A 股成交（或预留）的全部费用。下单预留、下单容量、成交扣费三处都调它，算出来的数一致。
// notional 为 0 时没有费用（没有成交就不收最低佣金）。
func StockFee(side Side, notional, commissionRate shared.Decimal) shared.Decimal {
	if notional.Cmp(shared.Zero()) <= 0 {
		return shared.Zero()
	}
	commission := notional.Mul(commissionRate)
	if commission.Cmp(stockMinCommission) < 0 {
		commission = stockMinCommission
	}
	fee := commission.Add(notional.Mul(stockTransferFeeRate))
	if side == SideSell {
		fee = fee.Add(notional.Mul(stockStampDutyRate))
	}
	return fee
}

// StockBuyCost 是买入的总花费：成交额加费用。
func StockBuyCost(notional, commissionRate shared.Decimal) shared.Decimal {
	return notional.Add(StockFee(SideBuy, notional, commissionRate))
}

// beijing 是 A 股的时区（没有夏令时，用固定时差，不依赖系统时区库）。
var beijing = time.FixedZone("CST", 8*60*60)

// T1Since 返回 T+1 的起算时刻：北京时间当天零点。这之后的买入成交当天不能卖。
// 成交只会发生在交易时段内，所以按北京时间的自然日判断就是按交易日判断。
func T1Since(now time.Time) time.Time {
	local := now.In(beijing)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, beijing).UTC()
}
