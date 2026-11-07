package ledger

import (
	"math"
	"reflect"
	"testing"
)

// 以下算例都是手算：永续不占用现金，每期按收盘价差把盈亏结算进现金，权益 = 现金 + 现货市值；成交只改数量并扣手续费。

const (
	spotA = "A|spot"
	swapA = "A|swap"
	swapB = "B|swap"
)

func TestSwapProfitSettlesIntoCashEachBar(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 3)
	ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 10}, Targets: map[string]float64{swapA: 1}})
	position := ledger.Positions[swapA]
	near(t, "开仓数量", position.Quantity, 10)
	near(t, "永续开仓不占用现金", ledger.Cash, 100)
	// 价格涨到 12：盈亏 10 × 2 = 20 结算进现金，权益 120；目标不变时名义价值随权益再平衡到 120，没有交易。
	same := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 12}, Targets: map[string]float64{swapA: 1}})
	near(t, "涨价后成交前权益", same.EquityBefore, 120)
	near(t, "盈亏结算进现金", ledger.Cash, 120)
	near(t, "不再平衡则成交额为 0", same.Traded, 0)
	// 目标降到 0.5：名义价值 120 → 60，卖出 5 个，现金不变。
	reduce := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 12}, Targets: map[string]float64{swapA: 0.5}})
	near(t, "减仓数量", ledger.Positions[swapA].Quantity, 5)
	near(t, "减仓不动现金", ledger.Cash, 120)
	near(t, "减仓成交额", reduce.Traded, 60)
	near(t, "减仓后权益不变", reduce.EquityAfter, 120)
	near(t, "永续净名义价值", reduce.SwapValue, 60)
	near(t, "没有现货", reduce.SpotValue, 0)
}

func TestSwapShortGainsWhenPriceFalls(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 3)
	ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 10}, Targets: map[string]float64{swapA: -0.5}})
	position := ledger.Positions[swapA]
	near(t, "空头数量", position.Quantity, -5)
	near(t, "空头名义价值", position.Value, -50)
	near(t, "做空不动现金", ledger.Cash, 100)
	// 价格跌到 8：空头盈亏 −5 × (8 − 10) = +10 结算进现金，权益 110；目标仍是 −0.5，名义价值调到 −55，加空 15 / 8 = 1.875 个。
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 8}, Targets: map[string]float64{swapA: -0.5}})
	near(t, "成交前权益", outcome.EquityBefore, 110)
	near(t, "空头盈利进现金", ledger.Cash, 110)
	near(t, "加空后数量", ledger.Positions[swapA].Quantity, -6.875)
	near(t, "加空成交额", outcome.Traded, 15)
	near(t, "净名义价值为负", outcome.SwapValue, -55)
	near(t, "权益不变", outcome.EquityAfter, 110)
}

func TestSwapFlipChangesSideWithoutTouchingCash(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 3)
	ledger.Positions[swapA] = &Position{Quantity: 5, LastPrice: 10, Value: 50}
	// 价格 12：多头盈亏 5 × 2 = 10 结算进现金，权益 110。目标 −0.5：名义价值 60 → −55，卖出 115 / 12 个，数量由 5 变成负数。
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 12}, Targets: map[string]float64{swapA: -0.5}})
	near(t, "成交前权益", outcome.EquityBefore, 110)
	near(t, "现金只有结算的盈亏", ledger.Cash, 110)
	near(t, "反向后的空头数量", ledger.Positions[swapA].Quantity, 5-115.0/12)
	near(t, "成交额", outcome.Traded, 115)
	near(t, "权益不变", outcome.EquityAfter, 110)
}

// 现货满仓、永续多头盈利：盈利结算进现金，下一期可以用来补买现货，不会因现金不足每期都被缩减。
func TestSwapProfitFundsSpotPurchases(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 3)
	targets := map[string]float64{spotA: 1, swapB: 0.5}
	first := ledger.Step(Step{OK: true, Prices: map[string]float64{spotA: 10, swapB: 10}, Targets: targets})
	near(t, "首期现货满仓后现金为 0", ledger.Cash, 0)
	near(t, "首期永续名义价值", first.SwapValue, 50)
	second := ledger.Step(Step{OK: true, Prices: map[string]float64{spotA: 10, swapB: 12}, Targets: targets})
	near(t, "永续盈利 5 × 2 结算进现金", second.EquityBefore, 110)
	if second.BuyScale != nil || len(second.Unfilled) != 0 {
		t.Fatalf("永续盈利应能补买现货：%+v", second)
	}
	near(t, "现货补买到 110", ledger.Positions[spotA].Value, 110)
	near(t, "永续名义价值调到 55", ledger.Positions[swapB].Value, 55)
	near(t, "现金用尽", ledger.Cash, 0)
}

// 永续亏损把现金拖成负数：现货买入缩减为 0，目标按缩小后的权益折算，现货被卖出补回现金。
func TestSwapLossDrivesCashNegativeAndSpotIsSold(t *testing.T) {
	ledger := NewLedger(0, 0, 0, 3)
	ledger.Positions[spotA] = &Position{Quantity: 10, LastPrice: 10, Value: 100}
	ledger.Positions[swapB] = &Position{Quantity: 5, LastPrice: 10, Value: 50}
	// 永续跌到 8：亏损 5 × 2 = 10，现金 −10，权益 90；目标现货 1.0 → 90，卖出 10。
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{spotA: 10, swapB: 8}, Targets: map[string]float64{spotA: 1, swapB: 0.5}})
	near(t, "成交前权益", outcome.EquityBefore, 90)
	near(t, "现货卖出后现金回到 0", ledger.Cash, 0)
	near(t, "现货市值", ledger.Positions[spotA].Value, 90)
	near(t, "权益", outcome.EquityAfter, 90)
}

func TestSwapFeeChargedOnNotionalAndConverges(t *testing.T) {
	ledger := NewLedger(100, 0, 10, 3)
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 10}, Targets: map[string]float64{swapA: 1}})
	// 费用迭代收敛：名义价值 = 100 / 1.001，费用 = 名义价值 × 0.1%。
	notional := 100 / 1.001
	near(t, "名义价值", ledger.Positions[swapA].Value, notional)
	near(t, "手续费", outcome.Fee, notional*0.001)
	near(t, "现金扣掉手续费", ledger.Cash, 100-notional*0.001)
	near(t, "成交后权益等于名义价值", outcome.EquityAfter, notional)
}

func TestMixedHedgeKeepsSpotAndSwapIndependent(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 3)
	targets := map[string]float64{spotA: 0.5, swapA: -0.5}
	ledger.Step(Step{OK: true, Prices: map[string]float64{spotA: 10, swapA: 10}, Targets: targets})
	near(t, "现货数量", ledger.Positions[spotA].Quantity, 5)
	near(t, "永续空头数量", ledger.Positions[swapA].Quantity, -5)
	near(t, "现货买入只动用现货的现金", ledger.Cash, 50)
	// 现货、永续价格都涨到 12：空头亏损 10 结算进现金（40），现货市值 60，对冲后权益不变；再平衡把两边各调回 50。
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{spotA: 12, swapA: 12}, Targets: targets})
	near(t, "对冲后权益不变", outcome.EquityBefore, 100)
	near(t, "现货卖回 50", ledger.Positions[spotA].Value, 50)
	near(t, "空头名义价值回到 −50", ledger.Positions[swapA].Value, -50)
	// 现货卖出 10 回到现金，永续回补 10 / 12 个只改数量。
	near(t, "现金", ledger.Cash, 50)
	near(t, "权益", outcome.EquityAfter, 100)
	near(t, "现货市值", outcome.SpotValue, 50)
	near(t, "永续净名义价值", outcome.SwapValue, -50)
	if got := ledger.Holdings(); got != 2 {
		t.Fatalf("现货与永续是两个持仓：%d", got)
	}
}

func TestSpotNeverGoesShort(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 3)
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{spotA: 10}, Targets: map[string]float64{spotA: -0.5}})
	if _, held := ledger.Positions[spotA]; held || outcome.Traded != 0 {
		t.Fatalf("现货不能做空：%+v %+v", ledger.Positions, outcome)
	}
}

func TestSwapIsNotCashConstrained(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 3)
	// 现货全仓用光现金后，永续仍可按目标建立多头（模拟账本不计保证金）；现货买入的现金约束不受影响。
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{spotA: 10, swapB: 5}, Targets: map[string]float64{spotA: 1, swapB: 1}})
	near(t, "现金用光", ledger.Cash, 0)
	near(t, "现货市值", ledger.Positions[spotA].Value, 100)
	near(t, "永续名义价值", ledger.Positions[swapB].Value, 100)
	if outcome.BuyScale != nil || len(outcome.Unfilled) != 0 {
		t.Fatalf("永续不应触发现货的买入缩减：%+v", outcome)
	}
	near(t, "成交额含两个市场", outcome.Traded, 200)
	near(t, "单边换手以成交前权益为分母", outcome.Turnover, 1)
	near(t, "权益", outcome.EquityAfter, 100)
}

func TestSpotBuyScaledByCashWhileSwapExecutes(t *testing.T) {
	ledger := NewLedger(50, 0, 0, 3)
	ledger.Positions[spotA] = &Position{Quantity: 5, LastPrice: 10, Value: 50}
	// 权益 100：现货目标 1.0 要加买 50，但现金只有 50 且永续费用先扣——这里永续费率为 0，现金恰好够；
	// 把现货目标提到 1.2 就不够：需要买 70，现金 50，按 50/70 缩减。永续空头 −0.2 照常成交。
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{spotA: 10, swapA: 10}, Targets: map[string]float64{spotA: 1.2, swapA: -0.2}})
	if outcome.BuyScale == nil {
		t.Fatalf("现货买入应被缩减：%+v", outcome)
	}
	near(t, "缩减比例", *outcome.BuyScale, 50.0/70)
	near(t, "永续空头照常成交", ledger.Positions[swapA].Value, -20)
	near(t, "现金用尽", ledger.Cash, 0)
	if !reflect.DeepEqual(outcome.Unfilled, []string{spotA}) {
		t.Fatalf("只有现货目标未达成：%v", outcome.Unfilled)
	}
}

func TestSeparateFeeRatesPerMarket(t *testing.T) {
	ledger := NewLedger(100, 10, 20, 3)
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{spotA: 10, swapB: 5}, Targets: map[string]float64{spotA: 0.4, swapB: -0.3}})
	// 现货 0.1%、永续 0.2%：费用 = 现货成交额 × 0.001 + 永续成交额 × 0.002。
	spotTraded, swapTraded := ledger.Positions[spotA].Value, -ledger.Positions[swapB].Value
	near(t, "手续费分市场计", outcome.Fee, spotTraded*0.001+swapTraded*0.002)
	near(t, "成交额", outcome.Traded, spotTraded+swapTraded)
	near(t, "资金守恒：权益 = 成交前权益 − 手续费", outcome.EquityAfter, 100-outcome.Fee)
}

func TestFrozenSwapLiquidatesAtLastPriceChargingOnlyFee(t *testing.T) {
	ledger := NewLedger(100, 0, 10, 2)
	ledger.Positions[swapA] = &Position{Quantity: -10, LastPrice: 8, Value: -80}
	for bar := 1; bar <= 2; bar++ {
		outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{}, Targets: map[string]float64{swapA: -1}})
		if bar == 1 && (len(outcome.Liquidated) != 0 || !ledger.Positions[swapA].Frozen) {
			t.Fatalf("第 1 期只冻结：%+v", outcome)
		}
		if bar == 2 {
			if !reflect.DeepEqual(outcome.Liquidated, []string{swapA}) {
				t.Fatalf("缺价达到阈值应清算：%+v", outcome)
			}
			// 盈亏早已按最后价结算，清算只扣费用 = 80 × 0.1%。
			near(t, "清算后现金", ledger.Cash, 100-0.08)
			near(t, "清算费用", outcome.Fee, 0.08)
			near(t, "清算成交额", outcome.Traded, 80)
		}
	}
	if len(ledger.Positions) != 0 {
		t.Fatalf("清算后不应有持仓：%+v", ledger.Positions)
	}
}

func TestFrozenSwapDoesNotSettleUntilPriceReturns(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 5)
	ledger.Positions[swapA] = &Position{Quantity: 5, LastPrice: 12, Value: 60}
	frozen := ledger.Step(Step{OK: true, Prices: map[string]float64{}, Targets: map[string]float64{swapA: 0}})
	near(t, "冻结的永续不结算", frozen.EquityBefore, 100)
	if !reflect.DeepEqual(frozen.Unfilled, []string{swapA}) || frozen.Traded != 0 {
		t.Fatalf("冻结的永续调仓记为未成交：%+v", frozen)
	}
	// 价格回来：从冻结前的最后价 12 结算到 15，盈亏 5 × 3 = 15。
	resumed := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 15}, Targets: map[string]float64{swapA: 0.5}})
	near(t, "恢复报价后结算冻结期间的价差", resumed.EquityBefore, 115)
	near(t, "名义价值调到权益的一半", ledger.Positions[swapA].Value, 57.5)
}

func TestPositionKeysSortBySubjectThenMarket(t *testing.T) {
	keys := []string{"B|swap", "A-B|spot", "A|swap", "A|spot", "B|spot"}
	sortKeys(keys)
	if want := []string{"A|spot", "A|swap", "A-B|spot", "B|spot", "B|swap"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("排序不符：%v", keys)
	}
}

// 加杠杆的永续多头遇到暴跌：权益降到不超过 0 即爆仓，持仓全部平掉，之后不再开仓；不会因为“目标权重 × 负权益”被翻成反向持仓。
func TestLeveragedSwapLongBlowsUpAndStaysFlat(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 3)
	targets := map[string]float64{swapA: 5}
	ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 10}, Targets: targets})
	near(t, "5 倍多头的数量", ledger.Positions[swapA].Quantity, 50)
	// 跌 25%：亏损 50 × 2.5 = 125，现金 −25，权益 −25。
	crash := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 7.5}, Targets: targets})
	near(t, "爆仓期的成交前权益", crash.EquityBefore, -25)
	if !crash.Bankrupt || !ledger.Bankrupt {
		t.Fatalf("权益不为正应记为爆仓：%+v", crash)
	}
	if len(ledger.Positions) != 0 {
		t.Fatalf("爆仓后不能留下任何持仓（包括反向持仓）：%+v", ledger.Positions[swapA])
	}
	near(t, "平仓不改变现金", ledger.Cash, -25)
	near(t, "平仓成交额", crash.Traded, 375)
	near(t, "爆仓后权益", crash.EquityAfter, -25)
	// 之后价格反弹、目标仍是 5 倍多头：保持空仓，权益不变，也不再标记爆仓。
	for _, price := range []float64{10, 14, 6} {
		next := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: price}, Targets: targets})
		if next.Bankrupt || len(ledger.Positions) != 0 || next.Traded != 0 || math.Abs(next.EquityAfter+25) > 1e-9 || len(next.Unfilled) != 0 {
			t.Fatalf("爆仓后应一直空仓：%+v %+v", next, ledger.Positions)
		}
	}
}

// 爆仓时现货也一并卖出，现金为负的部分保持不变。
func TestBankruptcySellsSpotAndFlattensSwap(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 3)
	targets := map[string]float64{spotA: 0.5, swapB: 5}
	ledger.Step(Step{OK: true, Prices: map[string]float64{spotA: 10, swapB: 10}, Targets: targets})
	// 永续跌 25%：现金 50 − 125 = −75，现货市值 50，权益 −25。
	crash := ledger.Step(Step{OK: true, Prices: map[string]float64{spotA: 10, swapB: 7.5}, Targets: targets})
	if !crash.Bankrupt || len(ledger.Positions) != 0 {
		t.Fatalf("爆仓应平掉现货与永续：%+v %+v", crash, ledger.Positions)
	}
	near(t, "现货卖出后现金", ledger.Cash, -25)
	near(t, "期末权益", crash.EquityAfter, -25)
	near(t, "期末现货市值", crash.SpotValue, 0)
	near(t, "期末永续名义价值", crash.SwapValue, 0)
}

// 空账户的零权益不算爆仓；权益为正时不受影响。
func TestEmptyZeroEquityLedgerIsNotBankrupt(t *testing.T) {
	ledger := NewLedger(0, 0, 0, 3)
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 10}, Targets: map[string]float64{swapA: 1}})
	if outcome.Bankrupt || ledger.Bankrupt {
		t.Fatalf("空账户不是爆仓：%+v", outcome)
	}
}
