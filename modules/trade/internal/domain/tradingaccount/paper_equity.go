package tradingaccount

import (
	"sort"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// AccountEquityInput 是统一账户权益公式的输入：
// 权益 = 结算资产净额（余额 - 借款）+ 永续未实现盈亏 + 其他稳定币保证金折算值 + 计价现货/股票持仓市值。
// 实盘与模拟盘的权益计算共用本公式，消除多套口径分叉。
type AccountEquityInput struct {
	SettlementNet shared.Decimal
	UnrealizedPnL shared.Decimal
	StableValue   shared.Decimal
	// HeldValues 是需要计入权益的非结算持仓市值之和（现货/股票持仓数量 × 估值价）。
	HeldValues shared.Decimal
}

// ComputeAccountEquity 按唯一权威公式计算账户总权益。
func ComputeAccountEquity(input AccountEquityInput) shared.Decimal {
	return input.SettlementNet.
		Add(input.UnrealizedPnL).
		Add(input.StableValue).
		Add(input.HeldValues)
}

// PaperPositionValuation 是模拟盘单个永续持仓的唯一权威估值公式：
// 未实现盈亏 = (标记价 - 开仓均价) × 带符号数量；占用保证金 = |数量| × 标记价 ÷ 杠杆。
func PaperPositionValuation(signedQty, entryPrice, markPrice, leverage shared.Decimal) (usedMargin, unrealizedPnL shared.Decimal) {
	if signedQty.IsZero() {
		return shared.Zero(), shared.Zero()
	}
	unrealizedPnL = markPrice.Sub(entryPrice).Mul(signedQty)
	if leverage.Cmp(shared.Zero()) > 0 {
		usedMargin = signedQty.Abs().Mul(markPrice).Div(leverage)
	}
	return usedMargin, unrealizedPnL
}

// PaperMarginPoolAdjustment 计算模拟盘单一资金池下的保证金可用额调整量：
//   - 保证金市场（合约，usesMargin=true）：浮盈亏 - 已占保证金（浮盈可抵保证金）；
//   - 余额市场（现货/股票，usesMargin=false）：min(浮盈亏, 0) - 已占保证金（现货不借币，合约浮盈不能用来买现货）。
func PaperMarginPoolAdjustment(usesMargin bool, usedMargin, unrealizedPnL shared.Decimal) shared.Decimal {
	pnl := unrealizedPnL
	if !usesMargin && pnl.Cmp(shared.Zero()) > 0 {
		pnl = shared.Zero()
	}
	return pnl.Sub(usedMargin)
}

// PaperEquityInput 是模拟盘账户快照（余额、可用资金、已占保证金、未实现盈亏、总权益）的唯一权威计算输入。
// 模拟盘适配器实时估值、关闭模拟盘静态刷新均调用 ComputePaperEquity，保证口径完全一致。
type PaperEquityInput struct {
	SettlementAsset   string
	HasBalanceMarket  bool
	HasPositionMarket bool
	Totals            map[string]shared.Decimal
	Locked            map[string]shared.Decimal
	// T1Locked 为 A 股等 T+1 场所当日买入不可卖数量（可选）。
	T1Locked map[string]shared.Decimal
	// AssetPrices 为非结算资产的估值价（资产 -> 结算币单价）；缺失资产不计市值。
	AssetPrices map[string]shared.Decimal
	// UsedMargin 与 UnrealizedPnL 为合约持仓汇总（可由 PaperPositionValuation 逐仓累加得到）。
	UsedMargin    shared.Decimal
	UnrealizedPnL shared.Decimal
	// PreserveCachedEquity 非空且账户含余额市场时保留缓存净值（用于关闭模拟盘时无实时报价场景）。
	PreserveCachedEquity *shared.Decimal
}

// PaperEquitySnapshot 是 ComputePaperEquity 的计算结果。
type PaperEquitySnapshot struct {
	Balances       []exchange.AssetBalance
	Equity         shared.Decimal
	AvailableFunds shared.Decimal
	UsedMargin     shared.Decimal
	UnrealizedPnL  shared.Decimal
}

// ComputePaperEquity 是模拟盘账户权益、可用资金与余额视图的唯一权威计算函数。
func ComputePaperEquity(input PaperEquityInput) PaperEquitySnapshot {
	settlement := input.Totals[input.SettlementAsset]
	lockedSettlement := input.Locked[input.SettlementAsset]

	heldValues := shared.Zero()
	if input.HasBalanceMarket {
		for asset, balance := range input.Totals {
			if asset == "" || asset == input.SettlementAsset || balance.IsZero() {
				continue
			}
			if price, ok := input.AssetPrices[asset]; ok && !price.IsZero() {
				heldValues = heldValues.Add(balance.Mul(price))
			}
		}
	}

	usedMargin := shared.Zero()
	unrealizedPnL := shared.Zero()
	if input.HasPositionMarket {
		usedMargin = input.UsedMargin
		unrealizedPnL = input.UnrealizedPnL
	}

	equity := ComputeAccountEquity(AccountEquityInput{
		SettlementNet: settlement,
		UnrealizedPnL: unrealizedPnL,
		HeldValues:    heldValues,
	})
	if input.HasBalanceMarket && input.PreserveCachedEquity != nil {
		equity = *input.PreserveCachedEquity
	}

	// 单一资金池：合约可用资金含浮盈并扣保证金与冻结；纯余额市场为结算币总额扣冻结。
	available := settlement.Sub(lockedSettlement)
	if input.HasPositionMarket {
		available = settlement.
			Add(PaperMarginPoolAdjustment(true, usedMargin, unrealizedPnL)).
			Sub(lockedSettlement)
	}

	balances := buildPaperBalances(input.Totals, input.Locked, input.T1Locked)
	if input.HasPositionMarket && input.HasBalanceMarket {
		// 单一资金池里结算币这一行供现货买单查看：扣合约占用保证金和浮亏，不把合约浮盈算作现货可花资金。
		spotAvailable := settlement.
			Add(PaperMarginPoolAdjustment(false, usedMargin, unrealizedPnL)).
			Sub(lockedSettlement)
		for i := range balances {
			if balances[i].Asset == input.SettlementAsset {
				balances[i].Available = spotAvailable
			}
		}
	}

	return PaperEquitySnapshot{
		Balances:       balances,
		Equity:         equity,
		AvailableFunds: available,
		UsedMargin:     usedMargin,
		UnrealizedPnL:  unrealizedPnL,
	}
}

func buildPaperBalances(total, locked, t1Locked map[string]shared.Decimal) []exchange.AssetBalance {
	assets := make([]string, 0, len(total)+len(locked))
	seen := make(map[string]struct{}, len(total)+len(locked))
	for asset := range total {
		if asset != "" {
			seen[asset] = struct{}{}
			assets = append(assets, asset)
		}
	}
	for asset := range locked {
		if asset != "" {
			if _, found := seen[asset]; !found {
				assets = append(assets, asset)
			}
		}
	}
	sort.Strings(assets)
	result := make([]exchange.AssetBalance, 0, len(assets))
	for _, asset := range assets {
		totalAmount := total[asset]
		lockedAmount := locked[asset]
		available := totalAmount.Sub(lockedAmount)
		if t1, ok := t1Locked[asset]; ok && !t1.IsZero() {
			available = available.Sub(t1)
			if available.IsNegative() {
				available = shared.Zero()
			}
		}
		result = append(result, exchange.AssetBalance{
			Wallet:    exchange.WalletUnified,
			Asset:     asset,
			Available: available,
			Locked:    lockedAmount,
			Total:     totalAmount,
		})
	}
	return result
}
