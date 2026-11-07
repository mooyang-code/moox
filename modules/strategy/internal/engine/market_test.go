package engine

import (
	"sort"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

const topOneRule = `name: m
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: 1
`

// marketFrame 构造两个市场的帧：rows 是 市场 → 标的 → m 值，failed 是 市场 → 列 → 标的 → 原因。基础集合是策略选币市场里出现的标的
// （select 为 both 时是两个市场的并集），每条规则的预期集合等于基础集合。
func marketFrame(program *dsl.Program, rows map[string]map[string]float64, failed map[string]map[string]map[string]string) Frame {
	frame := Frame{Markets: map[string]MarketInput{}, Expected: map[string][]string{}, AgedOut: map[string][]string{}}
	selectMarkets := map[string]bool{}
	for _, market := range program.Strategy.Market.Markets() {
		if program.Strategy.Market.Select == dsl.MarketBoth || program.Strategy.Market.Select == market {
			selectMarkets[market] = true
		}
	}
	universe := map[string]struct{}{}
	for _, market := range program.Strategy.Market.Markets() {
		input := MarketInput{Rows: map[string]Row{}, Failed: failed[market]}
		for id, value := range rows[market] {
			input.Rows[id] = values("m", value)
			if selectMarkets[market] {
				universe[id] = struct{}{}
			}
		}
		for _, subjects := range failed[market] {
			for id := range subjects {
				if selectMarkets[market] {
					universe[id] = struct{}{}
				}
			}
		}
		frame.Markets[market] = input
	}
	for id := range universe {
		frame.Universe = append(frame.Universe, id)
	}
	sort.Strings(frame.Universe)
	for _, rule := range program.Rules {
		frame.Expected[rule.Rule.ID] = frame.Universe
	}
	return frame
}

// keysOf 返回目标按（标的|市场）作键的权重。
func keysOf(decision Decision) map[string]string {
	out := map[string]string{}
	for _, target := range decision.Targets {
		out[TargetKey(target.InstrumentID, target.Market)] = target.Weight.String()
	}
	return out
}

func requireTargets(t *testing.T, decision Decision, want map[string]string) {
	t.Helper()
	assertOK(t, decision)
	got := keysOf(decision)
	if len(got) != len(want) {
		t.Fatalf("目标不符：got %v want %v", got, want)
	}
	for key, weight := range want {
		if got[key] != weight {
			t.Fatalf("目标不符：got %v want %v", got, want)
		}
	}
}

// 五种市场模式各一组：同一份数据在不同模式下取行、选币、定市场的结果不同。
func TestFiveMarketModes(t *testing.T) {
	rows := map[string]map[string]float64{
		dsl.MarketSpot: {"A": 10, "B": 5},
		dsl.MarketSwap: {"A": 1, "C": 7},
	}
	cases := []struct {
		name string
		mode dsl.Market
		want map[string]string
	}{
		{"纯现货", dsl.SingleMarket(dsl.MarketSpot), map[string]string{"A|spot": "1"}},
		{"纯永续", dsl.SingleMarket(dsl.MarketSwap), map[string]string{"C|swap": "1"}},
		{"两边选现货优先", dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, map[string]string{"A|spot": "1"}},
		{"两边选永续优先", dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSwap}, map[string]string{"C|swap": "1"}},
		{"现货选币永续下单", dsl.Market{Select: dsl.MarketSpot, Prefer: dsl.MarketSwap}, map[string]string{"A|swap": "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			program := compileIn(t, tc.mode, topOneRule, "m")
			use := map[string]map[string]float64{}
			for _, market := range tc.mode.Markets() {
				use[market] = rows[market]
			}
			decision, err := Evaluate(program, marketFrame(program, use, nil), State{})
			if err != nil {
				t.Fatal(err)
			}
			requireTargets(t, decision, tc.want)
			if item := findItem(t, decision, "r", decision.Targets[0].InstrumentID); item.Market != decision.Targets[0].Market {
				t.Fatalf("明细的市场应是入选权重进入的市场：%+v", item)
			}
		})
	}
}

// 粘住：上期持有正权重目标的币，本期仍沿用那个市场的行和市场，即使 prefer 是另一个。
func TestStickyMarketFollowsPositiveHolding(t *testing.T) {
	mode := dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSwap}
	program := compileIn(t, mode, topOneRule, "m")
	rows := map[string]map[string]float64{dsl.MarketSpot: {"A": 10}, dsl.MarketSwap: {"A": 1, "C": 7}}
	previous := State{Targets: map[string]string{"A|spot": "1"}}
	decision, err := Evaluate(program, marketFrame(program, rows, nil), previous)
	if err != nil {
		t.Fatal(err)
	}
	// A 持仓在现货，取现货行（10）胜过 C（7），仍落在现货。
	requireTargets(t, decision, map[string]string{"A|spot": "1"})
}

// 上期在永续上的空头不会把本期多头粘到永续上；空头一律永续。
func TestShortHoldingDoesNotStickLongs(t *testing.T) {
	mode := dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}
	program := compileIn(t, mode, topOneRule, "m")
	rows := map[string]map[string]float64{dsl.MarketSpot: {"A": 10}, dsl.MarketSwap: {"A": 10}}
	previous := State{Targets: map[string]string{"A|swap": "-0.5"}}
	decision, err := Evaluate(program, marketFrame(program, rows, nil), previous)
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"A|spot": "1"})
}

// 现货与永续不互相抵消：多头现货、空头永续同时存在。
func TestSpotLongAndSwapShortDoNotNet(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, `name: m
rules:
  - id: l
    type: rank
    score: "m"
    select: {top: 1}
    weight: 0.5
  - id: s
    type: rank
    side: short
    score: "m"
    select: {top: 1}
    weight: 0.5
portfolio: {leverage: 2}
`, "m")
	rows := map[string]map[string]float64{dsl.MarketSpot: {"A": 10}, dsl.MarketSwap: {"A": 10}}
	decision, err := Evaluate(program, marketFrame(program, rows, nil), State{})
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"A|spot": "0.5", "A|swap": "-0.5"})
	assertDecimal(t, "gross", decision.Summary.Gross, "1")
	assertDecimal(t, "net", decision.Summary.Net, "0")
	assertDecimal(t, "spot_net", decision.Summary.SpotNet, "0.5")
	assertDecimal(t, "swap_net", decision.Summary.SwapNet, "-0.5")
	if decision.Summary.Cash != "" {
		t.Fatalf("混合市场不显示现金：%q", decision.Summary.Cash)
	}
}

// max_weight 按币合计：同一个币现货、永续权重绝对值之和超过上限时两边按比例缩小；只有一边时直接取上限。
func TestMaxWeightCapsPerCoinAcrossMarkets(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, `name: m
rules:
  - id: l
    type: rank
    score: "m"
    select: {top: 1}
    weight: 0.4
  - id: s
    type: rank
    side: short
    score: "m"
    select: {top: 1}
    weight: 0.4
portfolio: {leverage: 2, max_weight: 0.3}
`, "m")
	rows := map[string]map[string]float64{dsl.MarketSpot: {"A": 10}, dsl.MarketSwap: {"A": 10}}
	decision, err := Evaluate(program, marketFrame(program, rows, nil), State{})
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"A|spot": "0.15", "A|swap": "-0.15"})

	single := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, `name: m
rules:
  - id: l
    type: rank
    score: "m"
    select: {top: 1}
    weight: 0.4
portfolio: {max_weight: 0.3}
`, "m")
	decision, err = Evaluate(single, marketFrame(single, rows, nil), State{})
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"A|spot": "0.3"})
}

// 现货预算不超过 1：leverage 大于 1 时，现货部分超出的按比例缩小，多出的留现金。
func TestSpotBudgetScalesSpotOnly(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, `name: m
rules:
  - id: l
    type: rank
    score: "m"
    select: {top: 2}
    weight: 1.5
portfolio: {leverage: 2}
`, "m")
	rows := map[string]map[string]float64{dsl.MarketSpot: {"A": 10, "B": 9}, dsl.MarketSwap: {"A": 10}}
	decision, err := Evaluate(program, marketFrame(program, rows, nil), State{})
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"A|spot": "0.5", "B|spot": "0.5"})
}

// 空头候选只保留本期永续可用的币：只有现货的币不会被做空。
func TestShortCandidatesRequireSwapAvailability(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, `name: m
rules:
  - id: s
    type: rank
    side: short
    score: "m"
    select: {bottom: 1}
    weight: 0.5
portfolio: {leverage: 1, max_missing: 1}
`, "m")
	// D 只有现货且分数最低，不能被做空；B 有永续，成为空头。
	rows := map[string]map[string]float64{dsl.MarketSpot: {"D": 1, "B": 2, "C": 3}, dsl.MarketSwap: {"B": 2, "C": 3}}
	decision, err := Evaluate(program, marketFrame(program, rows, nil), State{})
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"B|swap": "-0.5"})
}

// 一个市场整体不可用（因子本期被跳过）时，持仓在这个市场里的币冻结，不被卖出；新开仓用另一个市场。
func TestUnavailableMarketFreezesHoldings(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSwap}, topOneRule+"portfolio: {max_missing: 1}\n", "m")
	rows := map[string]map[string]float64{dsl.MarketSpot: {"B": 5}, dsl.MarketSwap: {}}
	unavailable := map[string]map[string]map[string]string{dsl.MarketSwap: {"m": {"A": FailedMarket, "B": FailedMarket}}}
	frame := marketFrame(program, rows, unavailable)
	frame.Markets[dsl.MarketSwap] = MarketInput{Rows: map[string]Row{}, Failed: unavailable[dsl.MarketSwap]}
	previous := State{Targets: map[string]string{"A|swap": "0.4"}}
	decision, err := Evaluate(program, frame, previous)
	if err != nil {
		t.Fatal(err)
	}
	assertOK(t, decision)
	got := keysOf(decision)
	if got["A|swap"] != "0.4" {
		t.Fatalf("永续不可用时持仓应冻结保持：%v", got)
	}
	var hold bool
	for _, target := range decision.Targets {
		if target.InstrumentID == "A" && target.Hold {
			hold = true
		}
	}
	if !hold {
		t.Fatalf("A 应是保持目标：%+v", decision.Targets)
	}
	if got["B|spot"] == "" {
		t.Fatalf("新开仓应使用现货：%v", got)
	}
}

// 冻结按标的展开成上期所有非零键：同一个币现货、永续都有持仓时两边一起保持。
func TestFreezeExpandsToAllNonZeroKeys(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSwap}, topOneRule+"portfolio: {max_missing: 1}\n", "m")
	unavailable := map[string]map[string]map[string]string{
		dsl.MarketSpot: {"m": {"A": FailedMarket}},
		dsl.MarketSwap: {"m": {"A": FailedMarket}},
	}
	frame := marketFrame(program, map[string]map[string]float64{}, unavailable)
	previous := State{Targets: map[string]string{"A|spot": "0.2", "A|swap": "-0.1"}}
	decision, err := Evaluate(program, frame, previous)
	if err != nil {
		t.Fatal(err)
	}
	assertOK(t, decision)
	got := keysOf(decision)
	if got["A|spot"] != "0.2" || got["A|swap"] != "-0.1" || len(got) != 2 {
		t.Fatalf("两个键都应保持：%v", got)
	}
}

// 单市场策略的目标带市场、排序现货在前、状态用（标的|市场）作键。
func TestTargetsCarryMarketAndStateKeys(t *testing.T) {
	program := compile(t, topOneRule, "m")
	decision := evaluate(t, program, frameOf(program, map[string]Row{"A": values("m", 3), "B": values("m", 1)}), State{})
	assertOK(t, decision)
	if len(decision.Targets) != 1 || decision.Targets[0].Market != testMarket || decision.State.Targets["A|swap"] != "1" {
		t.Fatalf("目标与状态不符：%+v %v", decision.Targets, decision.State.Targets)
	}
}

// 现货独有且年龄不够的新币：空头候选过滤后它不再算年龄剔除，不会把“预期 − 年龄剔除”算成 0 而让整期跳过（多头腿也不受影响）。
func TestShortCandidateFilterAlsoDropsAgedOutSubjects(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, `name: m
rules:
  - id: l
    type: rank
    score: "m"
    select: {top: 1}
    weight: 0.5
  - id: s
    type: rank
    side: short
    score: "m"
    select: {bottom: 1}
    weight: 0.5
portfolio: {leverage: 1, max_missing: 1}
`, "m")
	rows := map[string]map[string]float64{dsl.MarketSpot: {"B": 5, "D": 9}, dsl.MarketSwap: {"B": 5}}
	frame := marketFrame(program, rows, nil)
	// D 是现货独有的新币，年龄探针没通过：两条规则都把它记为年龄剔除。
	frame.AgedOut = map[string][]string{"l": {"D"}, "s": {"D"}}
	decision, err := Evaluate(program, frame, State{})
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"B|spot": "0.5", "B|swap": "-0.5"})
	if rule := decision.Summary.Rules["s"]; rule.Expected != 1 || rule.AgedOut != 0 {
		t.Fatalf("空头规则的预期集合应只含永续可用的标的，年龄剔除不再计入：%+v", rule)
	}
	if rule := decision.Summary.Rules["l"]; rule.Expected != 2 || rule.AgedOut != 1 {
		t.Fatalf("多头规则不受影响：%+v", rule)
	}
}

// select: spot, prefer: swap：持仓在永续上的币仍先取永续的行；永续本期不可用时冻结，不被卖出，也不退回去取现货的行。
func TestHoldingMarketRowFirstForSpotSelectSwapPrefer(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketSpot, Prefer: dsl.MarketSwap}, topOneRule+"portfolio: {max_missing: 1}\n", "m")
	rows := map[string]map[string]float64{dsl.MarketSpot: {"A": 1, "B": 9}, dsl.MarketSwap: {}}
	unavailable := map[string]map[string]map[string]string{dsl.MarketSwap: {"m": {"A": FailedMarket, "B": FailedMarket}}}
	decision, err := Evaluate(program, marketFrame(program, rows, unavailable), State{Targets: map[string]string{"A|swap": "0.4"}})
	if err != nil {
		t.Fatal(err)
	}
	assertOK(t, decision)
	got := keysOf(decision)
	if got["A|swap"] != "0.4" {
		t.Fatalf("永续不可用时持仓在永续的币应冻结保持：%v", got)
	}
	var hold bool
	for _, target := range decision.Targets {
		hold = hold || (target.InstrumentID == "A" && target.Market == dsl.MarketSwap && target.Hold)
	}
	if !hold {
		t.Fatalf("A 应是保持目标：%+v", decision.Targets)
	}

	// 永续可用时，持仓币取永续的行（A=10 胜过 B=2）；若误取现货的行（A=1），入选的会变成 B。
	rows = map[string]map[string]float64{dsl.MarketSpot: {"A": 1, "B": 2}, dsl.MarketSwap: {"A": 10, "B": 2}}
	decision, err = Evaluate(program, marketFrame(program, rows, nil), State{Targets: map[string]string{"A|swap": "0.4"}})
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"A|swap": "1"})
}
