package ledger

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Step 与 Mark + Rebalance 逐位一致：同一组多期输入（含 skipped 期、缺价清算、现货与永续、计资金费与不计）分别推进两本账，
// 每期的 Outcome 与账本 JSON 必须完全相同。
func TestStepEqualsMarkThenRebalance(t *testing.T) {
	steps := []Step{
		{OK: true, Prices: map[string]float64{spotA: 10, swapA: 10, swapB: 4}, Targets: map[string]float64{spotA: 0.4, swapA: -0.3, swapB: 0.5}},
		{OK: true, Prices: map[string]float64{spotA: 11, swapA: 9, swapB: 5}, Targets: map[string]float64{spotA: 0.2, swapA: -0.6}, Funding: map[string]float64{swapA: 0.001, swapB: -0.002}},
		{OK: false, Prices: map[string]float64{spotA: 12, swapA: 8}, Funding: map[string]float64{}},
		{OK: true, Prices: map[string]float64{swapA: 10}, Targets: map[string]float64{swapA: 0.5}, Funding: map[string]float64{swapA: 0.0005}},
		{OK: true, Prices: map[string]float64{swapA: 10}, Targets: map[string]float64{swapA: 0.5}},
		{OK: true, Prices: map[string]float64{swapA: 7, spotA: 9}, Targets: map[string]float64{spotA: 1}},
	}
	for _, funded := range []bool{false, true} {
		stepped, split := NewLedger(100, 10, 5, 2), NewLedger(100, 10, 5, 2)
		for i, step := range steps {
			if !funded {
				step.Funding = nil
			}
			want := stepped.Step(step)
			marked, err := split.Mark(step.Prices, step.Funding)
			if err != nil {
				t.Fatal(err)
			}
			got, err := split.Rebalance(marked, step)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("资金费 %v 第 %d 期 Outcome 不一致：Step %+v，Mark+Rebalance %+v", funded, i, want, got)
			}
			a, _ := json.Marshal(stepped)
			b, _ := json.Marshal(split)
			if string(a) != string(b) {
				t.Fatalf("资金费 %v 第 %d 期账本不一致：\n%s\n%s", funded, i, a, b)
			}
			if !funded && strings.Contains(string(a), "funding_basis") {
				t.Fatalf("不计资金费时账本不能多出快照：%s", a)
			}
		}
	}
}

func TestMarkTwiceInOneBarFails(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 3)
	prices := map[string]float64{swapA: 10}
	outcome, err := ledger.Mark(prices, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Mark(prices, nil); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("同一根重复 Mark 应报错：%v", err)
	}
	if _, err := ledger.Rebalance(outcome, Step{OK: true, Prices: prices, Targets: map[string]float64{swapA: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Rebalance(outcome, Step{OK: false}); err == nil || !strings.Contains(err.Error(), "先调用 Mark") {
		t.Fatalf("没有估值就调仓应报错：%v", err)
	}
	// 调仓之后进入下一根，可以再次估值。
	if _, err := ledger.Mark(prices, nil); err != nil {
		t.Fatalf("下一根估值：%v", err)
	}
}

// 资金费结算（手算）：第 1 根收盘开多 10 个（名义 100）；第 2 根费率按第 1 根调仓之前的持仓结算（空仓，不付），
// 收盘把多头换成空头 −5 个（价格 12，名义 −60）；第 3 根费率按第 2 根调仓之前的持仓（多 10 个，按第 2 根收盘价 12 折算，名义 120）结算。
func TestFundingSettlesOnPreviousPreRebalanceHoldings(t *testing.T) {
	cases := map[string]struct {
		rate, want float64
	}{
		"费率为正多头付": {0.001, 0.12},
		"费率为负多头收": {-0.002, -0.24},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ledger := NewLedger(100, 0, 0, 3)
			first := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 10}, Targets: map[string]float64{swapA: 1}, Funding: map[string]float64{swapA: tc.rate}})
			near(t, "第 1 根没有快照，不结算", first.Funding, 0)
			if len(ledger.FundingBasis) != 0 {
				t.Fatalf("第 1 根估值时还是空仓，快照为空：%v", ledger.FundingBasis)
			}
			// 第 2 根：价格 12，多头盈亏 20 进现金，权益 120；快照里没有仓位（第 1 根调仓前空仓），新开的多头不付。
			second := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 12}, Targets: map[string]float64{swapA: -0.5}, Funding: map[string]float64{swapA: tc.rate}})
			near(t, "新开的仓位不付", second.Funding, 0)
			near(t, "第 2 根成交前权益", second.EquityBefore, 120)
			near(t, "快照是调仓前的多头名义价值", ledger.FundingBasis[swapA], 120)
			// 第 3 根：价格不变；按快照 120 × 费率结算，虽然多头已在第 2 根收盘换成空头。
			third := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 12}, Targets: map[string]float64{swapA: -0.5}, Funding: map[string]float64{swapA: tc.rate}})
			near(t, "按上一根调仓前的持仓结算", third.Funding, tc.want)
			near(t, "资金费计入成交前权益", third.EquityBefore, 120-tc.want)
			near(t, "新快照是空头", ledger.FundingBasis[swapA], -60)
			// 第 4 根：空头按 −60 结算，费率为正时空头收。
			fourth := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 12}, Targets: map[string]float64{swapA: -0.5}, Funding: map[string]float64{swapA: tc.rate}})
			near(t, "空头方向相反", fourth.Funding, -60*tc.rate)
		})
	}
}

// 费率为空（本根没有值、NaN）时不结算，但快照照常更新；平掉的仓位下一根仍按快照付。
func TestFundingEmptyRateAndClosedPosition(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 3)
	ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 10}, Targets: map[string]float64{swapA: 1}, Funding: map[string]float64{}})
	empty := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 10}, Targets: map[string]float64{}, Funding: map[string]float64{}})
	near(t, "本根没有费率不结算", empty.Funding, 0)
	if _, held := ledger.Positions[swapA]; held {
		t.Fatal("多头应已平掉")
	}
	near(t, "快照仍是调仓前的多头", ledger.FundingBasis[swapA], 100)
	if keys := ledger.FundingKeys(); !reflect.DeepEqual(keys, []string{swapA}) {
		t.Fatalf("平掉的仓位仍要读费率：%v", keys)
	}
	nan := ledger.Step(Step{OK: true, Prices: map[string]float64{}, Funding: map[string]float64{swapA: nanRate()}})
	near(t, "费率无效按 0", nan.Funding, 0)
	if len(ledger.FundingBasis) != 0 {
		t.Fatalf("空仓后快照清空：%v", ledger.FundingBasis)
	}
	// 平掉后的那一根：快照是上一根（已平仓）的 100，费率有效时照付。
	again := NewLedger(100, 0, 0, 3)
	again.Step(Step{OK: true, Prices: map[string]float64{swapA: 10}, Targets: map[string]float64{swapA: 1}, Funding: map[string]float64{}})
	again.Step(Step{OK: true, Prices: map[string]float64{swapA: 10}, Funding: map[string]float64{}})
	paid := again.Step(Step{OK: true, Prices: map[string]float64{}, Funding: map[string]float64{swapA: 0.01}})
	near(t, "收盘平掉的仓位仍要付", paid.Funding, 1)
	near(t, "现金扣掉资金费", again.Cash, 99)
}

func nanRate() float64 {
	zero := 0.0
	return zero / zero
}
