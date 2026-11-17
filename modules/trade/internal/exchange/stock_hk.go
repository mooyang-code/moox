package exchange

import "github.com/mooyang-code/moox/modules/trade/internal/domain/shared"

// 港股费用，按 2026 年公开费率，模拟盘口径（不是任何券商的实际收费，也不保证与交易所最新公告一致）。
// 全部常量集中在这里，费率调整时只改这一处：
//   - 印花税：成交金额的 0.1%，买卖双方都收；按成交金额算出后向上取整到元，不足 1 港元按 1 港元；
//   - 交易费（港交所）：0.00565%，双边；
//   - 证监会交易征费：0.0027%，双边；
//   - 会财局交易征费：0.00015%，双边；
//   - 股份交收费（中央结算）：成交金额的 0.0042%，没有最低、最高，双边（港交所自 2025-06-30 起的收费）；
//   - 佣金：按账户配置的费率（模拟账户复用 taker 费率字段，建账户页面默认 0.03%），每笔最低 3 港元。
//
// 除印花税外各项不取整到分，误差在分以下。汇率不换算：账户以 HKD 计价和结算。
var (
	hkStampDutyRate     = shared.MustDecimal("0.001")
	hkStampDutyMinimum  = shared.MustDecimal("1")
	hkTradingFeeRate    = shared.MustDecimal("0.0000565")
	hkSFCLevyRate       = shared.MustDecimal("0.000027")
	hkAFRCLevyRate      = shared.MustDecimal("0.0000015")
	hkSettlementFeeRate = shared.MustDecimal("0.000042")
	hkMinCommission     = shared.MustDecimal("3")
)

// HKFee 是一笔港股成交（或预留）的全部费用，买卖相同。下单预留、下单容量、成交扣费三处都经 ExchangeRule.Fee 调它。
// notional 为 0 时没有费用（没有成交就不收最低佣金和最低印花税）。
func HKFee(_ Side, notional, commissionRate shared.Decimal) shared.Decimal {
	if notional.Cmp(shared.Zero()) <= 0 {
		return shared.Zero()
	}
	commission := notional.Mul(commissionRate)
	if commission.Cmp(hkMinCommission) < 0 {
		commission = hkMinCommission
	}
	return commission.
		Add(hkStampDuty(notional)).
		Add(notional.Mul(hkTradingFeeRate)).
		Add(notional.Mul(hkSFCLevyRate)).
		Add(notional.Mul(hkAFRCLevyRate)).
		Add(notional.Mul(hkSettlementFeeRate))
}

// hkStampDuty 是印花税：成交金额 × 0.1%，向上取整到元，不足 1 港元按 1 港元。
func hkStampDuty(notional shared.Decimal) shared.Decimal {
	one := shared.MustDecimal("1")
	duty := notional.Mul(hkStampDutyRate)
	if floored := shared.FloorToStep(duty, one); floored.Cmp(duty) != 0 {
		duty = floored.Add(one)
	}
	if duty.Cmp(hkStampDutyMinimum) < 0 {
		duty = hkStampDutyMinimum
	}
	return duty
}

// hkTickBands 是港交所的价位表（证券价格所在档位 → 最小价位）：价格不高于 upTo 的用这一档。
// 0.5～10 港元 0.005、10～20 港元 0.01、20～50 港元 0.02 这三档比旧价位表细，是港交所下调最小价位之后的取值
// （第一阶段 2025-08-04 生效：10～20 港元 0.01、20～50 港元 0.02；第二阶段 2026-08-03 生效：0.5～10 港元 0.005）。
// 各档是按 2026-11-17 腾讯实时报价里的真实价格反推核对的（1.005、8.395、18.81、29.08、68.85、138.5、376.0 等都落在对应档位上），
// 没有对照交易所公告原文；交易所再调整时只改这张表。模拟盘口径。
var hkTickBands = []struct{ upTo, tick shared.Decimal }{
	{shared.MustDecimal("0.25"), shared.MustDecimal("0.001")},
	{shared.MustDecimal("0.5"), shared.MustDecimal("0.005")},
	{shared.MustDecimal("10"), shared.MustDecimal("0.005")},
	{shared.MustDecimal("20"), shared.MustDecimal("0.01")},
	{shared.MustDecimal("50"), shared.MustDecimal("0.02")},
	{shared.MustDecimal("100"), shared.MustDecimal("0.05")},
	{shared.MustDecimal("200"), shared.MustDecimal("0.1")},
	{shared.MustDecimal("500"), shared.MustDecimal("0.2")},
	{shared.MustDecimal("1000"), shared.MustDecimal("0.5")},
	{shared.MustDecimal("2000"), shared.MustDecimal("1")},
	{shared.MustDecimal("5000"), shared.MustDecimal("2")},
}

// hkTopTick 是 5000 港元以上的最小价位。
var hkTopTick = shared.MustDecimal("5")

// HKMinPriceTick 是港股价位表里最小的一档，标的表里的价格步长存它（成交均价的小数位数按它取）；
// 下单限价按 HKPriceTick 对齐到价格所在档位。
const HKMinPriceTick = "0.001"

// HKPriceTick 返回价格所在档位的最小价位：价格正好在档位边界上时属于较低的一档（“0.25 以上至 0.50”）。
func HKPriceTick(price shared.Decimal) shared.Decimal {
	for _, band := range hkTickBands {
		if price.Cmp(band.upTo) <= 0 {
			return band.tick
		}
	}
	return hkTopTick
}
