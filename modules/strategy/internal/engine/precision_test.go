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
