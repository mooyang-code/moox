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

// 漂移分配（P3）、波动冻结（P6）与漂移起点（P8）的引擎用例。成员都是永续单方向策略、手续费为 0；
// 期望值按设计的公式手算（与实现同样的运算顺序），float64 部分容差 1e-12，换回 Decimal 的比例逐位比较。

const (
	swapA = "A|swap"
	swapB = "B|swap"
	swapC = "C|swap"
	swapD = "D|swap"
)

// driftConfig 解析一个 drift 池（body 写在 strategy_pool 之下），portfolio.leverage 为 3。
func driftConfig(t *testing.T, body string, clock engine.PoolClock, members ...[2]string) Config {
	t.Helper()
	cfg := sidedConfig(t, body, "portfolio: {leverage: 3}\n", members...)
	cfg.Clock = clock
	return cfg
}

// driftStep 是 drift 用例的一期：index 是 bar 序号（可以跳号）；members 为空表示所有成员都没通过（池跳过）。
type driftStep struct {
	index   int64
	prices  map[string]float64
	members []MemberResult
}

func runDrift(t *testing.T, cfg Config, state engine.State, steps []driftStep) ([]engine.Decision, engine.State) {
	t.Helper()
	var decisions []engine.Decision
	for _, step := range steps {
		members := step.members
		if len(members) == 0 {
			for _, id := range cfg.MemberIDs() {
				members = append(members, fail(id, "no_data"))
			}
		}
		decision, err := Step(cfg, state, Input{BarIndex: step.index, Prices: step.prices, Members: members})
		if err != nil {
			t.Fatalf("第 %d 根：%v", step.index, err)
		}
		decisions = append(decisions, decision)
		state = decision.State
	}
	return decisions, state
}

// driftState 返回决策里的 drift 状态（没有时为空）。
func driftState(t *testing.T, decision engine.Decision) *DriftState {
	t.Helper()
	decoded, err := DecodeState(decision.State.Pool)
	if err != nil {
		t.Fatal(err)
	}
	return decoded.Drift
}

// requireCoefficients 核对状态里的分配系数（容差 1e-12）与摘要里的比例（换成 12 位小数后逐位比较）。
func requireCoefficients(t *testing.T, label string, decision engine.Decision, want map[string]float64) {
	t.Helper()
	state := driftState(t, decision)
	if state == nil {
		t.Fatalf("%s：没有 drift 状态", label)
	}
	for id, value := range want {
		if math.Abs(state.G[id]-value) > 1e-12 {
			t.Fatalf("%s：成员 %s 的系数 %.15f，期望 %.15f", label, id, state.G[id], value)
		}
	}
	got := ratios(decision)
	for id, value := range want {
		decimal, err := decimalRatio(value)
		if err != nil {
			t.Fatal(err)
		}
		if got[id] != decimal.String() {
			t.Fatalf("%s：成员 %s 的比例 %s，期望 %s", label, id, got[id], decimal.String())
		}
	}
}

// ret 是价格从 from 到 to 的收益（与账本相同的运算顺序）。
func ret(from, to float64) float64 { return to/from - 1 }

// 两成员（一多一空）的手算：漂移公式、成员空仓、预算未用满、池跳过与缺期后的累计收益。
func TestDriftFormulaByHand(t *testing.T) {
	cfg := driftConfig(t, "  allocation: {method: drift, ratios: {l: 0.6, s: 0.4}}\n", engine.PoolClock{}, longShort...)
	decisions, _ := runDrift(t, cfg, engine.State{}, []driftStep{
		{1, map[string]float64{swapX: 10, swapY: 10}, []MemberResult{pass("l", map[string]string{swapX: "1"}), pass("s", map[string]string{swapY: "-1"})}},
		// 多头只用一半预算；空头本根目标为空，收盘平仓。
		{2, map[string]float64{swapX: 11, swapY: 12}, []MemberResult{pass("l", map[string]string{swapX: "0.5"}), pass("s", map[string]string{})}},
		{3, map[string]float64{swapX: 12, swapY: 9}, []MemberResult{pass("l", map[string]string{swapX: "0.5"}), pass("s", map[string]string{swapY: "-1"})}},
		{4, nil, nil}, // 所有成员都没通过，池跳过
		{6, map[string]float64{swapX: 15, swapY: 10}, []MemberResult{pass("l", map[string]string{swapX: "0.5"}), pass("s", map[string]string{swapY: "-1"})}},
	})
	// 第 1 根：漂移起点，系数置为 ratios。
	if pool := decisions[0].Summary.Pool; pool.Method != dsl.AllocationDrift || pool.Drift == nil || pool.Drift.Phase != engine.DriftStart {
		t.Fatalf("第 1 根应是漂移起点：%+v", pool)
	}
	requireCoefficients(t, "第 1 根", decisions[0], map[string]float64{"l": 0.6, "s": 0.4})
	requireMap(t, "第 1 根池目标", targets(decisions[0]), map[string]string{swapX: "0.6", swapY: "-0.4"})

	// 第 2 根：多头持有名义 1 的 X（权益 1），空头持有名义 −1 的 Y（权益 1）。
	rX, rY := 11.0/10-1, 12.0/10-1
	rhoL, piL := (1*rX)/1, (1*rX)/1
	rhoS, piS := (1*rY)/1, (-1*rY)/1
	denominator := 1 + 0.6*piL + 0.4*piS
	gL2, gS2 := 0.6*(1+rhoL)/denominator, 0.4*(1+rhoS)/denominator
	requireCoefficients(t, "第 2 根", decisions[1], map[string]float64{"l": gL2, "s": gS2})
	if member := decisions[1].Summary.Pool.Members[0]; member.RatioBefore != "0.6" || member.Drifted != member.Ratio || member.Base != "0.6" {
		t.Fatalf("第 2 根的明细：%+v", member)
	}

	// 第 3 根：多头上一根收盘后的权益 1.1、持有一半（名义 0.55）；预算没用满不影响 ρ。空头已平仓：ρ = π = 0，系数只随权益变化。
	rX = 12.0/11 - 1
	rhoL, piL = rX, 0.55*rX/1.1
	denominator = 1 + gL2*piL
	gL3, gS3 := gL2*(1+rhoL)/denominator, gS2/denominator
	requireCoefficients(t, "第 3 根", decisions[2], map[string]float64{"l": gL3, "s": gS3})

	// 第 4 根池跳过：状态不变。
	if decisions[3].Status != engine.StatusSkipped || string(decisions[3].State.Pool) != string(decisions[2].State.Pool) {
		t.Fatal("池跳过时状态不变")
	}
	// 第 6 根（第 5 根缺期）：收益是相对上一次估值价（第 3 根）的累计收益。多头权益 1.15、名义 0.575；空头权益 0.8、名义 −0.8。
	rX, rY = 15.0/12-1, 10.0/9-1
	rhoL, piL = rX, 0.575*rX/1.15
	rhoS, piS = rY, -0.8*rY/0.8
	denominator = 1 + gL3*piL + gS3*piS
	requireCoefficients(t, "第 6 根", decisions[4], map[string]float64{"l": gL3 * (1 + rhoL) / denominator, "s": gS3 * (1 + rhoS) / denominator})
}

// rebalanceMembers 是侧内再平衡用例的成员：多头 l1（持有 A）、l2（持有 B），空头 s（持有 C）。
var rebalanceMembers = [][2]string{{"l1", dsl.SideLong}, {"l2", dsl.SideLong}, {"s", dsl.SideShort}}

// rebalanceBar 是只有 A 变价的一根：l1 满仓 A、l2 满仓 B、s 满仓做空 C。
func rebalanceBar(index int64, a float64) driftStep {
	return driftStep{index, map[string]float64{swapA: a, swapB: 10, swapC: 10}, []MemberResult{
		pass("l1", map[string]string{swapA: "1"}), pass("l2", map[string]string{swapB: "1"}), pass("s", map[string]string{swapC: "-1"}),
	}}
}

// driftA 是只有 A 变价 r 时的一根漂移：l1 满仓，ρ = π = r；其余成员 ρ = π = 0。
func driftA(g map[string]float64, r float64) map[string]float64 {
	denominator := 1 + g["l1"]*r
	return map[string]float64{"l1": g["l1"] * (1 + r) / denominator, "l2": g["l2"] / denominator, "s": g["s"] / denominator}
}

// pullLong 把多头侧的构成向目标 0.6 : 0.4 移动 1/remaining（remaining 为 1 时精确到达），侧的总量不变。
func pullLong(g map[string]float64, remaining int) map[string]float64 {
	total := g["l1"] + g["l2"]
	out := map[string]float64{"s": g["s"]}
	for id, target := range map[string]float64{"l1": 0.3 / 0.5, "l2": 0.2 / 0.5} {
		if remaining <= 1 {
			out[id] = target * total
			continue
		}
		share := g[id] / total
		out[id] = (share + (target-share)/float64(remaining)) * total
	}
	return out
}

var rebalanceStart = map[string]float64{"l1": 0.3, "l2": 0.2, "s": 0.5}

// 五种 rebalance 方式与 spread_bars：A 第 2 根涨 20%、第 3 根再涨 25%，之后不动。
func TestDriftRebalanceModes(t *testing.T) {
	bars := []driftStep{rebalanceBar(1, 10), rebalanceBar(2, 12), rebalanceBar(3, 15), rebalanceBar(4, 15)}
	g2 := driftA(rebalanceStart, ret(10, 12))
	cases := []struct {
		name, side string
		// want 是第 2～4 根的系数；triggers 是第 2～4 根多头侧的触发原因。
		want     []map[string]float64
		triggers []string
	}{
		{"none", "{rebalance: none}", []map[string]float64{g2, driftA(g2, ret(12, 15)), driftA(g2, ret(12, 15))}, []string{"", "", ""}},
		{"every_bar", "{rebalance: every_bar}", func() []map[string]float64 {
			first := pullLong(g2, 1)
			second := pullLong(driftA(first, ret(12, 15)), 1)
			return []map[string]float64{first, second, second}
		}(), []string{"every_bar", "every_bar", "every_bar"}},
		// 锚点默认是漂移起点（第 1 根）：相位为 0 的是第 3 根。
		{"periodic", "{rebalance: periodic, period_bars: 2}", func() []map[string]float64 {
			third := pullLong(driftA(g2, ret(12, 15)), 1)
			return []map[string]float64{g2, third, third}
		}(), []string{"", "periodic", ""}},
		// 第 2 根 l2 的侧内占比 0.2/0.56 相对 0.4 偏离约 10.7%，超过 10% 触发；第 3 根再次偏离。
		{"threshold", "{rebalance: threshold, threshold: 0.1}", func() []map[string]float64 {
			first := pullLong(g2, 1)
			second := pullLong(driftA(first, ret(12, 15)), 1)
			return []map[string]float64{first, second, second}
		}(), []string{"threshold", "threshold", ""}},
		{"threshold 未超过", "{rebalance: threshold, threshold: 0.2}", func() []map[string]float64 {
			third := pullLong(driftA(g2, ret(12, 15)), 1)
			return []map[string]float64{g2, third, third}
		}(), []string{"", "threshold", ""}},
		// both：第 3 根偏离与周期同时成立，记 periodic。
		{"both", "{rebalance: both, period_bars: 2, threshold: 0.1}", func() []map[string]float64 {
			first := pullLong(g2, 1)
			second := pullLong(driftA(first, ret(12, 15)), 1)
			return []map[string]float64{first, second, second}
		}(), []string{"threshold", "periodic", ""}},
		// spread_bars 2：第 2 根触发后走一半，第 3 根不重新触发、走完剩下的；第 4 根再次触发（构成已在目标上，不变）。
		{"every_bar 分两步", "{rebalance: every_bar, spread_bars: 2}", func() []map[string]float64 {
			first := pullLong(g2, 2)
			second := pullLong(driftA(first, ret(12, 15)), 1)
			return []map[string]float64{first, second, pullLong(second, 2)}
		}(), []string{"every_bar", "", "every_bar"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := driftConfig(t, "  allocation:\n    method: drift\n    ratios: {l1: 0.3, l2: 0.2, s: 0.5}\n    sides: {long: "+tc.side+"}\n", engine.PoolClock{}, rebalanceMembers...)
			decisions, _ := runDrift(t, cfg, engine.State{}, bars)
			for i, want := range tc.want {
				decision := decisions[i+1]
				requireCoefficients(t, fmt.Sprintf("第 %d 根", i+2), decision, want)
				long := decision.Summary.Pool.Drift.Sides[0]
				if long.Side != dsl.SideLong || long.Trigger != tc.triggers[i] {
					t.Fatalf("第 %d 根多头侧的触发 %+v，期望 %q", i+2, long, tc.triggers[i])
				}
			}
		})
	}
}

// 分步期间再次满足触发条件不打断这一次：剩余根数逐根减少，走完后再判断。
func TestDriftSpreadIgnoresNewTriggers(t *testing.T) {
	cfg := driftConfig(t, "  allocation:\n    method: drift\n    ratios: {l1: 0.3, l2: 0.2, s: 0.5}\n    sides: {long: {rebalance: threshold, threshold: 0.01, spread_bars: 3}}\n", engine.PoolClock{}, rebalanceMembers...)
	decisions, _ := runDrift(t, cfg, engine.State{}, []driftStep{rebalanceBar(1, 10), rebalanceBar(2, 12), rebalanceBar(3, 15), rebalanceBar(4, 18), rebalanceBar(5, 18)})
	g := pullLong(g2Of(), 3)
	requireCoefficients(t, "第 2 根走 1/3", decisions[1], g)
	g = pullLong(driftA(g, ret(12, 15)), 2)
	requireCoefficients(t, "第 3 根走 1/2（偏离仍超过阈值，不重新触发）", decisions[2], g)
	g = pullLong(driftA(g, ret(15, 18)), 1)
	requireCoefficients(t, "第 4 根精确到达", decisions[3], g)
	for i, want := range []engine.PoolSideRebalance{
		{Side: dsl.SideLong, Rebalance: dsl.RebalanceThreshold, Trigger: "threshold", Moved: true, Remaining: 2, Reason: "threshold"},
		{Side: dsl.SideLong, Rebalance: dsl.RebalanceThreshold, Moved: true, Remaining: 1, Reason: "threshold"},
		{Side: dsl.SideLong, Rebalance: dsl.RebalanceThreshold, Moved: true, Reason: "threshold"},
	} {
		got := decisions[i+1].Summary.Pool.Drift.Sides[0]
		got.Deviation = nil
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 根的再平衡明细 %+v，期望 %+v", i+2, got, want)
		}
	}
	// 第 5 根价格不变、构成已在目标上：不触发。
	if long := decisions[4].Summary.Pool.Drift.Sides[0]; long.Trigger != "" || long.Moved {
		t.Fatalf("走完之后构成在目标上不应触发：%+v", long)
	}
	if spread := driftState(t, decisions[3]).Spread; len(spread) != 0 {
		t.Fatalf("走完之后分步状态清空：%+v", spread)
	}
}

func g2Of() map[string]float64 { return driftA(rebalanceStart, ret(10, 12)) }

// periodic 的触发点落在被跳过的那一根（池跳过或缺期）时，在之后第一根成功求值的 bar 上补触发。
func TestDriftPeriodicCatchesUpSkippedBar(t *testing.T) {
	cfg := driftConfig(t, "  allocation:\n    method: drift\n    ratios: {l1: 0.3, l2: 0.2, s: 0.5}\n    sides: {long: {rebalance: periodic, period_bars: 2}}\n", engine.PoolClock{}, rebalanceMembers...)
	for name, skipped := range map[string][]driftStep{
		"池跳过": {{3, nil, nil}},
		"缺期":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			steps := append([]driftStep{rebalanceBar(1, 10), rebalanceBar(2, 12)}, skipped...)
			decisions, _ := runDrift(t, cfg, engine.State{}, append(steps, rebalanceBar(4, 15)))
			last := decisions[len(decisions)-1]
			// 第 3 根是相位 0 的触发点但没有求值；第 4 根相位不为 0，仍补触发一次。
			requireCoefficients(t, "第 4 根补触发", last, pullLong(driftA(g2Of(), ret(12, 15)), 1))
			if long := last.Summary.Pool.Drift.Sides[0]; long.Trigger != dsl.RebalancePeriodic {
				t.Fatalf("应补触发 periodic：%+v", long)
			}
		})
	}
	// 写了 anchor 时相位从锚点起算：锚点在第 2 根，第 2、4 根是触发点。
	anchored := driftConfig(t, "  allocation:\n    method: drift\n    ratios: {l1: 0.3, l2: 0.2, s: 0.5}\n    sides: {long: {rebalance: periodic, period_bars: 2, anchor: \"2026-01-01T01:00:00Z\"}}\n",
		engine.PoolClock{Anchors: map[string]int64{dsl.SideLong: 2}}, rebalanceMembers...)
	decisions, _ := runDrift(t, anchored, engine.State{}, []driftStep{rebalanceBar(1, 10), rebalanceBar(2, 12), rebalanceBar(3, 15)})
	if decisions[1].Summary.Pool.Drift.Sides[0].Trigger != dsl.RebalancePeriodic || decisions[2].Summary.Pool.Drift.Sides[0].Trigger != "" {
		t.Fatalf("锚点决定相位：%+v %+v", decisions[1].Summary.Pool.Drift.Sides[0], decisions[2].Summary.Pool.Drift.Sides[0])
	}
}

// threshold 不判断目标比例为 0 的成员（它的侧内目标占比为 0，相除没有意义）；再平衡后它的系数仍为 0。
func TestDriftThresholdSkipsZeroTargets(t *testing.T) {
	members := append(append([][2]string{}, rebalanceMembers...), [2]string{"l3", dsl.SideLong})
	cfg := driftConfig(t, "  allocation:\n    method: drift\n    ratios: {l1: 0.3, l2: 0.2, s: 0.5, l3: 0}\n    sides: {long: {rebalance: threshold, threshold: 0.1}}\n", engine.PoolClock{}, members...)
	step := func(index int64, a float64) driftStep {
		bar := rebalanceBar(index, a)
		bar.prices[swapD] = 10
		bar.members = append(bar.members, pass("l3", map[string]string{swapD: "1"}))
		return bar
	}
	decisions, _ := runDrift(t, cfg, engine.State{}, []driftStep{step(1, 10), step(2, 12)})
	long := decisions[1].Summary.Pool.Drift.Sides[0]
	if long.Deviation == nil || math.IsNaN(*long.Deviation) || math.IsInf(*long.Deviation, 0) || long.Trigger != dsl.RebalanceThreshold {
		t.Fatalf("偏离只看目标不为 0 的成员：%+v", long)
	}
	want := pullLong(g2Of(), 1)
	want["l3"] = 0
	requireCoefficients(t, "第 2 根", decisions[1], want)
}

// drift_by: nav：侧的总量仍按名义口径漂移，侧内构成按 g·(1 + π) 分配。空头 s1 持有的 C 涨 20%：名义口径下它的占比变大，资金口径下变小。
func TestDriftByNAV(t *testing.T) {
	members := [][2]string{{"l", dsl.SideLong}, {"s1", dsl.SideShort}, {"s2", dsl.SideShort}}
	step := func(index int64, c float64) driftStep {
		return driftStep{index, map[string]float64{swapA: 10, swapC: c, swapD: 10}, []MemberResult{
			pass("l", map[string]string{swapA: "1"}), pass("s1", map[string]string{swapC: "-1"}), pass("s2", map[string]string{swapD: "-1"}),
		}}
	}
	bars := []driftStep{step(1, 10), step(2, 12)}
	rC := ret(10, 12)
	denominator := 1 + 0.25*(-rC)
	notional := map[string]float64{"l": 0.5 / denominator, "s1": 0.25 * (1 + rC) / denominator, "s2": 0.25 / denominator}
	total := notional["s1"] + notional["s2"]
	weight := 0.25 * (1 - rC)
	nav := map[string]float64{"l": notional["l"], "s1": total * weight / (weight + 0.25), "s2": total * 0.25 / (weight + 0.25)}
	for name, tc := range map[string]struct {
		side string
		want map[string]float64
	}{
		"notional": {"{drift_by: notional}", notional},
		"nav":      {"{drift_by: nav}", nav},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := driftConfig(t, "  allocation:\n    method: drift\n    ratios: {l: 0.5, s1: 0.25, s2: 0.25}\n    sides: {short: "+tc.side+"}\n", engine.PoolClock{}, members...)
			decisions, _ := runDrift(t, cfg, engine.State{}, bars)
			requireCoefficients(t, "第 2 根", decisions[1], tc.want)
		})
	}
}

// 条件转移（P2）与 drift：只作用于本根合成用的比例，状态里的系数（下一根漂移的起点）与不写 switch 时完全相同。
func TestDriftSwitchDoesNotFeedCoefficients(t *testing.T) {
	members := append(append([][2]string{}, rebalanceMembers...), [2]string{"probe", dsl.SideLong})
	allocation := "  allocation:\n    method: drift\n    ratios: {l1: 0.3, l2: 0.2, s: 0.5, probe: 0}\n    sides: {long: {rebalance: threshold, threshold: 0.5}}\n"
	plain := driftConfig(t, allocation, engine.PoolClock{}, members...)
	switched := driftConfig(t, allocation+"    switch: [{when: {empty: probe}, move: {from: l2, to: [l1]}}]\n", engine.PoolClock{}, members...)
	step := func(index int64, a float64) driftStep {
		bar := rebalanceBar(index, a)
		bar.members = append(bar.members, pass("probe", nil))
		return bar
	}
	bars := []driftStep{step(1, 10), step(2, 12), step(3, 15)}
	want, _ := runDrift(t, plain, engine.State{}, bars)
	got, _ := runDrift(t, switched, engine.State{}, bars)
	for i := range bars {
		if !reflect.DeepEqual(driftState(t, got[i]).G, driftState(t, want[i]).G) {
			t.Fatalf("第 %d 根的系数被转移改变：%v 与 %v", i+1, driftState(t, got[i]).G, driftState(t, want[i]).G)
		}
		if sw := got[i].Summary.Pool.Switch; sw == nil || sw.From != "l2" {
			t.Fatalf("第 %d 根应转移：%+v", i+1, sw)
		}
		composed := composedRatios(got[i])
		if composed["l2"] != "0" || composed["l1"] == ratios(got[i])["l1"] {
			t.Fatalf("第 %d 根合成比例应含转移：%v", i+1, composed)
		}
	}
	// 转移后多头侧的总量不变。
	g := driftState(t, got[2]).G
	sum, _ := decimalRatio(g["l1"])
	l2, _ := decimalRatio(g["l2"])
	if composedRatios(got[2])["l1"] != sum.Add(l2).String() {
		t.Fatalf("同侧转移的总量应不变：%v", composedRatios(got[2]))
	}
}

// freezeMembers 是冻结用例的成员：多头 l1（A）、l2（B），空头 s（C）。
func freezeBar(index int64, a, c float64) driftStep {
	return driftStep{index, map[string]float64{swapA: a, swapB: 10, swapC: c}, []MemberResult{
		pass("l1", map[string]string{swapA: "1"}), pass("l2", map[string]string{swapB: "1"}), pass("s", map[string]string{swapC: "-1"}),
	}}
}

// 波动冻结（P6）：空头组合净值一根内跌 20% 超过 10%，从触发那根起冻结 2 根：冻结中跳过侧内再平衡（分步暂停、不消耗剩余根数）
// 与总杠杆区间，直接用漂移后的系数；P1 与 portfolio 的硬约束照常执行；到期后恢复，分步接着走完。
func TestDriftFreeze(t *testing.T) {
	body := "  allocation:\n    method: drift\n    ratios: {l1: 0.3, l2: 0.2, s: 0.5}\n    sides: {long: {rebalance: every_bar, spread_bars: 3}}\n" +
		"  freeze: {watch: short, window_bars: 1, threshold: 0.1, bars: 2}\n  gross: {min: 0.5, max: 1.1}\n"
	cfg := sidedConfig(t, body, "portfolio: {leverage: 3, max_weight: 0.6, limits: {long_max: 0.25}}\n", rebalanceMembers...)
	decisions, _ := runDrift(t, cfg, engine.State{}, []driftStep{
		freezeBar(1, 10, 10), freezeBar(2, 12, 10), freezeBar(3, 12, 12), freezeBar(4, 10, 12), freezeBar(5, 10, 12), freezeBar(6, 10, 12),
	})
	type expect struct {
		triggered, frozen, paused bool
		until                     int64
		remaining                 int
	}
	for i, want := range []expect{
		{remaining: 2},
		{triggered: true, frozen: true, paused: true, until: 5, remaining: 2},
		{frozen: true, paused: true, until: 5, remaining: 2},
		{remaining: 1},
		{},
	} {
		drift := decisions[i+1].Summary.Pool.Drift
		freeze, long := drift.Freeze, drift.Sides[0]
		if freeze == nil || freeze.Triggered != want.triggered || freeze.Frozen != want.frozen || freeze.Until != want.until || long.Paused != want.paused || long.Remaining != want.remaining {
			t.Fatalf("第 %d 根：冻结 %+v，多头侧 %+v，期望 %+v", i+2, freeze, long, want)
		}
	}
	// 触发那根的涨跌幅：空头成员权益 1 → 0.8。
	if change := decisions[2].Summary.Pool.Drift.Freeze.Change; change == nil || math.Abs(*change+0.2) > 1e-12 {
		t.Fatalf("涨跌幅 %v", change)
	}
	for _, i := range []int{2, 3} {
		pool := decisions[i].Summary.Pool
		if len(pool.Adjustments) != 0 || pool.Drift.Sides[0].Moved {
			t.Fatalf("第 %d 根冻结中不再平衡、不做总杠杆区间：%+v", i+1, pool)
		}
		for _, member := range pool.Members {
			if member.Drifted != member.Ratio {
				t.Fatalf("第 %d 根冻结中直接用漂移后的系数：%+v", i+1, member)
			}
		}
		if decisions[i].Summary.Limits == nil {
			t.Fatalf("第 %d 根冻结中仍执行单标的上限", i+1)
		}
		shortC := false
		for _, target := range decisions[i].Targets {
			if target.Weight.Cmp(cfg.Portfolio.Limits.LongMax) > 0 {
				t.Fatalf("第 %d 根的池目标超过 long_max：%+v", i+1, target)
			}
			// 空头系数约 0.62～0.67，超过 max_weight 0.6 的部分留现金。
			if target.InstrumentID == "C" {
				shortC = target.Weight.Neg().Cmp(cfg.Portfolio.MaxWeight) == 0
			}
		}
		if !shortC {
			t.Fatalf("第 %d 根冻结中仍执行 max_weight：%+v", i+1, decisions[i].Targets)
		}
	}
	// 空头亏损让系数总量升到约 1.2，冻结中超过 gross.max 也不缩；解除后第 5 根缩到 1.1。
	if adjustments := decisions[4].Summary.Pool.Adjustments; len(adjustments) != 1 || adjustments[0].Kind != "gross" {
		t.Fatalf("解除冻结后执行总杠杆区间：%+v", adjustments)
	}
	if final := driftState(t, decisions[5]); final.FreezeUntil != 0 || len(final.Spread) != 0 {
		t.Fatalf("冻结到期、分步走完后状态清零：%+v", final)
	}
	// 停用再启用：从空状态开始，第一根是漂移起点，系数回到 ratios，冻结与分步都没有。
	restart, _ := runDrift(t, cfg, engine.State{}, []driftStep{freezeBar(10, 10, 12)})
	fresh := driftState(t, restart[0])
	if restart[0].Summary.Pool.Drift.Phase != engine.DriftStart || fresh.Start != 10 || fresh.FreezeUntil != 0 || fresh.Spread != nil || fresh.G["l1"] != 0.3 {
		t.Fatalf("重新启用后状态应清零：%+v", fresh)
	}
}

// 漂移起点（P8）：clock 给出起点时，之前的根按起始比例合成、不写状态；起点那一根把系数置为 initial.ratios（或 ratios），
// periodic 的默认锚点是起点。
func TestDriftStartAndInitialRatios(t *testing.T) {
	body := "  allocation:\n    method: drift\n    ratios: {l1: 0.3, l2: 0.2, s: 0.5}\n    initial: {at: \"2026-01-01T02:00:00Z\", ratios: {l1: 0.4, l2: 0.1, s: 0.6}}\n    sides: {long: {rebalance: periodic, period_bars: 2}}\n"
	cfg := driftConfig(t, body, engine.PoolClock{Start: 3, HasStart: true}, rebalanceMembers...)
	decisions, _ := runDrift(t, cfg, engine.State{}, []driftStep{rebalanceBar(1, 10), rebalanceBar(2, 12), rebalanceBar(3, 12), rebalanceBar(4, 15), rebalanceBar(5, 15)})
	initial := map[string]float64{"l1": 0.4, "l2": 0.1, "s": 0.6}
	for i := 0; i < 2; i++ {
		if decisions[i].Summary.Pool.Drift.Phase != engine.DriftPending || driftState(t, decisions[i]) != nil {
			t.Fatalf("第 %d 根还没到起点：%+v", i+1, decisions[i].Summary.Pool.Drift)
		}
		requireMap(t, "起点之前按起始比例合成", ratios(decisions[i]), map[string]string{"l1": "0.4", "l2": "0.1", "s": "0.6"})
	}
	if decisions[2].Summary.Pool.Drift.Phase != engine.DriftStart {
		t.Fatalf("第 3 根是起点：%+v", decisions[2].Summary.Pool.Drift)
	}
	requireCoefficients(t, "起点那一根是 initial.ratios（不漂移、不再平衡）", decisions[2], initial)
	if start := driftState(t, decisions[2]).Start; start != 3 {
		t.Fatalf("起点序号 %d", start)
	}
	// 第 4 根：A 涨 25% 漂移；锚点是起点（第 3 根），相位 1 不触发；第 5 根相位 0 触发。
	g4 := driftA(initial, ret(12, 15))
	requireCoefficients(t, "第 4 根", decisions[3], g4)
	total := g4["l1"] + g4["l2"]
	requireCoefficients(t, "第 5 根按 ratios 的侧内构成再平衡", decisions[4], map[string]float64{"l1": total * 0.6, "l2": total * 0.4, "s": g4["s"]})
	if decisions[4].Summary.Pool.Drift.Sides[0].Trigger != dsl.RebalancePeriodic {
		t.Fatalf("第 5 根应按周期触发：%+v", decisions[4].Summary.Pool.Drift.Sides[0])
	}
	// 只写 start（没有 initial）时，起点的系数就是 ratios。
	started := driftConfig(t, strings.Replace(body, "    initial: {at: \"2026-01-01T02:00:00Z\", ratios: {l1: 0.4, l2: 0.1, s: 0.6}}\n", "    start: \"2026-01-01T02:00:00Z\"\n", 1), engine.PoolClock{Start: 3, HasStart: true}, rebalanceMembers...)
	decisions, _ = runDrift(t, started, engine.State{}, []driftStep{rebalanceBar(3, 12)})
	requireCoefficients(t, "start 的起点", decisions[0], rebalanceStart)
}

// 写了 initial 时池在 at 之前已经推进过（起点前的根），漂移起点那一根仍按“初始化那一根”处理：gross 不放大，之后照常放大。
func TestDriftStartBarDoesNotScaleUpGross(t *testing.T) {
	body := "  allocation:\n    method: drift\n    ratios: {l1: 0.3, l2: 0.2, s: 0.5}\n    initial: {at: \"2026-01-01T02:00:00Z\", ratios: {l1: 0.2, l2: 0.1, s: 0.2}}\n  gross: {min: 0.8}\n"
	cfg := driftConfig(t, body, engine.PoolClock{Start: 3, HasStart: true}, rebalanceMembers...)
	decisions, _ := runDrift(t, cfg, engine.State{}, []driftStep{rebalanceBar(1, 10), rebalanceBar(2, 10), rebalanceBar(3, 10), rebalanceBar(4, 10)})
	if len(decisions[1].Summary.Pool.Adjustments) != 1 {
		t.Fatalf("起点之前、池第一根之后的根照常放大：%+v", decisions[1].Summary.Pool.Adjustments)
	}
	if adjustments := decisions[2].Summary.Pool.Adjustments; len(adjustments) != 0 {
		t.Fatalf("漂移起点那一根不放大：%+v", adjustments)
	}
	requireCoefficients(t, "漂移起点", decisions[2], map[string]float64{"l1": 0.2, "l2": 0.1, "s": 0.2})
	adjustments := decisions[3].Summary.Pool.Adjustments
	if len(adjustments) != 1 || adjustments[0].Kind != "gross" || math.Abs(adjustments[0].Factor-0.8/0.5) > 1e-12 {
		t.Fatalf("起点之后总敞口 0.5 低于 0.8 应放大：%+v", adjustments)
	}
	requireCoefficients(t, "起点之后放大", decisions[3], map[string]float64{"l1": 0.2 * (0.8 / 0.5), "l2": 0.1 * (0.8 / 0.5), "s": 0.2 * (0.8 / 0.5)})
}

// 预热里一段空缺超过补齐上限 maxFill（4096 根）：净值序列只补到上限、再截到窗口所需的长度，漂移按相对上一次估值价的累计收益计算。
func TestDriftLongGapBeyondMaxFill(t *testing.T) {
	body := "  allocation:\n    method: drift\n    ratios: {l1: 0.3, l2: 0.2, s: 0.5}\n  freeze: {watch: [l1], window_bars: 3, threshold: 0.5, bars: 1}\n"
	cfg := driftConfig(t, body, engine.PoolClock{}, rebalanceMembers...)
	decisions, _ := runDrift(t, cfg, engine.State{}, []driftStep{rebalanceBar(1, 10), rebalanceBar(2, 10), rebalanceBar(2+maxFill+1000, 12)})
	requireCoefficients(t, "空缺之后的一根", decisions[2], driftA(rebalanceStart, ret(10, 12)))
	decoded, err := DecodeState(decisions[2].State.Pool)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(decoded.Series["l1"]); got != 4 {
		t.Fatalf("净值序列保留窗口 3 + 1 个值，实际 %d", got)
	}
	if change := decisions[2].Summary.Pool.Drift.Freeze.Change; change == nil || math.Abs(*change-0.2) > 1e-12 {
		t.Fatalf("窗口起点取补齐的净值：%v", change)
	}
}

// 不写 drift 的池不出现 drift 的状态与摘要字段。
func TestNoDriftFieldsWithoutDrift(t *testing.T) {
	cfg := sidedConfig(t, "  allocation: {method: fixed, ratios: {l: 0.8, s: 0.2}}\n", "portfolio: {leverage: 2}\n", longShort...)
	decisions, state := run(t, cfg, []bar{longShortBar()})
	if decisions[0].Summary.Pool.Drift != nil || strings.Contains(string(state.Pool), "drift") {
		t.Fatalf("没写 drift 时不应有 drift 字段：%+v %s", decisions[0].Summary.Pool, state.Pool)
	}
	for _, member := range decisions[0].Summary.Pool.Members {
		if member.Drifted != "" {
			t.Fatalf("没写 drift 时不应有 drifted：%+v", member)
		}
	}
}
