package factorexpr

import (
	"strings"
	"testing"
)

func mustCompile(t *testing.T, text string) *Factor {
	t.Helper()
	factor, err := Compile(text)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return factor
}

func TestParamDeclarationParsed(t *testing.T) {
	factor := mustCompile(t, `
params:
  window: {type: int, min: 5, max: 120, default: 20}
  k: {type: float, min: 0, max: 5, default: 2.0}
let:
  ma: ts_mean(close, window)
output: ma + k * ts_std(close, window)
`)
	if len(factor.Params) != 2 {
		t.Fatalf("params = %+v", factor.Params)
	}
	window, _ := factor.Param("window")
	if !window.IsInt() || window.Min != 5 || window.Max != 120 || window.Default != 20 {
		t.Fatalf("window = %+v", window)
	}
	k, _ := factor.Param("k")
	if k.IsInt() || k.Default != 2 {
		t.Fatalf("k = %+v", k)
	}
	// 回看按参数上限推导：ts_std(close, window) 在 window = 120 时需要 120 根。
	if factor.Lookback != 120 {
		t.Fatalf("lookback = %d, want 120", factor.Lookback)
	}
	if got := factor.Outputs[0].Root.Canonical(); got != "add(ts_mean(close,$window),mul($k,ts_std(close,$window)))" {
		t.Fatalf("canonical = %s", got)
	}
}

func TestParamDeclarationRejected(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"list value", "params: {window: [5, 20]}\noutput: ts_mean(close, window)\n", "{type, min, max, default}"},
		{"scalar value", "params: {window: 20}\noutput: ts_mean(close, window)\n", "{type, min, max, default}"},
		{"missing type", "params: {window: {min: 1, max: 5, default: 2}}\noutput: ts_mean(close, window)\n", "缺少 type"},
		{"missing default", "params: {window: {type: int, min: 1, max: 5}}\noutput: ts_mean(close, window)\n", "缺少 default"},
		{"bad type", "params: {window: {type: string, min: 1, max: 5, default: 2}}\noutput: close\n", "int 或 float"},
		{"unknown key", "params: {window: {type: int, min: 1, max: 5, default: 2, step: 1}}\noutput: close\n", "未知的键"},
		{"int with float bound", "params: {window: {type: int, min: 1, max: 5.5, default: 2}}\noutput: close\n", "必须是整数"},
		{"min above max", "params: {window: {type: int, min: 6, max: 5, default: 5}}\noutput: close\n", "min 不能大于 max"},
		{"default outside", "params: {window: {type: int, min: 1, max: 5, default: 9}}\noutput: close\n", "默认值"},
		{"template name", "params: {window: {type: int, min: 1, max: 5, default: 2}}\noutputs:\n  x_{window}: ts_mean(close, window)\n  y: close\n", "不合法"},
		{"float as window", "params: {w: {type: float, min: 1, max: 5, default: 2}}\noutput: ts_mean(close, w)\n", "整数类型"},
		{"range beyond window limit", "params: {w: {type: int, min: 1, max: 200000, default: 2}}\noutput: ts_mean(close, w)\n", "范围在"},
		{"std window below minimum", "params: {w: {type: int, min: 1, max: 20, default: 5}}\noutput: ts_std(close, w)\n", "范围在"},
		{"expression as window", "params: {w: {type: int, min: 1, max: 20, default: 5}}\noutput: ts_mean(close, w * 2)\n", "单个整数参数"},
		{"param only output", "params: {k: {type: float, min: 0, max: 5, default: 2}}\noutput: k * 2\n", "只由参数决定"},
		{"param as series", "params: {k: {type: float, min: 0, max: 5, default: 2}}\noutput: ts_mean(k, 5) + close\n", "不能是常量或参数"},
		{"quantile out of range", "params: {q: {type: float, min: 0, max: 2, default: 0.5}}\noutput: ts_quantile(close, 5, q)\n", "范围必须在"},
		{"lookback worst case", "params: {w: {type: int, min: 2, max: 100000, default: 20}}\noutput: ts_mean(ts_std(close, w), 20)\n", "最坏情况"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.text)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// sma 的截断长度取决于 α = m/n：最坏回看在 n 取上限、m 取下限时出现。
func TestParamLookbackWorstCaseForSMA(t *testing.T) {
	factor := mustCompile(t, `
params:
  n: {type: int, min: 10, max: 100, default: 20}
  m: {type: int, min: 1, max: 9, default: 2}
output: sma(close, n, m)
`)
	want := RecursiveSpan(SMAAlpha(100, 1))
	if factor.Lookback != want {
		t.Fatalf("lookback = %d, want %d", factor.Lookback, want)
	}
}

func TestCheckParams(t *testing.T) {
	factor := mustCompile(t, `
params:
  n: {type: int, min: 2, max: 100, default: 20}
  m: {type: int, min: 1, max: 99, default: 2}
output: sma(close, n, m)
`)
	if err := factor.CheckParams(map[string]float64{"n": 30, "m": 5}); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for name, values := range map[string]map[string]float64{
		"joint constraint": {"n": 10, "m": 10},
		"out of range":     {"n": 101, "m": 1},
		"not integer":      {"n": 10.5, "m": 1},
		"missing":          {"n": 10},
		"unknown":          {"n": 10, "m": 1, "x": 1},
	} {
		if err := factor.CheckParams(values); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
	// 默认值不满足联合约束时编译即拒绝。
	if _, err := Compile("params:\n  n: {type: int, min: 2, max: 100, default: 5}\n  m: {type: int, min: 1, max: 99, default: 5}\noutput: sma(close, n, m)\n"); err == nil ||
		!strings.Contains(err.Error(), "m 必须小于 n") {
		t.Fatalf("err = %v", err)
	}
}

// 参数声明进入规范文本（从而进入 definition_hash），改默认值、范围或类型都会改变它；参数名只是槽位名，同样进入。
func TestParamDeclarationInCanonicalText(t *testing.T) {
	base := "params: {w: {type: int, min: 2, max: 50, default: 20}}\noutput: ts_mean(close, w)\n"
	text := mustCompile(t, base).CanonicalText()
	if !strings.HasPrefix(text, "$w int [2, 50] = 20\n") {
		t.Fatalf("canonical = %q", text)
	}
	for _, changed := range []string{
		strings.Replace(base, "default: 20", "default: 21", 1),
		strings.Replace(base, "max: 50", "max: 60", 1),
	} {
		if mustCompile(t, changed).CanonicalText() == text {
			t.Fatalf("canonical text should change: %s", changed)
		}
	}
}

// 两个成员共用一个定义时，参数槽按成员区分；不含参数的子树在成员之间共享为一个节点。
func TestProgramParamSlotsPerMember(t *testing.T) {
	factor := mustCompile(t, `
params: {w: {type: int, min: 2, max: 50, default: 20}}
output: ts_mean(close * 2, w)
`)
	program, err := NewProgram([]ProgramInput{
		{Key: "set_a/fast", FactorID: "f", Factor: factor},
		{Key: "set_b/fast", FactorID: "f", Factor: factor},
	})
	if err != nil {
		t.Fatalf("program: %v", err)
	}
	if len(program.Params) != 2 || program.Params[0].Slot == program.Params[1].Slot {
		t.Fatalf("params = %+v", program.Params)
	}
	// close、2、mul(close,2) 共享；两个 ts_mean 各自一个。
	means := 0
	muls := 0
	for _, node := range program.Nodes {
		switch node.Op {
		case "ts_mean":
			means++
		case "mul":
			muls++
		}
	}
	if means != 2 || muls != 1 {
		t.Fatalf("means = %d, muls = %d", means, muls)
	}
	if _, err := NewProgram([]ProgramInput{{Key: "k", Factor: factor}, {Key: "k", Factor: factor}}); err == nil {
		t.Fatalf("duplicate key should fail")
	}
}

// 截面依赖节点按标的范围区分，纯时序节点在范围之间共享。
func TestProgramCrossNodesKeyedByUniverse(t *testing.T) {
	factor := mustCompile(t, "output: cs_rank(ts_mean(close, 5))\n")
	program, err := NewProgram([]ProgramInput{
		{Key: "a/r", Factor: factor, Universe: "a"},
		{Key: "b/r", Factor: factor, Universe: "b"},
	})
	if err != nil {
		t.Fatalf("program: %v", err)
	}
	counts := map[string]int{}
	for _, node := range program.Nodes {
		counts[node.Op]++
	}
	if counts["ts_mean"] != 1 || counts["cs_rank"] != 2 {
		t.Fatalf("counts = %v", counts)
	}
}
