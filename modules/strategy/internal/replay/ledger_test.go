package replay

import (
	"math"
	"reflect"
	"testing"
)

func near(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %.9f，期望 %.9f", label, got, want)
	}
}

// heldLedger 构造 A、B 各持 50、现金 0 的账本。
func heldLedger(feeBps float64) *Ledger {
	ledger := NewLedger(0, feeBps, 3)
	ledger.Positions["A"] = &Position{Quantity: 5, LastPrice: 10, Value: 50}
	ledger.Positions["B"] = &Position{Quantity: 50, LastPrice: 1, Value: 50}
	return ledger
}

// S22 算例一：A 缺价冻结、目标全仓 B：B 不能增持，现金不为负，记录目标未达成，手续费只按实际成交额。
func TestS22FrozenHoldingDoesNotFundPurchases(t *testing.T) {
	ledger := heldLedger(10)
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{"B": 1}, Targets: map[string]float64{"B": 1}})
	near(t, "成交前权益", outcome.EquityBefore, 100)
	near(t, "B 的市值", ledger.Positions["B"].Value, 50)
	near(t, "A 的市值", ledger.Positions["A"].Value, 50)
	if !ledger.Positions["A"].Frozen {
		t.Fatal("A 应冻结")
	}
	if ledger.Cash < 0 {
		t.Fatalf("现金不能为负：%f", ledger.Cash)
	}
	near(t, "成交额", outcome.Traded, 0)
	near(t, "手续费", outcome.Fee, 0)
	if !reflect.DeepEqual(outcome.Unfilled, []string{"A", "B"}) {
		t.Fatalf("应记录 A（冻结）与 B（资金不足）目标未达成：%v", outcome.Unfilled)
	}
}

// S22 算例二：A 冻结 50，B 目标 20%：B 卖到 20 而不是 10，目标分母是总权益。
func TestS22TargetsUseTotalEquityDenominator(t *testing.T) {
	ledger := heldLedger(0)
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{"B": 1}, Targets: map[string]float64{"B": 0.2}})
	near(t, "B 的市值", ledger.Positions["B"].Value, 20)
	near(t, "现金", ledger.Cash, 30)
	near(t, "成交额", outcome.Traded, 30)
	near(t, "换手", outcome.Turnover, 0.3)
	near(t, "成交后权益", outcome.EquityAfter, 100)
}

// 带费用的全仓建仓与全量换仓：资金守恒、现金不为负、费用只按实际成交额。
func TestLedgerFullRebalanceConservesCapital(t *testing.T) {
	ledger := NewLedger(100, 10, 3)
	first := ledger.Step(Step{OK: true, Prices: map[string]float64{"A": 10, "B": 5}, Targets: map[string]float64{"A": 0.5, "B": 0.5}})
	if ledger.Cash < 0 {
		t.Fatalf("现金不能为负：%f", ledger.Cash)
	}
	near(t, "首期成交额", first.Traded, ledger.Positions["A"].Value+ledger.Positions["B"].Value)
	near(t, "首期手续费", first.Fee, first.Traded*0.001)
	near(t, "首期资金守恒", ledger.Cash+ledger.Positions["A"].Value+ledger.Positions["B"].Value+first.Fee, 100)
	near(t, "A 与 B 等权", ledger.Positions["A"].Value, ledger.Positions["B"].Value)
	// 全量换仓到 C。
	equity := ledger.Equity()
	second := ledger.Step(Step{OK: true, Prices: map[string]float64{"A": 10, "B": 5, "C": 2}, Targets: map[string]float64{"C": 1}})
	if _, ok := ledger.Positions["A"]; ok {
		t.Fatal("A 应清空")
	}
	if ledger.Cash < 0 {
		t.Fatalf("现金不能为负：%f", ledger.Cash)
	}
	near(t, "换仓后资金守恒", ledger.Equity()+second.Fee, equity)
	near(t, "换仓费用", second.Fee, second.Traded*0.001)
	if len(second.Unfilled) != 0 {
		t.Fatalf("资金足够时不应有未达成：%v", second.Unfilled)
	}
}

// 目标不变但价格漂移后再平衡：三期已知价格，断言持仓、现金、收益与换手。
func TestLedgerRebalancesAfterPriceDrift(t *testing.T) {
	ledger := NewLedger(100, 0, 3)
	ledger.Step(Step{OK: true, Prices: map[string]float64{"A": 10, "B": 10}, Targets: map[string]float64{"A": 0.5, "B": 0.5}})
	near(t, "A 数量", ledger.Positions["A"].Quantity, 5)
	drift := ledger.Step(Step{OK: true, Prices: map[string]float64{"A": 20, "B": 10}, Targets: map[string]float64{"A": 0.5, "B": 0.5}})
	near(t, "漂移后成交前权益", drift.EquityBefore, 150)
	near(t, "A 再平衡到 75", ledger.Positions["A"].Value, 75)
	near(t, "B 再平衡到 75", ledger.Positions["B"].Value, 75)
	near(t, "再平衡成交额", drift.Traded, 50)
	near(t, "再平衡换手", drift.Turnover, 50.0/150)
	third := ledger.Step(Step{OK: true, Prices: map[string]float64{"A": 20, "B": 5}, Targets: map[string]float64{"A": 0.5, "B": 0.5}})
	near(t, "第三期成交前权益", third.EquityBefore, 112.5)
	near(t, "现金", ledger.Cash, 0)
}

// S27：连续缺价，第 K 期恰好 skipped：不成交、不清算、只推进计数；下一 ok 期仍无价则按最后价清算。
func TestS27LiquidationOnlyOnOkBars(t *testing.T) {
	ledger := NewLedger(0, 10, 3)
	ledger.Positions["A"] = &Position{Quantity: 10, LastPrice: 10, Value: 100}
	for bar := 1; bar <= 2; bar++ {
		outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{}, Targets: map[string]float64{"A": 1}})
		if len(outcome.Liquidated) != 0 || ledger.Positions["A"].MissingBars != bar {
			t.Fatalf("第 %d 期不应清算：%+v %+v", bar, outcome, ledger.Positions["A"])
		}
	}
	skipped := ledger.Step(Step{OK: false, Prices: map[string]float64{}})
	if len(skipped.Liquidated) != 0 || ledger.Positions["A"].MissingBars != 3 || skipped.Traded != 0 {
		t.Fatalf("skipped 期只推进计数：%+v %+v", skipped, ledger.Positions["A"])
	}
	liquidated := ledger.Step(Step{OK: true, Prices: map[string]float64{}, Targets: map[string]float64{"A": 1}})
	if !reflect.DeepEqual(liquidated.Liquidated, []string{"A"}) {
		t.Fatalf("下一 ok 期仍无价应清算：%+v", liquidated)
	}
	near(t, "按最后价清算后的现金", ledger.Cash, 100*(1-0.001))
	near(t, "清算费用", liquidated.Fee, 0.1)
	if _, ok := ledger.Positions["A"]; ok {
		t.Fatal("清算后不应再持有 A")
	}
}

// S27：重新有价则回到可成交并清零计数，按当期目标处理。
func TestS27PriceReturnResetsCounter(t *testing.T) {
	ledger := NewLedger(0, 0, 3)
	ledger.Positions["A"] = &Position{Quantity: 10, LastPrice: 10, Value: 100}
	ledger.Step(Step{OK: true, Prices: map[string]float64{}, Targets: map[string]float64{"A": 1}})
	ledger.Step(Step{OK: true, Prices: map[string]float64{}, Targets: map[string]float64{"A": 1}})
	ledger.Step(Step{OK: false, Prices: map[string]float64{}})
	back := ledger.Step(Step{OK: true, Prices: map[string]float64{"A": 12}, Targets: map[string]float64{"A": 0.5}})
	position := ledger.Positions["A"]
	if position == nil || position.Frozen || position.MissingBars != 0 || len(back.Liquidated) != 0 {
		t.Fatalf("重新有价应回到可成交并清零计数：%+v %+v", position, back)
	}
	near(t, "按当期目标减仓到一半", position.Value, 60)
	near(t, "现金", ledger.Cash, 60)
}

// 冻结持仓的调仓记为未成交；下期有价后执行。
func TestFrozenRebalanceExecutesWhenPriceReturns(t *testing.T) {
	ledger := heldLedger(0)
	frozen := ledger.Step(Step{OK: true, Prices: map[string]float64{"B": 1}, Targets: map[string]float64{"B": 0.5}})
	if !reflect.DeepEqual(frozen.Unfilled, []string{"A"}) {
		t.Fatalf("冻结的 A 应记为未成交：%v", frozen.Unfilled)
	}
	near(t, "A 保留", ledger.Positions["A"].Value, 50)
	resumed := ledger.Step(Step{OK: true, Prices: map[string]float64{"A": 10, "B": 1}, Targets: map[string]float64{"B": 0.5}})
	if _, ok := ledger.Positions["A"]; ok || len(resumed.Unfilled) != 0 {
		t.Fatalf("A 有价后应卖出：%+v %+v", ledger.Positions, resumed)
	}
	near(t, "现金", ledger.Cash, 50)
}

// 买单缩减后费用只按实际成交额计。
func TestScaledBuysChargeFeesOnActualTrades(t *testing.T) {
	ledger := NewLedger(10, 100, 3)
	ledger.Positions["A"] = &Position{Quantity: 9, LastPrice: 10, Value: 90}
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{"B": 1}, Targets: map[string]float64{"A": 0.9, "B": 0.5}})
	if outcome.BuyScale <= 0 || outcome.BuyScale >= 1 {
		t.Fatalf("应按比例缩减买单：%+v", outcome)
	}
	near(t, "费用等于实际成交额乘费率", outcome.Fee, outcome.Traded*0.01)
	if ledger.Cash < 0 {
		t.Fatalf("现金不能为负：%f", ledger.Cash)
	}
}
