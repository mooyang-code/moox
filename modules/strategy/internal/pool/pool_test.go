package pool

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

const (
	spotX = "X|spot"
	spotY = "Y|spot"
	swapZ = "Z|swap"
)

// poolConfig 解析一份只有分配部分不同的池定义；手续费为 0，手算更方便。
func poolConfig(t *testing.T, allocation, portfolio string, members ...string) Config {
	t.Helper()
	if len(members) == 0 {
		members = []string{"a", "b"}
	}
	var list strings.Builder
	for _, id := range members {
		fmt.Fprintf(&list, "    - {id: %s, strategy: s_%s}\n", id, id)
	}
	raw := "name: pool\nbar: 1h\nstrategy_pool:\n  members:\n" + list.String() + allocation + "  fees: {spot_bps: 0, swap_bps: 0}\n" + portfolio
	strategy, err := dsl.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("解析池失败：%v\n%s", err, raw)
	}
	mode := dsl.SingleMarket(dsl.MarketSpot)
	for _, member := range members {
		if member == "swapper" {
			mode = dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}
		}
	}
	cfg, err := NewConfig(strategy, mode)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// pass 构造一个通过的成员结果：目标是（标的|市场 → 权重）。
func pass(id string, weights map[string]string) MemberResult {
	targets := make([]engine.Target, 0, len(weights))
	state := engine.State{Targets: map[string]string{}}
	for key, weight := range weights {
		instrument, market := engine.SplitTargetKey(key)
		targets = append(targets, engine.Target{InstrumentID: instrument, Market: market, Weight: quant.Must(weight)})
		state.Targets[key] = weight
	}
	return MemberResult{ID: id, Decision: &engine.Decision{Status: engine.StatusOK, Targets: targets, State: state}}
}

func fail(id, reason string) MemberResult {
	return MemberResult{ID: id, Reason: reason, Detail: "测试"}
}

// run 依次推进若干期：bars[i] 是第 i+1 期的价格与成员结果，返回每期的决策。
type bar struct {
	prices  map[string]float64
	members []MemberResult
}

func run(t *testing.T, cfg Config, bars []bar) ([]engine.Decision, engine.State) {
	t.Helper()
	var decisions []engine.Decision
	state := engine.State{}
	for i, b := range bars {
		decision, err := Step(cfg, state, Input{BarIndex: int64(i + 1), Prices: b.prices, Members: b.members})
		if err != nil {
			t.Fatalf("第 %d 期：%v", i+1, err)
		}
		decisions = append(decisions, decision)
		state = decision.State
	}
	return decisions, state
}

func ratios(decision engine.Decision) map[string]string {
	out := map[string]string{}
	for _, member := range decision.Summary.Pool.Members {
		out[member.ID] = member.Ratio
	}
	return out
}

func targets(decision engine.Decision) map[string]string {
	out := map[string]string{}
	for _, target := range decision.Targets {
		out[engine.TargetKey(target.InstrumentID, target.Market)] = target.Weight.String()
	}
	return out
}

func requireMap(t *testing.T, label string, got, want map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v，期望 %v", label, got, want)
	}
}

func TestFixedRatiosScaleMemberTargets(t *testing.T) {
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 0.6, b: 0.4}}\n", "")
	decisions, _ := run(t, cfg, []bar{{prices: map[string]float64{spotX: 10, spotY: 5}, members: []MemberResult{
		pass("a", map[string]string{spotX: "0.5"}), pass("b", map[string]string{spotY: "1"}),
	}}})
	decision := decisions[0]
	if decision.Status != engine.StatusOK {
		t.Fatalf("状态 %s", decision.Status)
	}
	requireMap(t, "池目标", targets(decision), map[string]string{spotX: "0.3", spotY: "0.4"})
	requireMap(t, "比例", ratios(decision), map[string]string{"a": "0.6", "b": "0.4"})
	pool := decision.Summary.Pool
	if pool.Method != dsl.AllocationFixed || pool.Members[0].Gross != "0.3" || pool.Members[1].Net != "0.4" || pool.Members[0].Targets[spotX] != "0.3" {
		t.Fatalf("明细不符：%+v", pool)
	}
	if decision.Summary.Net != "0.7" || decision.Summary.Cash != "0.3" {
		t.Fatalf("摘要的净敞口与现金不符：%+v", decision.Summary)
	}
}

func TestFixedRatiosMayExceedOneButLeverageCapsThem(t *testing.T) {
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 1, b: 1}}\n", "portfolio: {leverage: 1}\n")
	decisions, _ := run(t, cfg, []bar{{prices: map[string]float64{spotX: 10, spotY: 5}, members: []MemberResult{
		pass("a", map[string]string{spotX: "1"}), pass("b", map[string]string{spotY: "1"}),
	}}})
	// 两个成员各满仓、比例都是 1：现货合计 2 超过预算 1，按比例缩小，多出的留作现金。
	requireMap(t, "池目标", targets(decisions[0]), map[string]string{spotX: "0.5", spotY: "0.5"})
}

func TestMaxWeightCapsPoolTargets(t *testing.T) {
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n", "portfolio: {max_weight: 0.3}\n")
	decisions, _ := run(t, cfg, []bar{{prices: map[string]float64{spotX: 10}, members: []MemberResult{
		pass("a", map[string]string{spotX: "0.8"}), pass("b", map[string]string{spotX: "0.8"}),
	}}})
	// 两个成员各 0.8 × 0.5 = 0.4，合成 0.8，超过 max_weight 0.3，截顶后其余留作现金。
	requireMap(t, "池目标", targets(decisions[0]), map[string]string{spotX: "0.3"})
	if !strings.Contains(strings.Join(decisions[0].Summary.Notes, ";"), "max_weight") {
		t.Fatalf("摘要应说明截顶：%v", decisions[0].Summary.Notes)
	}
}

// 成员 a 满仓 X，价格 100、110、121、100：账本手续费为 0，期末净值 1、1.1、1.21、1.0。
func risingThenFalling() []bar {
	prices := []float64{100, 110, 121, 100, 100}
	bars := make([]bar, 0, len(prices))
	for _, price := range prices {
		bars = append(bars, bar{prices: map[string]float64{spotX: price, spotY: 50}, members: []MemberResult{
			pass("a", map[string]string{spotX: "1"}), pass("b", map[string]string{spotY: "1"}),
		}})
	}
	return bars
}

func equities(decision engine.Decision) map[string]float64 {
	out := map[string]float64{}
	for _, member := range decision.Summary.Pool.Members {
		out[member.ID] = member.Equity
	}
	return out
}

func TestTimingUsesNetValueUpToPreviousBar(t *testing.T) {
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n  timing: {window: 3}\n", "")
	decisions, _ := run(t, cfg, risingThenFalling())
	wantEquity := []float64{1, 1.1, 1.21, 1.0, 1.0}
	for i, want := range wantEquity {
		if got := equities(decisions[i])["a"]; math.Abs(got-want) > 1e-12 {
			t.Fatalf("第 %d 期 a 的期末净值 %v，期望 %v", i+1, got, want)
		}
	}
	// 前 3 期序列不足 3 个值：系数取 1；第 4 期序列 [1, 1.1, 1.21]，最后值 1.21 高于均线 1.1033：取 1；
	// 第 5 期序列 [1.1, 1.21, 1.0]（只保留 3 个），最后值 1.0 低于均线 1.1033：取 0。
	wantTiming := []string{"1", "1", "1", "1", "0"}
	for i, want := range wantTiming {
		if got := decisions[i].Summary.Pool.Members[0].Timing; got != want {
			t.Fatalf("第 %d 期 a 的择时系数 %q，期望 %q", i+1, got, want)
		}
	}
	// b 的净值恒为 1（只是现货满仓，价格不变），不高于均线：系数一旦能判断就是 0。
	if got := decisions[3].Summary.Pool.Members[1].Timing; got != "0" {
		t.Fatalf("b 的净值不高于均线，系数应为 0：%q", got)
	}
	requireMap(t, "第 5 期比例", ratios(decisions[4]), map[string]string{"a": "0", "b": "0"})
	if len(decisions[4].Targets) != 0 {
		t.Fatalf("择时全为 0 时池目标为空：%v", targets(decisions[4]))
	}
}

func TestRatioDependsOnlyOnNetValueUpToPreviousBar(t *testing.T) {
	cfg := poolConfig(t, "  allocation:\n    method: rotation\n    rotation: {top: 1, factors: [{name: return, window: 1}]}\n  timing: {window: 2}\n", "")
	base := risingThenFalling()
	history, state := run(t, cfg, base[:3])
	_ = history
	// 同一个上期状态，本期价格天差地别：本期的比例一样（只用到上一期末为止的净值），本期末净值不一样。
	up := Input{BarIndex: 4, Prices: map[string]float64{spotX: 1000, spotY: 50}, Members: base[3].members}
	down := Input{BarIndex: 4, Prices: map[string]float64{spotX: 1, spotY: 50}, Members: base[3].members}
	decUp, err := Step(cfg, state, up)
	if err != nil {
		t.Fatal(err)
	}
	decDown, err := Step(cfg, state, down)
	if err != nil {
		t.Fatal(err)
	}
	requireMap(t, "本期比例", ratios(decUp), ratios(decDown))
	for i := range decUp.Summary.Pool.Members {
		a, b := decUp.Summary.Pool.Members[i], decDown.Summary.Pool.Members[i]
		if a.Base != b.Base || a.Timing != b.Timing || a.RatioBefore != b.RatioBefore || a.Ratio != b.Ratio {
			t.Fatalf("成员 %s 的比例用到了本期的净值：%+v %+v", a.ID, a, b)
		}
	}
	if equities(decUp)["a"] == equities(decDown)["a"] {
		t.Fatal("本期末净值应随本期价格变化")
	}
}

func TestRotationReturnTopOneAndInsufficientHistory(t *testing.T) {
	cfg := poolConfig(t, "  allocation:\n    method: rotation\n    rotation: {top: 1, factors: [{name: return, window: 1}]}\n", "")
	decisions, _ := run(t, cfg, risingThenFalling())
	// 第 1、2 期序列只有 0、1 个值，return(1) 需要 2 个：不排名，未爆仓的成员均分。
	requireMap(t, "第 1 期", ratios(decisions[0]), map[string]string{"a": "0.5", "b": "0.5"})
	requireMap(t, "第 2 期", ratios(decisions[1]), map[string]string{"a": "0.5", "b": "0.5"})
	if decisions[1].Summary.Pool.Members[0].Rank != nil {
		t.Fatal("没有排名时不给名次分")
	}
	// 第 3 期序列 [1, 1.1]：a 收益 0.1 排第 1，b 收益 0 排第 2，top 1 → a 独得 1。
	requireMap(t, "第 3 期", ratios(decisions[2]), map[string]string{"a": "1", "b": "0"})
	if rank := decisions[2].Summary.Pool.Members[0].Rank; rank == nil || *rank != 1 {
		t.Fatalf("a 的名次分应为 1：%v", rank)
	}
	// 第 4 期序列 [1, 1.1, 1.21]：a 仍领先；第 5 期序列 [.., 1.0]：a 收益 −0.17，b 收益 0，换成 b。
	requireMap(t, "第 4 期", ratios(decisions[3]), map[string]string{"a": "1", "b": "0"})
	requireMap(t, "第 5 期", ratios(decisions[4]), map[string]string{"a": "0", "b": "1"})
	requireMap(t, "第 5 期池目标", targets(decisions[4]), map[string]string{spotY: "1"})
}

func TestRotationTiesShareTheTopSlots(t *testing.T) {
	cfg := poolConfig(t, "  allocation:\n    method: rotation\n    rotation: {top: 1, factors: [{name: return, window: 1}]}\n", "", "a", "b", "c")
	flat := func() []MemberResult {
		return []MemberResult{pass("a", map[string]string{spotX: "0.3"}), pass("b", map[string]string{spotX: "0.3"}), pass("c", map[string]string{spotY: "0.3"})}
	}
	bars := make([]bar, 4)
	for i := range bars {
		// a、b 的账本完全相同；c 的价格每期下跌。
		bars[i] = bar{prices: map[string]float64{spotX: 10, spotY: 10 - float64(i)}, members: flat()}
	}
	decisions, _ := run(t, cfg, bars)
	// 第 3 期起排名：a 与 b 收益都是 0 并列第一（平均名次 1.5），c 为负排第三。top 1 取到并列的 a、b，平分。
	last := ratios(decisions[3])
	if last["a"] != "0.5" || last["b"] != "0.5" || last["c"] != "0" {
		t.Fatalf("并列的平分：%v", last)
	}
	if rank := decisions[3].Summary.Pool.Members[0].Rank; rank == nil || *rank != 1.5 {
		t.Fatalf("并列的平均名次 1.5：%v", rank)
	}
}

func TestRotationFactorsWeightAndDirection(t *testing.T) {
	// 两个因子：return(1) 权重 1、drawdown(2) 权重 1（默认越小越好）。a 收益高但回撤大，b 收益低但没有回撤。
	cfg := poolConfig(t, "  allocation:\n    method: rotation\n    rotation:\n      top: 1\n      factors:\n        - {name: return, window: 1, weight: 1}\n        - {name: drawdown, window: 2, weight: 2}\n", "")
	prices := []float64{100, 120, 90, 150}
	bars := make([]bar, len(prices))
	for i, price := range prices {
		bars[i] = bar{prices: map[string]float64{spotX: price, spotY: 10 + float64(i)}, members: []MemberResult{
			pass("a", map[string]string{spotX: "1"}), pass("b", map[string]string{spotY: "1"}),
		}}
	}
	decisions, _ := run(t, cfg, bars)
	// 第 4 期序列（期末净值）：a = [1, 1.2, 0.9]，b 缓慢上涨。return(1)：a = 0.9/1.2−1 = −0.25，b > 0 → b 排第 1；
	// drawdown(2)：a 的回撤 (1.2−0.9)/1.2 = 0.25，b 没有回撤 → b 排第 1。b 综合名次分 1×1+1×2 = 3，a 为 2×1+2×2 = 6。
	last := decisions[3].Summary.Pool
	if last.Members[1].Ratio != "1" || last.Members[0].Ratio != "0" {
		t.Fatalf("b 应入选：%+v", last.Members)
	}
	if last.Members[1].Rank == nil || *last.Members[1].Rank != 3 || *last.Members[0].Rank != 6 {
		t.Fatalf("综合名次分不符：%v %v", *last.Members[0].Rank, *last.Members[1].Rank)
	}
	// 方向反转：return 升序后收益低的靠前。
	reversed := poolConfig(t, "  allocation:\n    method: rotation\n    rotation: {top: 1, factors: [{name: return, window: 1, order: asc}]}\n", "")
	decisions, _ = run(t, reversed, bars)
	if got := ratios(decisions[3]); got["a"] != "1" {
		t.Fatalf("升序时收益最低的 a 靠前：%v", got)
	}
}

func TestMARatioFactor(t *testing.T) {
	cfg := poolConfig(t, "  allocation:\n    method: rotation\n    rotation: {top: 1, factors: [{name: ma_ratio, window: 2}]}\n", "")
	prices := []float64{100, 100, 130, 100}
	bars := make([]bar, len(prices))
	for i, price := range prices {
		bars[i] = bar{prices: map[string]float64{spotX: price, spotY: 10 + float64(i)}, members: []MemberResult{
			pass("a", map[string]string{spotX: "1"}), pass("b", map[string]string{spotY: "1"}),
		}}
	}
	decisions, _ := run(t, cfg, bars)
	// 第 3 期序列 [1, 1]：a 净值 ÷ 均线 − 1 = 0，b 的净值在涨 → b 靠前；第 4 期序列 [1, 1.3]：a 1.3/1.15−1 > 0，b 的涨幅小 → a 靠前。
	if got := ratios(decisions[2]); got["b"] != "1" {
		t.Fatalf("第 3 期 b 靠前：%v", got)
	}
	if got := ratios(decisions[3]); got["a"] != "1" {
		t.Fatalf("第 4 期 a 靠前：%v", got)
	}
}

func TestStepLimitsRatioChange(t *testing.T) {
	cfg := poolConfig(t, "  allocation:\n    method: rotation\n    rotation: {top: 1, factors: [{name: return, window: 1}]}\n  step: 0.2\n", "")
	decisions, _ := run(t, cfg, risingThenFalling())
	// 第 3、4 期排名都是 a=1，从 0.5 起每期最多加 0.2：0.7、0.9；第 5 期排名换成 b=1，a 从 0.9 只能降到 0.7。
	for i, want := range []string{"0.5", "0.5", "0.7", "0.9", "0.7"} {
		if got := ratios(decisions[i])["a"]; got != want {
			t.Fatalf("第 %d 期 a 的比例 %s，期望 %s", i+1, got, want)
		}
	}
	last := decisions[4].Summary.Pool
	if last.Members[0].RatioBefore != "0" || last.Members[0].Ratio != "0.7" || last.Members[1].RatioBefore != "1" || last.Members[1].Ratio != "0.3" {
		t.Fatalf("步长限制前后：%+v", last.Members)
	}
	// 第 1 期没有上期比例，不受步长限制。
	if got := ratios(decisions[0]); got["a"] != "0.5" {
		t.Fatalf("第 1 期：%v", got)
	}
	// 之后继续限制：第 6 期同样的输入再走一步。
	state := decisions[4].State
	next, err := Step(cfg, state, Input{BarIndex: 6, Prices: map[string]float64{spotX: 100, spotY: 50}, Members: risingThenFalling()[4].members})
	if err != nil {
		t.Fatal(err)
	}
	if got := ratios(next); got["a"] != "0.5" || got["b"] != "0.5" {
		t.Fatalf("第 6 期继续按步长靠拢：%v", got)
	}
}

func TestFailedMemberKeepsPreviousWeightsAndLedgerIsValued(t *testing.T) {
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n", "")
	bars := []bar{
		{prices: map[string]float64{spotX: 100, spotY: 50}, members: []MemberResult{pass("a", map[string]string{spotX: "1"}), pass("b", map[string]string{spotY: "0.8"})}},
		// 第 2 期 b 没通过：沿用上期 0.8；它的账本照常估值（Y 涨 10%）但不调仓。
		{prices: map[string]float64{spotX: 100, spotY: 55}, members: []MemberResult{pass("a", map[string]string{spotX: "1"}), fail("b", "factor_skipped")}},
	}
	decisions, _ := run(t, cfg, bars)
	second := decisions[1]
	if second.Status != engine.StatusOK {
		t.Fatalf("有成员通过时池照常出 ok：%s", second.Status)
	}
	requireMap(t, "第 2 期池目标", targets(second), map[string]string{spotX: "0.5", spotY: "0.4"})
	b := second.Summary.Pool.Members[1]
	if b.Status != engine.StatusSkipped || b.Reason != "factor_skipped" || b.Detail != "测试" {
		t.Fatalf("没通过的成员应记原因：%+v", b)
	}
	// b 在第 1 期按 0.8 买入 Y（价格 50），第 2 期涨到 55：净值 = 0.2 + 0.8 × 1.1 = 1.08。
	if math.Abs(b.Equity-1.08) > 1e-12 {
		t.Fatalf("没通过的成员账本按收盘价估值：%v", b.Equity)
	}
}

func TestPoolSkipsOnlyWhenEveryMemberFailsAndFillsTheGap(t *testing.T) {
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n  timing: {window: 3}\n", "")
	first, err := Step(cfg, engine.State{}, Input{BarIndex: 1, Prices: map[string]float64{spotX: 100}, Members: []MemberResult{pass("a", map[string]string{spotX: "1"}), pass("b", map[string]string{spotX: "1"})}})
	if err != nil {
		t.Fatal(err)
	}
	skipped, err := Step(cfg, first.State, Input{BarIndex: 2, Members: []MemberResult{fail("a", "factor_skipped"), fail("b", "config_error")}})
	if err != nil {
		t.Fatal(err)
	}
	if skipped.Status != engine.StatusSkipped || skipped.SkipReason != "factor_skipped" || len(skipped.Targets) != 0 {
		t.Fatalf("所有成员都没通过时池 skipped：%+v", skipped)
	}
	if !strings.Contains(strings.Join(skipped.Summary.Notes, ""), "成员 b：config_error") {
		t.Fatalf("说明里应列出每个成员的原因：%v", skipped.Summary.Notes)
	}
	if string(skipped.State.Pool) != string(first.State.Pool) || !reflect.DeepEqual(skipped.State.Targets, first.State.Targets) {
		t.Fatal("池 skipped 时状态原样保留")
	}
	// 第 3 期正常求值：第 2 期没有被处理，成员净值沿用上一个值补齐，序列里每个位置对应连续的一期。
	third, err := Step(cfg, skipped.State, Input{BarIndex: 3, Prices: map[string]float64{spotX: 110}, Members: []MemberResult{pass("a", map[string]string{spotX: "1"}), pass("b", map[string]string{spotX: "1"})}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(third.State.Pool)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Series["a"]; len(got) != 3 || got[0] != got[1] || math.Abs(got[2]-1.1) > 1e-12 {
		t.Fatalf("净值序列应补齐缺的期：%v", got)
	}
	if _, err := Step(cfg, third.State, Input{BarIndex: 3, Prices: map[string]float64{spotX: 110}, Members: []MemberResult{pass("a", nil), pass("b", nil)}}); err == nil {
		t.Fatal("bar 序号没有前进应报错")
	}
}

func TestFrozenSubjectKeepsPreviousPoolWeight(t *testing.T) {
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n", "")
	first, err := Step(cfg, engine.State{}, Input{BarIndex: 1, Prices: map[string]float64{spotX: 100, spotY: 10}, Members: []MemberResult{
		pass("a", map[string]string{spotX: "0.6"}), pass("b", map[string]string{spotX: "0.2", spotY: "0.4"}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	requireMap(t, "第 1 期", targets(first), map[string]string{spotX: "0.4", spotY: "0.2"})
	// 第 2 期成员 a 因空洞冻结了 X（保持标记，权重是预留的 0.6），b 想把 X 调到 0.4：池里 X 保持上期池目标 0.4 不动。
	held := pass("a", map[string]string{spotX: "0.6"})
	held.Decision.Targets[0].Hold = true
	second, err := Step(cfg, first.State, Input{BarIndex: 2, Prices: map[string]float64{spotX: 100, spotY: 10}, Members: []MemberResult{
		held, pass("b", map[string]string{spotX: "0.4", spotY: "0.4"}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	requireMap(t, "第 2 期", targets(second), map[string]string{spotX: "0.4", spotY: "0.2"})
	var hold bool
	for _, target := range second.Targets {
		if target.InstrumentID == "X" {
			hold = target.Hold
		}
	}
	if !hold {
		t.Fatal("冻结的标的在池目标里带保持标记")
	}
}

func TestBankruptMemberGetsNoAllocation(t *testing.T) {
	cfg := poolConfig(t, "  allocation:\n    method: rotation\n    rotation: {top: 1, factors: [{name: return, window: 1}]}\n", "portfolio: {leverage: 10}\n", "swapper", "b")
	// 成员 swapper 5 倍永续多头，第 2 期暴跌 25%：账本权益为负，模拟爆仓。b 只是现货满仓、没有收益。
	prices := []map[string]float64{{swapZ: 10, spotY: 5}, {swapZ: 7.5, spotY: 5}, {swapZ: 7.5, spotY: 5}, {swapZ: 7.5, spotY: 5}}
	bars := make([]bar, len(prices))
	for i := range prices {
		bars[i] = bar{prices: prices[i], members: []MemberResult{pass("swapper", map[string]string{swapZ: "5"}), pass("b", map[string]string{spotY: "1"})}}
	}
	decisions, _ := run(t, cfg, bars)
	if !decisions[1].Summary.Pool.Members[0].Bankrupt {
		t.Fatalf("第 2 期应模拟爆仓：%+v", decisions[1].Summary.Pool.Members[0])
	}
	// 第 3 期起 swapper 不参与排名、比例 0，b 独得 1；净值不再被当成有效的收益去排名。
	for _, i := range []int{2, 3} {
		got := ratios(decisions[i])
		if got["swapper"] != "0" || got["b"] != "1" {
			t.Fatalf("第 %d 期爆仓成员比例为 0：%v", i+1, got)
		}
	}
	requireMap(t, "第 4 期池目标", targets(decisions[3]), map[string]string{spotY: "1"})
}

func TestBankruptMemberIgnoresStepAndFixedRatio(t *testing.T) {
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {swapper: 0.5, b: 0.5}}\n  step: 0.1\n", "portfolio: {leverage: 10}\n", "swapper", "b")
	prices := []map[string]float64{{swapZ: 10, spotY: 5}, {swapZ: 7.5, spotY: 5}, {swapZ: 7.5, spotY: 5}}
	bars := make([]bar, len(prices))
	for i := range prices {
		bars[i] = bar{prices: prices[i], members: []MemberResult{pass("swapper", map[string]string{swapZ: "5"}), pass("b", map[string]string{spotY: "1"})}}
	}
	decisions, _ := run(t, cfg, bars)
	if got := ratios(decisions[2]); got["swapper"] != "0" || got["b"] != "0.5" {
		t.Fatalf("fixed 方式下爆仓成员比例也归零，且不受步长限制：%v", got)
	}
}

func TestStateRoundTripKeepsFullPrecision(t *testing.T) {
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n  timing: {window: 3}\n", "")
	_, state := run(t, cfg, risingThenFalling())
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var back engine.State
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(back)
	if err != nil || string(again) != string(raw) {
		t.Fatalf("状态经 JSON 往返应不变：\n%s\n%s", raw, again)
	}
	before, _ := DecodeState(state.Pool)
	after, _ := DecodeState(back.Pool)
	for id, book := range before.Ledgers {
		if !reflect.DeepEqual(book, after.Ledgers[id]) {
			t.Fatalf("成员 %s 的账本往返后不一致：%+v %+v", id, book, after.Ledgers[id])
		}
	}
}

func TestMemberItemsGetMemberPrefix(t *testing.T) {
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 1, b: 1}}\n", "")
	a := pass("a", map[string]string{spotX: "0.5"})
	a.Decision.Items = []engine.Item{{RuleID: "r", InstrumentID: "X", Stage: engine.StageWeighted}}
	b := pass("b", map[string]string{spotY: "0.5"})
	b.Decision.Items = []engine.Item{{RuleID: "r", InstrumentID: "Y", Stage: engine.StageWeighted}}
	decisions, _ := run(t, cfg, []bar{{prices: map[string]float64{spotX: 1, spotY: 1}, members: []MemberResult{a, b}}})
	if len(decisions[0].Items) != 2 || decisions[0].Items[0].RuleID != "a/r" || decisions[0].Items[1].RuleID != "b/r" {
		t.Fatalf("明细的规则 id 要加成员前缀：%+v", decisions[0].Items)
	}
}

func TestMemberResultsMustCoverThePool(t *testing.T) {
	cfg := poolConfig(t, "  allocation: {method: fixed, ratios: {a: 1, b: 1}}\n", "")
	cases := map[string][]MemberResult{
		"缺成员":  {pass("a", nil)},
		"成员重复": {pass("a", nil), pass("a", nil)},
		"多成员":  {pass("a", nil), pass("b", nil), pass("c", nil)},
	}
	for name, members := range cases {
		if _, err := Step(cfg, engine.State{}, Input{BarIndex: 1, Members: members}); err == nil {
			t.Fatalf("%s应报错", name)
		}
	}
}

func testBarEnd(bar int) time.Time {
	return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(bar) * time.Hour)
}
