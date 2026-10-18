package engine

import (
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// 精度规范：目标权重向零截断到 4 位小数，现货权重之和不会因取整超过 1；分数保留 6 位有效数字；换手四舍五入到 4 位。
func TestPrecisionRules(t *testing.T) {
	program := compile(t, `name: precision
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 3}
    weight: {total: 1}
portfolio:
  max_missing: 1
`, "m")
	frame := frameOf(program, map[string]Row{"A": values("m", 0.123456789), "B": values("m", 0.00001234567891), "C": values("m", -0.5)})
	decision := evaluate(t, program, frame, State{})
	assertOK(t, decision)
	// 三个标的等权：1/3 截断为 0.3333，零头 0.0001 留作现金。
	assertWeights(t, decision, map[string]string{"A": "0.3333", "B": "0.3333", "C": "0.3333"})
	assertDecimal(t, "cash", decision.Summary.Cash, "0.0001")
	assertDecimal(t, "net", decision.Summary.Net, "0.9999")
	// 换手 = 0.9999 / 2 = 0.49995，四舍五入到 4 位为 0.5。
	assertDecimal(t, "turnover", decision.Summary.Turnover, "0.5")
	if item := findItem(t, decision, "r", "A"); item.Score != "0.123457" {
		t.Fatalf("分数应保留 6 位有效数字：%q", item.Score)
	}
	if item := findItem(t, decision, "r", "B"); item.Score != "1.23457e-05" {
		t.Fatalf("小量级分数不能被取整成 0：%q", item.Score)
	}
}

func TestDecimalTruncateAndRound(t *testing.T) {
	for _, tc := range []struct{ in, truncated, rounded string }{
		{"0.33339999", "0.3333", "0.3334"},
		{"-0.33339999", "-0.3333", "-0.3334"},
		{"0.00005", "0", "0.0001"},
		{"1", "1", "1"},
		{"0.99999", "0.9999", "1"},
	} {
		value := quant.Must(tc.in)
		if got := value.TruncateTo(4).String(); got != tc.truncated {
			t.Errorf("%s 截断：got %s want %s", tc.in, got, tc.truncated)
		}
		if got := value.RoundTo(4).String(); got != tc.rounded {
			t.Errorf("%s 取整：got %s want %s", tc.in, got, tc.rounded)
		}
	}
}

// 合法的高精度配置值不能被取整进到网格点之外：total = leverage = 0.9999999999999 时，权重截断为 0.9999，整期不应因 gross 超杠杆被跳过；
// max_weight = 0.4999999999999 裁剪后不能变成 0.5。
func TestPrecisionNeverExceedsConfiguredLimits(t *testing.T) {
	leverage := compile(t, `name: tight
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.9999999999999}
portfolio:
  leverage: 0.9999999999999
  max_missing: 1
`, "m")
	decision := evaluate(t, leverage, frameOf(leverage, map[string]Row{"A": values("m", 1)}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "0.9999"})

	capped := compile(t, `name: capped
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_weight: 0.4999999999999
  max_missing: 1
`, "m")
	decision = evaluate(t, capped, frameOf(capped, map[string]Row{"A": values("m", 1)}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "0.4999"})
}

// signal 的解释明细与目标、Allocated 使用同一精度，明细可以复算已分配金额。
func TestSignalItemsFollowWeightPrecision(t *testing.T) {
	program := compile(t, `name: sig_precision
rules:
  - id: s
    type: signal
    pool: [A, B, C]
    entry: "m > 0"
    exit: "m < 0"
    weight: {total: 1}
portfolio:
  max_missing: 1
`, "m")
	decision := evaluate(t, program, frameOf(program, map[string]Row{"A": values("m", 1), "B": values("m", 1), "C": values("m", 1)}), State{})
	assertOK(t, decision)
	total := quant.Zero()
	for _, id := range []string{"A", "B", "C"} {
		item := findItem(t, decision, "s", id)
		if item.Weight != "0.3333" {
			t.Fatalf("%s 的明细权重应为 0.3333：%q", id, item.Weight)
		}
		total = total.Add(quant.Must(item.Weight))
	}
	assertDecimal(t, "allocated", decision.Summary.Rules["s"].Allocated, total.String())
}

// 配置值离网格点比噪声容忍量还近（18 位小数）时，量化退回精确截断，仍然不突破上限；做空同理。
func TestPrecisionExactLimitsFallBackToExactTruncation(t *testing.T) {
	leverage := compile(t, `name: tight18
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.999999999999999999}
portfolio:
  leverage: 0.999999999999999999
  max_missing: 1
`, "m")
	decision := evaluate(t, leverage, frameOf(leverage, map[string]Row{"A": values("m", 1)}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "0.9999"})

	for _, tc := range []struct{ sideLine, want string }{{"", "0.4999"}, {"    side: short\n", "-0.4999"}} {
		program := compile(t, `name: maxw18
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 1}
`+tc.sideLine+`portfolio:
  max_weight: 0.499999999999999999
  leverage: 2
  max_missing: 1
`, "m")
		decision := evaluate(t, program, frameOf(program, map[string]Row{"A": values("m", 1)}), State{})
		assertOK(t, decision)
		assertWeights(t, decision, map[string]string{"A": tc.want})
	}
}

// 大集合的 rank + cap 再分配用精确运算：200 个标的全部选中、cap 恰好等于平均份额时，每个都应是 0.005，不会被截低一格。
func TestRankCapLargeSetKeepsExactShares(t *testing.T) {
	program := compile(t, `name: big_cap
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 200}
    weight: {total: 1, method: rank, cap: 0.005}
portfolio:
  max_missing: 1
`, "m")
	rows := map[string]Row{}
	for i, id := range numberedIDs("ALT", 200) {
		rows[id] = values("m", i)
	}
	decision := evaluate(t, program, frameOf(program, rows), State{})
	assertOK(t, decision)
	for _, target := range decision.Targets {
		if target.Weight.String() != "0.005" {
			t.Fatalf("%s 应为 0.005：%s", target.InstrumentID, target.Weight.String())
		}
	}
	if len(decision.Targets) != 200 {
		t.Fatalf("应有 200 个目标：%d", len(decision.Targets))
	}
	assertDecimal(t, "cash", decision.Summary.Cash, "0")
}

// max_weight 裁剪并截断后为零的标的不再出现在目标里：空目标才能让下游直接转现金，不为它读报价。
func TestMaxWeightClippedToZeroDropsTarget(t *testing.T) {
	program := compile(t, `name: tiny_max
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_weight: 0.00009
  max_missing: 1
`, "m")
	decision := evaluate(t, program, frameOf(program, map[string]Row{"A": values("m", 1)}), State{Targets: map[string]string{"C": "0.5"}})
	assertOK(t, decision)
	if len(decision.Targets) != 0 {
		t.Fatalf("裁剪为零的标的不应留在目标里：%+v", decision.Targets)
	}
	assertDecimal(t, "cash", decision.Summary.Cash, "1")
}

// 规则预算与 cap 在输出网格之外（18 位小数）时整体精确截断，权重不会被噪声容忍抬过规则的 total 或 cap；
// 网格内的配置仍容忍噪声。
func TestPrecisionRespectsRuleBudgetAndCapOffGrid(t *testing.T) {
	budget := compile(t, `name: budget18
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.499999999999999999}
portfolio:
  max_missing: 1
`, "m")
	decision := evaluate(t, budget, frameOf(budget, map[string]Row{"A": values("m", 1)}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "0.4999"})
	assertDecimal(t, "allocated", decision.Summary.Rules["r"].Allocated, "0.4999")

	for _, tc := range []struct{ name, sideLine, holding string }{
		{"rank", "", ""},
		{"short", "    side: short\n", ""},
		{"holding", "", "    holding: {bars: 10, offsets: [0]}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			program := compile(t, `name: cap18
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 3}
    weight: {total: 0.9, method: rank, cap: 0.399999999999999999}
`+tc.sideLine+tc.holding+`portfolio:
  leverage: 2
  max_missing: 1
`, "m")
			frame := frameOf(program, map[string]Row{"A": values("m", 3), "B": values("m", 2), "C": values("m", 1)})
			frame.BarIndex = 0
			decision := evaluate(t, program, frame, State{})
			assertOK(t, decision)
			limit := quant.Must("0.3999")
			for _, target := range decision.Targets {
				if abs(target.Weight).Cmp(limit) > 0 {
					t.Fatalf("%s 的权重 %s 超过 cap 0.399999999999999999", target.InstrumentID, target.Weight.String())
				}
			}
			for _, item := range decision.Items {
				if item.Weight != "" && abs(quant.Must(item.Weight)).Cmp(limit) > 0 {
					t.Fatalf("明细 %s 的权重 %s 超过 cap", item.InstrumentID, item.Weight)
				}
			}
		})
	}
}

func TestQuantizationToleranceRequiresEveryLimitOnGrid(t *testing.T) {
	onGrid := compile(t, `name: grid
rules:
  - {id: a, type: rank, score: m, select: {top: 3}, weight: {total: 0.5, cap: 0.2}}
portfolio: {max_weight: 0.3, leverage: 1}
`, "m")
	if got := quantizationTolerance(onGrid); got.Cmp(noise) != 0 {
		t.Fatalf("网格内的配置应容忍噪声：%s", got.String())
	}
	for name, yaml := range map[string]string{
		"total":      "  - {id: a, type: rank, score: m, select: {top: 3}, weight: {total: 0.49999, cap: 0.2}}\nportfolio: {leverage: 1}\n",
		"cap":        "  - {id: a, type: rank, score: m, select: {top: 3}, weight: {total: 0.5, cap: 0.20001}}\nportfolio: {leverage: 1}\n",
		"max_weight": "  - {id: a, type: rank, score: m, select: {top: 3}, weight: 0.5}\nportfolio: {max_weight: 0.30001}\n",
		"leverage":   "  - {id: a, type: rank, score: m, select: {top: 3}, weight: 0.5}\nportfolio: {leverage: 1.00001}\n",
	} {
		program := compile(t, "name: off\nrules:\n"+yaml, "m")
		if got := quantizationTolerance(program); !got.IsZero() {
			t.Errorf("%s 在网格之外应不容忍噪声：%s", name, got.String())
		}
	}
}
