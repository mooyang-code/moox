package pool

import (
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
)

// 单成员池（比例 1）与直接运行该策略的目标逐期一致：成员真的用引擎求值，比较目标、权重与换手。
func TestSingleMemberPoolMatchesRunningTheStrategyDirectly(t *testing.T) {
	memberDSL := `name: member
bar: 1h
market: {select: spot}
rules:
  - id: top
    type: rank
    score: "close"
    select: {top: 3, buffer: 1}
    weight: {total: 0.9, method: rank}
portfolio: {max_weight: 0.5, min_universe: 3}
`
	strategy, err := dsl.Parse([]byte(memberDSL))
	if err != nil {
		t.Fatal(err)
	}
	program, err := dsl.Compile(strategy, []string{"close"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 1}}\n", "portfolio: {max_weight: 0.5, min_universe: 3}\n", "a")
	symbols := []string{"AAA", "BBB", "CCC", "DDD", "EEE"}
	var memberState, poolState engine.State
	for bar := 1; bar <= 12; bar++ {
		rows := map[string]engine.Row{}
		prices := map[string]float64{}
		for i, symbol := range symbols {
			// 价格按期数与标的错开，让排名每期都有变化。
			price := 10 + float64((bar*(i+3))%7) + float64(i)
			rows[symbol] = engine.Row{Values: map[string]float64{"close": price}}
			prices[engine.TargetKey(symbol, dsl.MarketSpot)] = price
		}
		frame := engine.Frame{BarEnd: testBarEnd(bar), BarIndex: int64(bar), Markets: map[string]engine.MarketInput{dsl.MarketSpot: {Rows: rows}}, Universe: symbols,
			Expected: map[string][]string{"top": symbols}, AgedOut: map[string][]string{}}
		direct, err := engine.Evaluate(program, frame, memberState)
		if err != nil {
			t.Fatal(err)
		}
		memberState = direct.State
		member, err := engine.Evaluate(program, frame, poolState.Members["a"])
		if err != nil {
			t.Fatal(err)
		}
		pooled, err := Step(cfg, poolState, Input{BarIndex: int64(bar), Prices: prices, Members: []MemberResult{{ID: "a", Decision: &member}}})
		if err != nil {
			t.Fatal(err)
		}
		poolState = pooled.State
		if pooled.Status != direct.Status {
			t.Fatalf("第 %d 期状态 %s 与直接运行的 %s 不一致", bar, pooled.Status, direct.Status)
		}
		if fmt.Sprint(targets(pooled)) != fmt.Sprint(targets(direct)) {
			t.Fatalf("第 %d 期目标不一致：池 %v，直接 %v", bar, targets(pooled), targets(direct))
		}
		if pooled.Summary.Gross != direct.Summary.Gross || pooled.Summary.Net != direct.Summary.Net || pooled.Summary.Turnover != direct.Summary.Turnover || pooled.Summary.Cash != direct.Summary.Cash {
			t.Fatalf("第 %d 期摘要不一致：池 %+v，直接 %+v", bar, pooled.Summary, direct.Summary)
		}
		if fmt.Sprint(poolState.Targets) != fmt.Sprint(direct.State.Targets) {
			t.Fatalf("第 %d 期状态里的目标不一致", bar)
		}
	}
}
