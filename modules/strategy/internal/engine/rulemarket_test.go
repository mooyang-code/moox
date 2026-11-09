package engine

import (
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// 本文件覆盖规则级市场（S4）：规则写了 market 时只用该市场的行选择、目标落在该市场，冻结按（标的, 市场）键。

// ruleMarketDSL 构造两个市场模式下的策略：rules 是规则列表（YAML 流式写法，每行一条）。
func ruleMarketDSL(prefer, rules string) string {
	return "name: rm\nmarket: {select: both, prefer: " + prefer + "}\nportfolio: {max_missing: 1}\nrules:\n" + rules
}

// 同一个币在两条规则里落在不同市场：两个目标各自独立、不互相抵消；规则按自己市场的因子值排名。
func TestRuleMarketSameCoinInTwoMarkets(t *testing.T) {
	program := compileIn(t, dsl.Market{}, ruleMarketDSL(dsl.MarketSpot, `  - {id: s, type: rank, market: spot, score: m, select: {top: 1}, weight: 0.4}
  - {id: w, type: rank, market: swap, score: m, select: {top: 1}, weight: 0.4}
  - {id: h, type: rank, market: swap, side: short, score: m, select: {top: 1}, weight: 0.2}
`), "m")
	// 两个市场都是 A 的值最大：现货多头、永续多头、永续空头都选 A，永续多空合成 0.2，与现货多头互不抵消。
	same := marketFrame(program, map[string]map[string]float64{dsl.MarketSpot: {"A": 3, "B": 1}, dsl.MarketSwap: {"A": 5, "B": 2}}, nil)
	decision, err := Evaluate(program, same, State{})
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"A|spot": "0.4", "A|swap": "0.2"})
	for _, rule := range []string{"s", "w", "h"} {
		item := findItem(t, decision, rule, "A")
		want := dsl.MarketSwap
		if rule == "s" {
			want = dsl.MarketSpot
		}
		if item.Stage != StageWeighted || item.Market != want {
			t.Fatalf("规则 %s 的 A 应在 %s 入选：%+v", rule, want, item)
		}
	}
	// 永续上 B 的值最大：永续规则按永续的值排名选 B，现货规则仍选 A。
	split := marketFrame(program, map[string]map[string]float64{dsl.MarketSpot: {"A": 3, "B": 1}, dsl.MarketSwap: {"A": 1, "B": 5}}, nil)
	decision, err = Evaluate(program, split, State{})
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"A|spot": "0.4", "B|swap": "0.2"})
	if item := findItem(t, decision, "w", "A"); item.Market != dsl.MarketSwap || item.Stage == StageWeighted {
		t.Fatalf("永续规则里 A 应按永续的值落选、市场 swap：%+v", item)
	}
}

// 规则的预期集合只含在该市场有行、有失败证据或本规则持有的标的：只上了现货的币不是永续规则的候选，不记成无行缺数、不触发守门；
// 本规则持有、但永续已经没有行也没有证据的币记 no_row 缺数并平掉。
func TestRuleMarketNarrowsExpected(t *testing.T) {
	raw := "name: rm\nmarket: {select: both, prefer: spot}\nrules:\n  - {id: w, type: rank, market: swap, score: m, select: {top: 1}, weight: 0.4}\n"
	program := compileIn(t, dsl.Market{}, raw, "m")
	frame := marketFrame(program, map[string]map[string]float64{dsl.MarketSpot: {"A": 1, "B": 2, "C": 3}, dsl.MarketSwap: {"B": 20, "C": 30}}, nil)
	decision, err := Evaluate(program, frame, State{})
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"C|swap": "0.4"})
	if summary := decision.Summary.Rules["w"]; summary.Expected != 2 || summary.Missing != 0 {
		t.Fatalf("永续规则的预期集合应只有 B、C：%+v", summary)
	}

	loose := compileIn(t, dsl.Market{}, ruleMarketDSL(dsl.MarketSpot, "  - {id: w, type: rank, market: swap, score: m, select: {top: 1}, weight: 0.4}\n"), "m")
	previous := State{Rules: map[string]RuleState{"w": {Held: []string{"A"}}}, Targets: map[string]string{"A|swap": "0.4"}}
	decision, err = Evaluate(loose, marketFrame(loose, map[string]map[string]float64{dsl.MarketSpot: {"A": 1, "B": 2, "C": 3}, dsl.MarketSwap: {"B": 20, "C": 30}}, nil), previous)
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"C|swap": "0.4"})
	if item := findItem(t, decision, "w", "A"); item.Stage != StageMissing || item.Reason != "no_row" || item.Market != dsl.MarketSwap {
		t.Fatalf("永续上已经没有的持仓 A 应记 no_row 缺数：%+v", item)
	}
}

// 一个市场不可用时只冻结该市场的键：永续不可用，永续规则持有的 A|swap 保持，同一个币的现货规则照常用现货行重新选择；
// 永续规则不守门，不把现货规则一起整期跳过。没写规则级市场的同样两条规则会把 A 按币冻结、两个键都保持。
func TestRuleMarketFreezesOnlyItsKey(t *testing.T) {
	rules := `  - {id: s, type: rank, market: spot, score: m, select: {top: 1}, weight: 0.4}
  - {id: w, type: rank, market: swap, score: m, select: {top: 1}, weight: 0.4}
`
	program := compileIn(t, dsl.Market{}, ruleMarketDSL(dsl.MarketSpot, rules), "m")
	previous := State{
		Rules:   map[string]RuleState{"s": {Held: []string{"A"}}, "w": {Held: []string{"A"}}},
		Targets: map[string]string{"A|spot": "0.4", "A|swap": "0.4"},
	}
	decision, err := Evaluate(program, swapDown(program), previous)
	if err != nil {
		t.Fatal(err)
	}
	requireHolds(t, decision, map[string]string{"C|spot": "0.4", "A|swap": "0.4(hold)"})
	if item := findItem(t, decision, "w", "A"); item.Stage != StageHeld || item.Reason != reasonMarketHold || item.Market != dsl.MarketSwap {
		t.Fatalf("永续规则的 A 应按键冻结：%+v", item)
	}
	if item := findItem(t, decision, "s", "A"); item.Stage == StageHeld || item.Market != dsl.MarketSpot {
		t.Fatalf("现货规则的 A 不应冻结：%+v", item)
	}
	if !strings.Contains(strings.Join(decision.Summary.Notes, "\n"), "规则 w（market: swap）不守门") {
		t.Fatalf("应注明永续规则不守门：%v", decision.Summary.Notes)
	}
	if held := decision.State.Rules["w"].Held; len(held) != 1 || held[0] != "A" {
		t.Fatalf("冻结的 A 仍是永续规则的持仓：%v", held)
	}

	// 对照：一条按币选行的规则持有现货 A、另一条空头规则持有永续 A，永续不可用时 A 按币冻结，两个键都保持（已知的保守边界）。
	plain := compileIn(t, dsl.Market{}, ruleMarketDSL(dsl.MarketSpot, `  - {id: s, type: rank, score: m, select: {top: 1}, weight: 0.4}
  - {id: w, type: rank, side: short, score: m, select: {top: 1}, weight: 0.4}
`), "m")
	plainPrevious := State{
		Rules:   map[string]RuleState{"s": {Held: []string{"A"}}, "w": {Held: []string{"A"}}},
		Targets: map[string]string{"A|spot": "0.4", "A|swap": "-0.4"},
	}
	decision, err = Evaluate(plain, swapDown(plain), plainPrevious)
	if err != nil {
		t.Fatal(err)
	}
	if got := holdsOf(decision); got["A|spot"] != "0.4(hold)" || got["A|swap"] != "-0.4(hold)" {
		t.Fatalf("按币冻结应保持 A 的两个键：%v", got)
	}
}

// 规则级市场与“上期持有沿用市场”同时存在：写了规则级市场的规则不沿用，它的持仓也不让按币选行的规则沿用。
func TestRuleMarketIgnoresStickyMarket(t *testing.T) {
	rows := map[string]map[string]float64{dsl.MarketSpot: {"A": 3, "B": 1}, dsl.MarketSwap: {"A": 3, "B": 1}}
	// 按币选行的 u 上期在永续持有 A：u 沿用永续；现货规则 s 不沿用，A 落在现货。
	program := compileIn(t, dsl.Market{}, ruleMarketDSL(dsl.MarketSpot, `  - {id: u, type: rank, score: m, select: {top: 1}, weight: 0.4}
  - {id: s, type: rank, market: spot, score: m, select: {top: 1}, weight: 0.4}
`), "m")
	previous := State{Rules: map[string]RuleState{"u": {Held: []string{"A"}}}, Targets: map[string]string{"A|swap": "0.4"}}
	decision, err := Evaluate(program, marketFrame(program, rows, nil), previous)
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"A|spot": "0.4", "A|swap": "0.4"})

	// 永续规则 w 上期持有 A|swap：按币选行的 u 不因此粘到永续，仍按 prefer 落在现货。
	program = compileIn(t, dsl.Market{}, ruleMarketDSL(dsl.MarketSpot, `  - {id: u, type: rank, score: m, select: {top: 1}, weight: 0.4}
  - {id: w, type: rank, market: swap, score: m, select: {top: 1}, weight: 0.4}
`), "m")
	previous = State{Rules: map[string]RuleState{"w": {Held: []string{"A"}}}, Targets: map[string]string{"A|swap": "0.4"}}
	decision, err = Evaluate(program, marketFrame(program, rows, nil), previous)
	if err != nil {
		t.Fatal(err)
	}
	requireTargets(t, decision, map[string]string{"A|spot": "0.4", "A|swap": "0.4"})
	if item := findItem(t, decision, "u", "A"); item.Market != dsl.MarketSpot {
		t.Fatalf("u 的 A 应落在现货：%+v", item)
	}
}

// 规则级市场配 on_missing：永续规则持有的 A 在永续缺值时按键冻结保持（missing_kept），连续缺数根数记在这条规则；
// 超过 keep_bars 后移出，现货规则始终照常。上期状态为空（停用再启用）时不冻结。
func TestRuleMarketOnMissing(t *testing.T) {
	program := compileIn(t, dsl.Market{}, ruleMarketDSL(dsl.MarketSpot, `  - {id: s, type: rank, market: spot, score: m, select: {top: 1}, weight: 0.4}
  - {id: w, type: rank, market: swap, score: m, select: {top: 1}, weight: 0.4, on_missing: {keep_bars: 1}}
`), "m")
	frame := func() Frame {
		frame := marketFrame(program, map[string]map[string]float64{dsl.MarketSpot: {"A": 3, "B": 1}, dsl.MarketSwap: {"A": 3, "B": 1}}, nil)
		frame.Markets[dsl.MarketSwap].Rows["A"] = Row{Values: map[string]float64{}}
		return frame
	}
	previous := State{
		Rules:   map[string]RuleState{"s": {Held: []string{"A"}}, "w": {Held: []string{"A"}}},
		Targets: map[string]string{"A|spot": "0.4", "A|swap": "0.4"},
	}
	decision, err := Evaluate(program, frame(), previous)
	if err != nil {
		t.Fatal(err)
	}
	// 冻结的 A 不占 top 名额，永续规则另选 B；保持目标预留 0.4 后其余目标按剩余预算 0.6 等比缩小（组合约束的现状）。
	requireHolds(t, decision, map[string]string{"A|spot": "0.3", "A|swap": "0.4(hold)", "B|swap": "0.3"})
	if item := findItem(t, decision, "w", "A"); item.Stage != StageHeld || item.Reason != reasonMissingKept || item.Market != dsl.MarketSwap {
		t.Fatalf("永续规则的 A 应按缺数冻结保持：%+v", item)
	}
	if streak := decision.State.Rules["w"].MissingStreak; streak["A"] != 1 {
		t.Fatalf("连续缺数根数应为 1：%v", streak)
	}
	decision, err = Evaluate(program, frame(), decision.State)
	if err != nil {
		t.Fatal(err)
	}
	requireHolds(t, decision, map[string]string{"A|spot": "0.4", "B|swap": "0.4"})
	if streak := decision.State.Rules["w"].MissingStreak; len(streak) != 0 {
		t.Fatalf("移出后不应再记连续缺数：%v", streak)
	}
	decision, err = Evaluate(program, frame(), State{})
	if err != nil {
		t.Fatal(err)
	}
	requireHolds(t, decision, map[string]string{"A|spot": "0.4", "B|swap": "0.4"})
}
