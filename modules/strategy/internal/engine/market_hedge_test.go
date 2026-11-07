package engine

import (
	"fmt"
	"sort"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// 本文件覆盖多市场模式下多空并存时的取行、冻结与守门：同一个币现货多头加永续空头（对冲币）、永续下架的空头、
// 永续整体不可用时空头规则不守门的条件，以及守门只排除别的规则在不可用市场的持仓。

// longShortTwoColumns 是多头按 m、空头按 v 排名的两规则策略。
const longShortTwoColumns = `name: ls
rules:
  - id: l
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.5}
  - id: s
    type: rank
    side: short
    score: "v"
    select: {top: 1}
    weight: {total: 0.5}
portfolio: {leverage: 2, max_missing: 1}
`

// rowsFrame 按给定的各市场行与证据构造帧：基础集合由调用方给出，每条规则的预期集合都是基础集合。
func rowsFrame(program *dsl.Program, rows map[string]map[string]Row, failed map[string]map[string]map[string]string, universe []string) Frame {
	frame := Frame{Markets: map[string]MarketInput{}, Expected: map[string][]string{}, AgedOut: map[string][]string{}}
	for _, market := range program.Strategy.Market.Markets() {
		frame.Markets[market] = MarketInput{Rows: rows[market], Failed: failed[market]}
	}
	frame.Universe = append([]string(nil), universe...)
	sort.Strings(frame.Universe)
	for _, rule := range program.Rules {
		frame.Expected[rule.Rule.ID] = frame.Universe
	}
	return frame
}

// hedgeState 是对冲币 X 的上期状态：规则 l 在现货持有 X 多头，规则 s 在永续持有 X 空头。
func hedgeState() State {
	return State{Rules: map[string]RuleState{"l": {Held: []string{"X"}}, "s": {Held: []string{"X"}}}, Targets: map[string]string{"X|spot": "0.5", "X|swap": "-0.5"}}
}

// 对冲币 X 的永续证据不全是可冻结的市场不可用时，不只取永续的行：
//   - 永续 NULL 行上 m 列 factor_failed、v 列 market_unavailable（混有逐币失败）；
//   - 永续没有 X 的行，只有 v 列 market_unavailable 证据（证据没覆盖 m 列）。
//
// 两种情况都按 pickOrder 取现货（粘住）的行，现货多头照常、永续空头用现货行继续排名，两腿都保持（不是被一起平掉）。
func TestHedgeCoinKeepsBothLegsWhenSwapEvidenceNotFreezable(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, longShortTwoColumns, "m", "v")
	cases := []struct {
		name   string
		swapX  *Row
		failed map[string]map[string]string
	}{
		{"混合失败的 NULL 行", &Row{Values: map[string]float64{}}, map[string]map[string]string{"m": {"X": FailedFactor}, "v": {"X": FailedMarket}}},
		{"无行且证据不全", nil, map[string]map[string]string{"v": {"X": FailedMarket}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swap := map[string]Row{"A": values("m", 1, "v", 1), "B": values("m", 2, "v", 2)}
			if tc.swapX != nil {
				swap["X"] = *tc.swapX
			}
			rows := map[string]map[string]Row{
				dsl.MarketSpot: {"A": values("m", 1, "v", 1), "B": values("m", 2, "v", 2), "X": values("m", 9, "v", 9)},
				dsl.MarketSwap: swap,
			}
			frame := rowsFrame(program, rows, map[string]map[string]map[string]string{dsl.MarketSwap: tc.failed}, []string{"A", "B", "X"})
			decision, err := Evaluate(program, frame, hedgeState())
			if err != nil {
				t.Fatal(err)
			}
			requireHolds(t, decision, map[string]string{"X|spot": "0.5", "X|swap": "-0.5"})
		})
	}
}

// 对冲币 X（现货多头加永续空头）、本期永续整体不可用：两腿一起冻结（冻结按币，保守的已知边界）。冻结明细按规则方向标上期持仓的市场：
// 多头规则标现货、空头规则标永续。
func TestHedgeCoinFreezesBothLegsWhenSwapDownAndLabelsByDirection(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, longShortTwoColumns, "m", "v")
	rows := map[string]map[string]Row{
		dsl.MarketSpot: {"A": values("m", 1, "v", 1), "B": values("m", 2, "v", 2), "X": values("m", 9, "v", 9)},
		dsl.MarketSwap: {},
	}
	down := map[string]string{"A": FailedMarket, "B": FailedMarket, "X": FailedMarket}
	frame := rowsFrame(program, rows, map[string]map[string]map[string]string{dsl.MarketSwap: {"m": down, "v": down}}, []string{"A", "B", "X"})
	decision, err := Evaluate(program, frame, hedgeState())
	if err != nil {
		t.Fatal(err)
	}
	requireHolds(t, decision, map[string]string{"B|spot": "0.5", "X|spot": "0.5(hold)", "X|swap": "-0.5(hold)"})
	for ruleID, market := range map[string]string{"l": dsl.MarketSpot, "s": dsl.MarketSwap} {
		if item := findItem(t, decision, ruleID, "X"); item.Stage != StageHeld || item.Reason != reasonMarketHold || item.Market != market {
			t.Fatalf("规则 %s 里 X 的冻结明细应是 market_hold、标 %s：%+v", ruleID, market, item)
		}
	}
}

// 永续可用、持有的永续空头 Y 已从永续下架（永续没有行、也没有证据），现货还在：空头规则按无行处理，Y 记 no_row 缺数并平掉，
// 不再借用现货行继续出 Y|swap（与持有的永续多头在永续无行时被平掉对称）。三种含永续下单的模式一样。
func TestDelistedSwapShortIsClosed(t *testing.T) {
	modes := []dsl.Market{
		{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot},
		{Select: dsl.MarketSpot, Prefer: dsl.MarketSwap},
		{Select: dsl.MarketBoth, Prefer: dsl.MarketSwap},
	}
	for _, mode := range modes {
		program := compileIn(t, mode, longShortTwoColumns, "m", "v")
		rows := map[string]map[string]Row{
			dsl.MarketSpot: {"A": values("m", 1, "v", 1), "B": values("m", 2, "v", 2), "Y": values("m", 0, "v", 9)},
			dsl.MarketSwap: {"A": values("m", 1, "v", 1), "B": values("m", 2, "v", 2)},
		}
		frame := rowsFrame(program, rows, nil, []string{"A", "B", "Y"})
		previous := State{Rules: map[string]RuleState{"s": {Held: []string{"Y"}}}, Targets: map[string]string{"Y|swap": "-0.5"}}
		decision, err := Evaluate(program, frame, previous)
		if err != nil {
			t.Fatal(err)
		}
		assertOK(t, decision)
		if _, kept := holdsOf(decision)["Y|swap"]; kept {
			t.Fatalf("%+v：永续下架的空头 Y 应平掉：%v", mode, holdsOf(decision))
		}
		if item := findItem(t, decision, "s", "Y"); item.Stage != StageMissing || item.Reason != "no_row" || item.Market != dsl.MarketSwap {
			t.Fatalf("%+v：空头规则里 Y 应是 missing/no_row、市场 swap：%+v", mode, item)
		}
		if contains(decision.State.Rules["s"].Held, "Y") {
			t.Fatalf("%+v：空头规则的持仓不应再有 Y：%+v", mode, decision.State.Rules["s"])
		}
	}
}

// 对冲币 X 的永续下架（永续没有行、没有证据）：只平掉永续空头，现货多头照常用现货行（多头规则不受影响）。
func TestDelistedSwapShortOfHedgeCoinKeepsSpotLong(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, longShortTwoColumns, "m", "v")
	rows := map[string]map[string]Row{
		dsl.MarketSpot: {"A": values("m", 1, "v", 1), "B": values("m", 2, "v", 2), "X": values("m", 9, "v", 9)},
		dsl.MarketSwap: {"A": values("m", 1, "v", 1), "B": values("m", 2, "v", 2)},
	}
	frame := rowsFrame(program, rows, nil, []string{"A", "B", "X"})
	decision, err := Evaluate(program, frame, hedgeState())
	if err != nil {
		t.Fatal(err)
	}
	requireHolds(t, decision, map[string]string{"X|spot": "0.5", "B|swap": "-0.5"})
}

// shortTopFour 是一条多头、一条最多 4 个币的空头规则。
const shortTopFour = `name: ls4
rules:
  - id: l
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.5}
  - id: s
    type: rank
    side: short
    score: "m"
    select: {top: 4}
    weight: {total: 0.5}
portfolio: {leverage: 2, max_missing: 0.3}
`

// swapDownWithHeldY 构造永续整体不可用的一期：空头规则持有 X1、X2、X3、Y，Y 不在永续本期的基础集合里。
// withY 为 true 时 Y 也带 market_unavailable 证据（input 会给上期在永续持仓的标的补上证据）。
func swapDownWithHeldY(program *dsl.Program, withY bool) (Frame, State) {
	spot := map[string]Row{}
	var universe []string
	for i, id := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "X1", "X2", "X3", "Y"} {
		spot[id] = values("m", i+1)
		universe = append(universe, id)
	}
	down := map[string]string{"A": FailedMarket, "X1": FailedMarket, "X2": FailedMarket, "X3": FailedMarket}
	if withY {
		down["Y"] = FailedMarket
	}
	frame := rowsFrame(program, map[string]map[string]Row{dsl.MarketSpot: spot, dsl.MarketSwap: {}}, map[string]map[string]map[string]string{dsl.MarketSwap: {"m": down}}, universe)
	previous := State{Rules: map[string]RuleState{"s": {Held: []string{"X1", "X2", "X3", "Y"}}}, Targets: map[string]string{"X1|swap": "-0.125", "X2|swap": "-0.125", "X3|swap": "-0.125", "Y|swap": "-0.125"}}
	return frame, previous
}

// 永续整体不可用、持有的永续空头 Y 有 market_unavailable 证据：四个空头都冻结，空头规则不守门，多头照常在现货开仓。
func TestSwapDownFreezesEveryHeldShort(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, shortTopFour, "m")
	frame, previous := swapDownWithHeldY(program, true)
	decision, err := Evaluate(program, frame, previous)
	if err != nil {
		t.Fatal(err)
	}
	requireHolds(t, decision, map[string]string{"H|spot": "0.5", "X1|swap": "-0.125(hold)", "X2|swap": "-0.125(hold)", "X3|swap": "-0.125(hold)", "Y|swap": "-0.125(hold)"})
}

// 同上，但 Y 没有证据（证据没覆盖到）：Y 会借用现货行，空头规则的可用集合不为空，不放开守门；照常守门整期 too_many_missing，
// 而不是让 Y 在现货行上重新排名（-0.125 变 -0.5）。
func TestSwapDownGuardsShortRuleWhenHeldShortLacksEvidence(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, shortTopFour, "m")
	frame, previous := swapDownWithHeldY(program, false)
	decision, err := Evaluate(program, frame, previous)
	if err != nil {
		t.Fatal(err)
	}
	assertSkipped(t, decision, SkipTooManyMissing)
}

// 规则 l1 预期 5 个币，其中 G1、G2 由规则 l2 在现货持有、本期只缺上一根（prev_hold）：不管永续可不可用，l1 都把它们计入缺数，
// 2/5 超过 max_missing 0.2，整期 too_many_missing。守门只排除别的规则持有、因市场不可用（market_hold）冻结的标的。
func TestGuardCountsOtherRulesPrevHoldsRegardlessOfSwap(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, `name: ll
rules:
  - id: l1
    type: rank
    score: "m + bars[-1].m"
    select: {top: 1}
    weight: {total: 0.5}
  - id: l2
    type: rank
    score: "m + bars[-1].m"
    select: {top: 2}
    weight: {total: 0.5}
portfolio: {max_missing: 0.2}
`, "m")
	big := []string{"A", "B", "C", "G1", "G2"}
	for i := 0; i < 15; i++ {
		big = append(big, fmt.Sprintf("Z%02d", i))
	}
	for _, swapDown := range []bool{false, true} {
		// G1、G2 当期齐全、只缺上一根；其余币当期与上一根都有值。
		spot := map[string]Row{"G1": {Values: map[string]float64{"m": 1}, Previous: map[string]float64{}}, "G2": {Values: map[string]float64{"m": 1}, Previous: map[string]float64{}}}
		swap := map[string]Row{}
		for i, id := range big {
			if id != "G1" && id != "G2" {
				row := Row{Values: map[string]float64{"m": float64(i)}, Previous: map[string]float64{"m": float64(i)}}
				spot[id], swap[id] = row, row
			}
		}
		failed := map[string]map[string]map[string]string{}
		if swapDown {
			down := map[string]string{}
			for id := range swap {
				down[id] = FailedMarket
			}
			swap = map[string]Row{}
			failed[dsl.MarketSwap] = map[string]map[string]string{"m": down}
		}
		frame := rowsFrame(program, map[string]map[string]Row{dsl.MarketSpot: spot, dsl.MarketSwap: swap}, failed, big)
		frame.Expected["l1"] = []string{"A", "B", "C", "G1", "G2"}
		previous := State{Rules: map[string]RuleState{"l2": {Held: []string{"G1", "G2"}}}, Targets: map[string]string{"G1|spot": "0.25", "G2|spot": "0.25"}}
		decision, err := Evaluate(program, frame, previous)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Status != StatusSkipped || decision.SkipReason != SkipTooManyMissing {
			t.Fatalf("swapDown=%v：期望 too_many_missing，实际 %s(%s) %v", swapDown, decision.Status, decision.SkipReason, decision.Summary.Notes)
		}
		if got := decision.Summary.Rules["l1"]; got.Held != 2 || got.Missing != 0 {
			t.Fatalf("swapDown=%v：G1、G2 应是冻结（prev_hold）而不是普通缺数：%+v", swapDown, got)
		}
		if item := findItem(t, decision, "l1", "G1"); item.Stage != StageHeld || item.Reason != reasonPrevHold {
			t.Fatalf("swapDown=%v：G1 的明细应是 held/prev_hold：%+v", swapDown, item)
		}
	}
}

// holdingShort 是一条多头、一条分 3 批持有的空头规则。
const holdingShort = `name: lsh
rules:
  - id: l
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.5}
  - id: s
    type: rank
    side: short
    score: "m"
    select: {top: 1}
    weight: {total: 0.5}
    holding: {bars: 3, offsets: [0, 1, 2]}
portfolio: {leverage: 2, max_missing: 1}
`

// threeCoins 构造现货 A、B、C 的一期；swapDown 为 true 时永续整体不可用，否则永续与现货同值。
func threeCoins(program *dsl.Program, bar int64, swapDown bool) Frame {
	spot := map[string]Row{"A": values("m", 1), "B": values("m", 2), "C": values("m", 3)}
	swap := map[string]Row{"A": values("m", 1), "B": values("m", 2), "C": values("m", 3)}
	var failed map[string]map[string]map[string]string
	if swapDown {
		swap = map[string]Row{}
		failed = map[string]map[string]map[string]string{dsl.MarketSwap: {"m": {"A": FailedMarket, "B": FailedMarket, "C": FailedMarket}}}
	}
	frame := rowsFrame(program, map[string]map[string]Row{dsl.MarketSpot: spot, dsl.MarketSwap: swap}, failed, []string{"A", "B", "C"})
	frame.BarIndex = bar
	return frame
}

// holding 空头规则不守门的一期不重建批次：
//   - 启用后的第一根 ok bar 正好永续整体不可用：不建空批次，永续恢复的下一期把三个批次一起建上，空头配满 -0.5（不是 1/3）；
//   - 批次已建立、永续不可用的一期正好轮到一个批次重建：沿用上期批次，永续恢复后补建，空头仍是 -0.5。
func TestWaivedHoldingShortRuleKeepsBatches(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, holdingShort, "m")
	evaluate := func(frame Frame, previous State) Decision {
		t.Helper()
		decision, err := Evaluate(program, frame, previous)
		if err != nil {
			t.Fatal(err)
		}
		assertOK(t, decision)
		return decision
	}
	requireFullShort := func(decision Decision) {
		t.Helper()
		if got := holdsOf(decision)["C|swap"]; got != "-0.5" {
			t.Fatalf("永续恢复后空头应配满：%v", holdsOf(decision))
		}
		batches := decision.State.Rules["s"].Batches
		if len(batches) != 3 {
			t.Fatalf("应有三个批次：%+v", batches)
		}
		for _, batch := range batches {
			if batch.BaseWeights["C"] != "1" {
				t.Fatalf("每个批次都应持有 C：%+v", batches)
			}
		}
	}

	first := evaluate(threeCoins(program, 5, true), State{})
	if batches := first.State.Rules["s"].Batches; len(batches) != 0 {
		t.Fatalf("不守门的一期不应建批次：%+v", batches)
	}
	requireFullShort(evaluate(threeCoins(program, 6, false), first.State))

	established := evaluate(threeCoins(program, 3, false), State{})
	requireFullShort(established)
	down := evaluate(threeCoins(program, 4, true), established.State)
	if got := holdsOf(down)["C|swap"]; got != "-0.5(hold)" {
		t.Fatalf("永续不可用时空头 C 应冻结：%v", holdsOf(down))
	}
	requireFullShort(evaluate(threeCoins(program, 5, false), down.State))
}
