package pool

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
)

const (
	swapX = "X|swap"
	swapY = "Y|swap"
)

// sidedMember 是一个单方向的永续成员策略（side 为 long 或 short）。
func sidedMember(t *testing.T, side string) dsl.Strategy {
	t.Helper()
	strategy, err := dsl.Parse([]byte("name: m\nbar: 1h\nmarket: {select: swap}\nrules:\n  - id: r\n    type: rank\n    score: close\n    select: {top: 1}\n    weight: 1\n    side: " + side + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return strategy
}

// sidedConfig 解析一个池（body 写在 strategy_pool 之下，portfolio 写在顶层），members 是按顺序的（成员 id, 方向）。
func sidedConfig(t *testing.T, body, portfolio string, members ...[2]string) Config {
	t.Helper()
	var list strings.Builder
	strategies := map[string]dsl.Strategy{}
	for _, member := range members {
		fmt.Fprintf(&list, "    - {id: %s, strategy: s_%s}\n", member[0], member[0])
		strategies[member[0]] = sidedMember(t, member[1])
	}
	raw := "name: pool\nbar: 1h\nstrategy_pool:\n  members:\n" + list.String() + body + "  fees: {spot_bps: 0, swap_bps: 0}\n" + portfolio
	strategy, err := dsl.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("解析池失败：%v\n%s", err, raw)
	}
	if _, err := dsl.ValidatePoolMembers(strategy, strategies); err != nil {
		t.Fatal(err)
	}
	cfg, err := NewConfig(strategy, dsl.SingleMarket(dsl.MarketSwap), strategies)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

var longShort = [][2]string{{"l", dsl.SideLong}, {"s", dsl.SideShort}}

// longShortBar 是多头成员持有 X、空头成员持有 Y 的一期。
func longShortBar() bar {
	return bar{prices: map[string]float64{swapX: 10, swapY: 10}, members: []MemberResult{pass("l", map[string]string{swapX: "1"}), pass("s", map[string]string{swapY: "-1"})}}
}

func composedRatios(decision engine.Decision) map[string]string {
	out := map[string]string{}
	for _, member := range decision.Summary.Pool.Members {
		ratio := member.Ratio
		if member.Composed != "" {
			ratio = member.Composed
		}
		out[member.ID] = ratio
	}
	return out
}

func TestExposureBandAdjustModes(t *testing.T) {
	cases := []struct {
		name, ratios, band string
		want               map[string]string
	}{
		// 净敞口 0.6 高于 max 0.3：缩多头到 0.5，或放大空头到 0.5。
		{"高于上限缩多头", "{l: 0.8, s: 0.2}", "adjust: reduce_larger", map[string]string{"l": "0.5", "s": "0.2"}},
		{"高于上限放大空头", "{l: 0.8, s: 0.2}", "adjust: raise_smaller", map[string]string{"l": "0.8", "s": "0.5"}},
		// 净敞口 −0.6 低于 min −0.1：缩空头到 0.3，或放大多头到 0.7。
		{"低于下限缩空头", "{l: 0.2, s: 0.8}", "adjust: reduce_larger", map[string]string{"l": "0.2", "s": "0.3"}},
		{"低于下限放大多头", "{l: 0.2, s: 0.8}", "adjust: raise_smaller", map[string]string{"l": "0.7", "s": "0.8"}},
		// auto：总敞口 1.0 不大于 pivot 1.5 时放大，大于 pivot 0.5 时缩小。
		{"auto 放大", "{l: 0.8, s: 0.2}", "adjust: auto, pivot: 1.5", map[string]string{"l": "0.8", "s": "0.5"}},
		{"auto 缩小", "{l: 0.8, s: 0.2}", "adjust: auto, pivot: 0.5", map[string]string{"l": "0.5", "s": "0.2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := sidedConfig(t, "  allocation: {method: fixed, ratios: "+tc.ratios+"}\n  exposure: {bands: [{min: -0.1, max: 0.3, "+tc.band+"}]}\n", "portfolio: {leverage: 2}\n", longShort...)
			decisions, _ := run(t, cfg, []bar{longShortBar()})
			requireMap(t, "合成比例", composedRatios(decisions[0]), tc.want)
			if adjustments := decisions[0].Summary.Pool.Adjustments; len(adjustments) != 1 || adjustments[0].Kind != "exposure" || adjustments[0].Band != 1 {
				t.Fatalf("修正明细 %+v", adjustments)
			}
			requireMap(t, "池目标", targets(decisions[0]), map[string]string{swapX: tc.want["l"], swapY: "-" + tc.want["s"]})
		})
	}
}

// 区间不含 0 时也按超出方向修正，净敞口回到区间边界：字面上的“较大一侧”在这时会把净敞口推得更远
// （例如区间 [0.2, 0.5]、多头 0.55、空头 0.45，缩较大的多头会让净敞口继续下降）。
func TestExposureBandWithoutZero(t *testing.T) {
	cases := []struct {
		name, ratios, band string
		want               map[string]string
	}{
		// 区间 [0.2, 0.5]，净敞口 0.1 低于 min：缩空头到 0.35，或放大多头到 0.65。
		{"正区间低于下限缩空头", "{l: 0.55, s: 0.45}", "min: 0.2, max: 0.5, adjust: reduce_larger", map[string]string{"l": "0.55", "s": "0.35"}},
		{"正区间低于下限放大多头", "{l: 0.55, s: 0.45}", "min: 0.2, max: 0.5, adjust: raise_smaller", map[string]string{"l": "0.65", "s": "0.45"}},
		// 区间 [−0.5, −0.2]，净敞口 −0.05 高于 max：缩多头到 0.3，或放大空头到 0.65。
		{"负区间高于上限缩多头", "{l: 0.45, s: 0.5}", "min: -0.5, max: -0.2, adjust: reduce_larger", map[string]string{"l": "0.3", "s": "0.5"}},
		{"负区间高于上限放大空头", "{l: 0.45, s: 0.5}", "min: -0.5, max: -0.2, adjust: raise_smaller", map[string]string{"l": "0.45", "s": "0.65"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := sidedConfig(t, "  allocation: {method: fixed, ratios: "+tc.ratios+"}\n  exposure: {bands: [{"+tc.band+"}]}\n", "portfolio: {leverage: 2}\n", longShort...)
			decisions, _ := run(t, cfg, []bar{longShortBar()})
			requireMap(t, "合成比例", composedRatios(decisions[0]), tc.want)
		})
	}
}

// 带 when_gross_below 的段只在总敞口低于阈值时执行；区间内不修正。
func TestExposureConditionalBand(t *testing.T) {
	body := "  exposure: {bands: [{when_gross_below: 1.0, min: 0, max: 0, adjust: raise_smaller}]}\n"
	low := sidedConfig(t, "  allocation: {method: fixed, ratios: {l: 0.5, s: 0.3}}\n"+body, "portfolio: {leverage: 2}\n", longShort...)
	decisions, _ := run(t, low, []bar{longShortBar()})
	requireMap(t, "总敞口 0.8 低于 1 时把净敞口拉到 0", composedRatios(decisions[0]), map[string]string{"l": "0.5", "s": "0.5"})
	high := sidedConfig(t, "  allocation: {method: fixed, ratios: {l: 0.7, s: 0.5}}\n"+body, "portfolio: {leverage: 2}\n", longShort...)
	decisions, _ = run(t, high, []bar{longShortBar()})
	requireMap(t, "总敞口 1.2 时不执行", composedRatios(decisions[0]), map[string]string{"l": "0.7", "s": "0.5"})
	if len(decisions[0].Summary.Pool.Adjustments) != 0 {
		t.Fatalf("不执行时没有修正明细：%+v", decisions[0].Summary.Pool.Adjustments)
	}
}

// 总杠杆区间：低于 min 时整体放大（池的第一根不放大），净敞口按同一倍数变化；高于 max 时整体缩小。
func TestGrossScalesAllMembers(t *testing.T) {
	cfg := sidedConfig(t, "  allocation: {method: fixed, ratios: {l: 0.4, s: 0.2}}\n  gross: {min: 1.2, max: 1.5}\n", "portfolio: {leverage: 2}\n", longShort...)
	decisions, _ := run(t, cfg, []bar{longShortBar(), longShortBar()})
	requireMap(t, "第一根不放大", composedRatios(decisions[0]), map[string]string{"l": "0.4", "s": "0.2"})
	requireMap(t, "第二根放大 2 倍", composedRatios(decisions[1]), map[string]string{"l": "0.8", "s": "0.4"})
	adjustment := decisions[1].Summary.Pool.Adjustments[0]
	netBefore, netAfter := adjustment.LongBefore-adjustment.ShortBefore, adjustment.LongAfter-adjustment.ShortAfter
	if adjustment.Kind != "gross" || math.Abs(netAfter-netBefore*adjustment.Factor) > 1e-12 || math.Abs(adjustment.Factor-2) > 1e-12 {
		t.Fatalf("净敞口应按同一倍数变化：%+v", adjustment)
	}
	shrink := sidedConfig(t, "  allocation: {method: fixed, ratios: {l: 1.2, s: 0.6}}\n  gross: {max: 1.5}\n", "portfolio: {leverage: 2}\n", longShort...)
	decisions, _ = run(t, shrink, []bar{longShortBar()})
	requireMap(t, "高于 max 整体缩小（第一根也缩）", composedRatios(decisions[0]), map[string]string{"l": "1", "s": "0.5"})
}

// 写了 exposure 后，fixed 的步长仍按步长之后、约束之前的比例计算：状态里的上期比例不含修正。
func TestExposureDoesNotFeedStep(t *testing.T) {
	cfg := sidedConfig(t, "  allocation: {method: fixed, ratios: {l: 0.8, s: 0.2}}\n  step: 0.1\n  exposure: {bands: [{min: -0.1, max: 0.3, adjust: reduce_larger}]}\n", "portfolio: {leverage: 2}\n", longShort...)
	decisions, state := run(t, cfg, []bar{longShortBar(), longShortBar()})
	for i, decision := range decisions {
		requireMap(t, fmt.Sprintf("第 %d 期步长之后的比例", i+1), ratios(decision), map[string]string{"l": "0.8", "s": "0.2"})
		requireMap(t, fmt.Sprintf("第 %d 期合成比例", i+1), composedRatios(decision), map[string]string{"l": "0.5", "s": "0.2"})
	}
	decoded, err := DecodeState(state.Pool)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Ratios, map[string]string{"l": "0.8", "s": "0.2"}) {
		t.Fatalf("状态里的上期比例 %v", decoded.Ratios)
	}
}

// 条件转移（fixed）：probe 本期有效目标为空时，new 的比例转给 core；probe 有持仓或没通过但沿用上期非空目标时不转移。
// 转移后空头侧的总量不变，状态里的比例不含转移。
func TestSwitchMovesWithinSide(t *testing.T) {
	members := [][2]string{{"long_core", dsl.SideLong}, {"short_core", dsl.SideShort}, {"short_new", dsl.SideShort}, {"probe", dsl.SideShort}}
	cfg := sidedConfig(t, "  allocation:\n    method: fixed\n    ratios: {long_core: 0.5, short_core: 0.45, short_new: 0.05, probe: 0}\n    switch:\n      - when: {empty: probe}\n        move: {from: short_new, to: [short_core]}\n",
		"portfolio: {leverage: 2}\n", members...)
	base := func(probe MemberResult) bar {
		return bar{prices: map[string]float64{swapX: 10, swapY: 10, "Z|swap": 10}, members: []MemberResult{
			pass("long_core", map[string]string{swapX: "1"}), pass("short_core", map[string]string{swapY: "-1"}), pass("short_new", map[string]string{"Z|swap": "-1"}), probe,
		}}
	}
	decisions, state := run(t, cfg, []bar{
		base(pass("probe", map[string]string{})),
		base(pass("probe", map[string]string{"Z|swap": "-1"})),
		base(fail("probe", "no_data")),
	})
	requireMap(t, "probe 为空时转移", composedRatios(decisions[0]), map[string]string{"long_core": "0.5", "short_core": "0.5", "short_new": "0", "probe": "0"})
	if sw := decisions[0].Summary.Pool.Switch; sw == nil || sw.From != "short_new" || sw.Moved != "0.05" {
		t.Fatalf("生效的转移 %+v", sw)
	}
	requireMap(t, "池目标（空头侧合计 0.5 不变）", targets(decisions[0]), map[string]string{swapX: "0.5", swapY: "-0.5"})
	requireMap(t, "probe 有持仓时不转移", composedRatios(decisions[1]), map[string]string{"long_core": "0.5", "short_core": "0.45", "short_new": "0.05", "probe": "0"})
	requireMap(t, "probe 没通过、沿用上期非空目标时不转移", composedRatios(decisions[2]), map[string]string{"long_core": "0.5", "short_core": "0.45", "short_new": "0.05", "probe": "0"})
	if decisions[1].Summary.Pool.Switch != nil || decisions[2].Summary.Pool.Switch != nil {
		t.Fatal("不成立时不记录转移")
	}
	decoded, _ := DecodeState(state.Pool)
	if decoded.Ratios["short_new"] != "0.05" || decoded.Ratios["short_core"] != "0.45" {
		t.Fatalf("状态里的比例不含转移：%v", decoded.Ratios)
	}
}

// 转给多个成员时按它们本根的比例分（都为 0 时平分），各份之和恰好等于转出的量。
func TestSwitchSharesByRatio(t *testing.T) {
	members := [][2]string{{"a", dsl.SideShort}, {"b", dsl.SideShort}, {"c", dsl.SideShort}, {"probe", dsl.SideShort}}
	for name, tc := range map[string]struct {
		ratios string
		want   map[string]string
	}{
		"按比例":     {"{a: 0.2, b: 0.1, c: 0.3, probe: 0}", map[string]string{"a": "0", "b": "0.15", "c": "0.45", "probe": "0"}},
		"都为 0 平分": {"{a: 0.3, b: 0, c: 0, probe: 0}", map[string]string{"a": "0", "b": "0.15", "c": "0.15", "probe": "0"}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := sidedConfig(t, "  allocation:\n    method: fixed\n    ratios: "+tc.ratios+"\n    switch: [{when: {empty: probe}, move: {from: a, to: [b, c]}}]\n", "", members...)
			decisions, _ := run(t, cfg, []bar{{prices: map[string]float64{swapX: 10}, members: []MemberResult{
				pass("a", map[string]string{swapX: "-1"}), pass("b", map[string]string{swapX: "-1"}), pass("c", map[string]string{swapX: "-1"}), pass("probe", nil),
			}}})
			requireMap(t, "合成比例", composedRatios(decisions[0]), tc.want)
		})
	}
}

// 单标的上限在池里按成员的分组目标（比例 × 成员目标）转移；比例为 0 的成员不能作为去向（它的分组目标为 0），超出留现金。
func TestPoolLimitsOverflowToMember(t *testing.T) {
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 1, b: 0.5}}\n", "portfolio: {leverage: 2, limits: {long_max: 0.4, overflow: {to: b}}}\n")
	decisions, _ := run(t, cfg, []bar{{prices: map[string]float64{spotX: 10, spotY: 10}, members: []MemberResult{
		pass("a", map[string]string{spotX: "0.6"}), pass("b", map[string]string{spotY: "0.4"}),
	}}})
	// X 0.6 超出 0.2 转给 b 持有的 Y：0.2 + 0.2 = 0.4。
	requireMap(t, "池目标", targets(decisions[0]), map[string]string{spotX: "0.4", spotY: "0.4"})
	if limits := decisions[0].Summary.Limits; len(limits) != 1 || limits[0].To != "b" || limits[0].Excess != "0.2" {
		t.Fatalf("超限明细 %+v", limits)
	}
	zero := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 1, b: 0}}\n", "portfolio: {leverage: 2, limits: {long_max: 0.4, overflow: {to: b}}}\n")
	decisions, _ = run(t, zero, []bar{{prices: map[string]float64{spotX: 10, spotY: 10}, members: []MemberResult{
		pass("a", map[string]string{spotX: "0.6"}), pass("b", map[string]string{spotY: "0.4"}),
	}}})
	requireMap(t, "比例为 0 的成员不接收", targets(decisions[0]), map[string]string{spotX: "0.4"})
	if limits := decisions[0].Summary.Limits; len(limits) != 1 || limits[0].To != "" {
		t.Fatalf("超限明细 %+v", limits)
	}
}

// 不写新字段时不出现新的摘要字段。
func TestNoConstraintFieldsWithoutConfig(t *testing.T) {
	cfg := sidedConfig(t, "  allocation: {method: fixed, ratios: {l: 0.8, s: 0.2}}\n", "portfolio: {leverage: 2}\n", longShort...)
	decisions, _ := run(t, cfg, []bar{longShortBar()})
	pool := decisions[0].Summary.Pool
	if pool.Adjustments != nil || pool.Switch != nil || decisions[0].Summary.Limits != nil {
		t.Fatalf("没写约束时不应有修正明细：%+v", pool)
	}
	for _, member := range pool.Members {
		if member.Composed != "" {
			t.Fatalf("没写约束时不应有 composed：%+v", member)
		}
	}
}

// 资金费：成员账本按上一根调仓前的持仓结算本根费率；没写 funding_column 时不计。停用再启用（从空状态开始）后快照清零，第一根不结算。
func TestPoolFundingSettlesMemberLedger(t *testing.T) {
	funded := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 1}}\n", "", "a")
	funded.Spec.Fees.FundingColumn = "funding_rate"
	plain := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 1}}\n", "", "a")
	steps := []Input{
		{BarIndex: 1, Prices: map[string]float64{swapZ: 10}, Funding: map[string]float64{swapZ: 0.01}, Members: []MemberResult{pass("a", map[string]string{swapZ: "1"})}},
		{BarIndex: 2, Prices: map[string]float64{swapZ: 10}, Funding: map[string]float64{swapZ: 0.01}, Members: []MemberResult{pass("a", map[string]string{})}},
		{BarIndex: 3, Prices: map[string]float64{swapZ: 10}, Funding: map[string]float64{swapZ: 0.01}, Members: []MemberResult{pass("a", map[string]string{})}},
	}
	equity := func(cfg Config, start engine.State, inputs []Input) (float64, engine.State) {
		state := start
		var last engine.Decision
		for _, in := range inputs {
			decision, err := Step(cfg, state, in)
			if err != nil {
				t.Fatal(err)
			}
			last, state = decision, decision.State
		}
		return last.Summary.Pool.Members[0].Equity, state
	}
	// 第 1 根开多（名义 1）；第 2 根费率按第 1 根调仓前的持仓（空仓）结算，不付，收盘平仓；第 3 根按第 2 根调仓前的多头付 0.01。
	got, state := equity(funded, engine.State{}, steps)
	if math.Abs(got-0.99) > 1e-12 {
		t.Fatalf("计资金费后的净值 %v，期望 0.99", got)
	}
	if plainEquity, _ := equity(plain, engine.State{}, steps); plainEquity != 1 {
		t.Fatalf("不计资金费时净值 %v，期望 1", plainEquity)
	}
	// 第 2 根之后多头已平，但快照仍要读它的费率。
	_, mid := equity(funded, engine.State{}, steps[:2])
	keys, err := PriceKeys(mid, nil)
	if err != nil || !reflect.DeepEqual(keys, []string{swapZ}) {
		t.Fatalf("平掉的仓位仍要读价格与费率：%v %v", keys, err)
	}
	decoded, _ := DecodeState(state.Pool)
	if len(decoded.Ledgers["a"].FundingBasis) != 0 {
		t.Fatalf("空仓后快照清空：%v", decoded.Ledgers["a"].FundingBasis)
	}
	// 停用再启用：从空状态开始，第一根没有快照，不结算。
	restart := []Input{{BarIndex: 10, Prices: map[string]float64{swapZ: 10}, Funding: map[string]float64{swapZ: 0.5}, Members: []MemberResult{pass("a", map[string]string{swapZ: "1"})}}}
	if got, _ := equity(funded, engine.State{}, restart); got != 1 {
		t.Fatalf("重新启用后第一根不结算：%v", got)
	}
	if _, plainState := equity(plain, engine.State{}, steps[:1]); strings.Contains(string(plainState.Pool), "funding_basis") {
		t.Fatalf("不计资金费时状态不出现快照：%s", plainState.Pool)
	}
}
