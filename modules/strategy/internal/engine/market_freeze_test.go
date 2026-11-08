package engine

import (
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// 本文件覆盖多市场模式下持仓在永续上的冻结：永续整体不可用（market_unavailable）时冻结，逐币的因子失败不冻结。

const shortTopOneRule = `name: s
rules:
  - id: r
    type: rank
    side: short
    score: "m"
    select: {top: 1}
    weight: {total: 0.5}
portfolio: {leverage: 2, max_missing: 1}
`

const longTopOneRule = `name: l
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.5}
portfolio: {max_missing: 1}
`

// holdsOf 返回目标按（标的|市场）作键的权重，保持目标附 "(hold)"。
func holdsOf(decision Decision) map[string]string {
	out := map[string]string{}
	for _, target := range decision.Targets {
		key := TargetKey(target.InstrumentID, target.Market)
		out[key] = target.Weight.String()
		if target.Hold {
			out[key] += "(hold)"
		}
	}
	return out
}

func requireHolds(t *testing.T, decision Decision, want map[string]string) {
	t.Helper()
	assertOK(t, decision)
	got := holdsOf(decision)
	if len(got) != len(want) {
		t.Fatalf("目标不符：got %v want %v", got, want)
	}
	for key, weight := range want {
		if got[key] != weight {
			t.Fatalf("目标不符：got %v want %v", got, want)
		}
	}
}

// singleCoinFailure 构造“永续上 A 单币失败”的帧：现货 A、B、C，永续 B、C、D，永续没有 A 的行，只有 A 的失败证据。
func singleCoinFailure(program *dsl.Program, reason string) Frame {
	rows := map[string]map[string]float64{
		dsl.MarketSpot: {"A": 1, "B": 2, "C": 3},
		dsl.MarketSwap: {"B": 20, "C": 30, "D": 1},
	}
	failed := map[string]map[string]map[string]string{dsl.MarketSwap: {"m": {"A": reason}}}
	return marketFrame(program, rows, failed)
}

// swapDown 构造永续整体不可用的帧：现货 A、B、C 照常，永续的行已清掉，永续基础集合 A、B、C、D 都带 market_unavailable 证据。
func swapDown(program *dsl.Program) Frame {
	rows := map[string]map[string]float64{dsl.MarketSpot: {"A": 1, "B": 2, "C": 3}}
	down := map[string]string{"A": FailedMarket, "B": FailedMarket, "C": FailedMarket, "D": FailedMarket}
	return marketFrame(program, rows, map[string]map[string]map[string]string{dsl.MarketSwap: {"m": down}})
}

// 多市场模式下持有永续空头 A、本期永续整体不可用：A 冻结保持，不改用现货行的因子值重新排名而被平掉（两种 prefer）。
func TestHeldSwapShortFreezesWhenSwapUnavailable(t *testing.T) {
	for _, prefer := range []string{dsl.MarketSpot, dsl.MarketSwap} {
		program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: prefer}, shortTopOneRule, "m")
		previous := State{Rules: map[string]RuleState{"r": {Held: []string{"A"}}}, Targets: map[string]string{"A|swap": "-0.5"}}
		decision, err := Evaluate(program, swapDown(program), previous)
		if err != nil {
			t.Fatal(err)
		}
		requireHolds(t, decision, map[string]string{"A|swap": "-0.5(hold)"})
		item := findItem(t, decision, "r", "A")
		if item.Stage != StageHeld || item.Reason != reasonMarketHold || item.Market != dsl.MarketSwap {
			t.Fatalf("prefer=%s：A 的明细应是 held/market_hold、市场 swap：%+v", prefer, item)
		}
	}
}

// factor_failed 单市场下不冻结，多市场下持有的永续空头也不放开：A 按缺数或现货行重新排名处理，不是保持目标。
func TestHeldSwapShortNotFrozenOnFactorFailure(t *testing.T) {
	for _, prefer := range []string{dsl.MarketSpot, dsl.MarketSwap} {
		program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: prefer}, shortTopOneRule, "m")
		frame := singleCoinFailure(program, FailedFactor)
		previous := State{Rules: map[string]RuleState{"r": {Held: []string{"A"}}}, Targets: map[string]string{"A|swap": "-0.5"}}
		decision, err := Evaluate(program, frame, previous)
		if err != nil {
			t.Fatal(err)
		}
		requireHolds(t, decision, map[string]string{"C|swap": "-0.5"})
	}
}

// 多市场模式下持有永续多头 A、本期永续整体不可用：粘住持仓市场，冻结保持（三种含永续下单的模式）。
func TestHeldSwapLongFreezesWhenSwapUnavailable(t *testing.T) {
	modes := []dsl.Market{
		{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot},
		{Select: dsl.MarketBoth, Prefer: dsl.MarketSwap},
		{Select: dsl.MarketSpot, Prefer: dsl.MarketSwap},
	}
	for _, mode := range modes {
		program := compileIn(t, mode, longTopOneRule, "m")
		previous := State{Rules: map[string]RuleState{"r": {Held: []string{"A"}}}, Targets: map[string]string{"A|swap": "0.5"}}
		decision, err := Evaluate(program, swapDown(program), previous)
		if err != nil {
			t.Fatal(err)
		}
		assertOK(t, decision)
		if got := holdsOf(decision)["A|swap"]; got != "0.5(hold)" {
			t.Fatalf("%+v：持有的永续多头 A 在永续不可用时应冻结：%v", mode, holdsOf(decision))
		}
		if item := findItem(t, decision, "r", "A"); item.Stage != StageHeld || item.Reason != reasonMarketHold || item.Market != dsl.MarketSwap {
			t.Fatalf("%+v：A 的明细应是 held/market_hold、市场 swap：%+v", mode, item)
		}
	}
}

// 永续整体不可用时空头 signal 规则也不守门（与空头候选过滤的范围一致）：持有的永续空头 X 保持，明细原因 market_hold、市场 swap；
// 多头规则照常（冻结的 X 不是多头规则的持仓，不计入它守门的缺数），在现货开仓。
func TestShortSignalRuleSkipsGuardWhenSwapUnavailable(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, `name: ls
rules:
  - id: l
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.5}
  - id: s
    type: signal
    side: short
    pool: [X]
    entry: "m > 100"
    exit: "m < -100"
    weight: {total: 0.5}
portfolio: {leverage: 2}
`, "m")
	rows := map[string]map[string]float64{dsl.MarketSpot: {"A": 1, "B": 2, "C": 3, "X": 4}, dsl.MarketSwap: {}}
	failed := map[string]map[string]map[string]string{dsl.MarketSwap: {"m": {"B": FailedMarket, "C": FailedMarket, "X": FailedMarket}}}
	frame := marketFrame(program, rows, failed)
	frame.Expected["s"] = []string{"X"}
	previous := State{Rules: map[string]RuleState{"s": {Held: []string{"X"}}}, Targets: map[string]string{"X|swap": "-0.5"}}
	decision, err := Evaluate(program, frame, previous)
	if err != nil {
		t.Fatal(err)
	}
	requireHolds(t, decision, map[string]string{"C|spot": "0.5", "X|swap": "-0.5(hold)"})
	// 不守门的这一期之后空头规则的理论持仓仍有 X，下一期永续恢复时照常判 exit。
	if !contains(decision.State.Rules["s"].Held, "X") {
		t.Fatalf("空头规则的持仓应保留 X：%+v", decision.State.Rules["s"])
	}
	for _, ruleID := range []string{"l", "s"} {
		if item := findItem(t, decision, ruleID, "X"); item.Stage != StageHeld || item.Reason != reasonMarketHold || item.Market != dsl.MarketSwap {
			t.Fatalf("规则 %s 里 X 的明细应是 held/market_hold、市场 swap：%+v", ruleID, item)
		}
	}
}

// 多头规则自己持有的冻结标的仍计入它守门的缺数：永续不可用时持有的 D、X 都冻结，比例超过 max_missing，整期 too_many_missing。
func TestFrozenHoldingsOfTheRuleStillCountInGuard(t *testing.T) {
	program := compileIn(t, dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSwap}, `name: l
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 1}
portfolio: {max_missing: 0.2}
`, "m")
	rows := map[string]map[string]float64{dsl.MarketSpot: {"A": 1, "B": 2, "C": 3, "X": 4}, dsl.MarketSwap: {}}
	failed := map[string]map[string]map[string]string{dsl.MarketSwap: {"m": {"B": FailedMarket, "C": FailedMarket, "D": FailedMarket, "X": FailedMarket}}}
	frame := marketFrame(program, rows, failed)
	previous := State{Rules: map[string]RuleState{"r": {Held: []string{"D", "X"}}}, Targets: map[string]string{"D|swap": "0.5", "X|swap": "0.5"}}
	decision, err := Evaluate(program, frame, previous)
	if err != nil {
		t.Fatal(err)
	}
	assertSkipped(t, decision, SkipTooManyMissing)
}
