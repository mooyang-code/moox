package factorexpr

import (
	"strings"
	"testing"
)

// 多输入、分组截面与截面回归的编译期检查：分组键必须是数值序列；变参按实际个数逐个核对，个数有上下限。
func TestGroupAndResidualChecks(t *testing.T) {
	valid := []struct {
		expression, canonical string
	}{
		{`cs_corr(close, volume)`, "cs_corr(close,volume)"},
		{`gs_rank(close, industry)`, "gs_rank(close,industry)"},
		{`gs_count(close > open, industry)`, "gs_count(gt(close,open),industry)"},
		{`gs_wmean(close, mcap, industry)`, "gs_wmean(close,mcap,industry)"},
		{`gx_rank(gs_mean(close, industry), industry)`, "gx_rank(gs_mean(close,industry),industry)"},
		{`cs_resid(close, a)`, "cs_resid(close,a)"},
		{`cs_resid(close, a, b, c, d, e, f, g, h)`, "cs_resid(close,a,b,c,d,e,f,g,h)"},
		{`gs_resid(close, industry)`, "gs_resid(close,industry)"},
		{`gs_resid(close, industry, a, b, c, d, e, f, g, h)`, "gs_resid(close,industry,a,b,c,d,e,f,g,h)"},
		{`-cs_resid(log(mcap), a, b) / log(mcap)`, ""},
	}
	for _, tc := range valid {
		factor, err := Compile(outputSpec(tc.expression))
		if err != nil {
			t.Fatalf("%s: %v", tc.expression, err)
		}
		if factor.Type != FactorTypeCrossSection {
			t.Errorf("%s: type = %s", tc.expression, factor.Type)
		}
		if tc.canonical != "" {
			if got := factor.Outputs[0].Root.Canonical(); got != tc.canonical {
				t.Errorf("%s: canonical = %s, want %s", tc.expression, got, tc.canonical)
			}
		}
	}
	// 自变量的个数与顺序都进入规范文本：多写或换序是不同的定义。
	two, swapped := mustCompile(t, outputSpec(`cs_resid(close, a, b)`)), mustCompile(t, outputSpec(`cs_resid(close, b, a)`))
	if two.CanonicalText() == swapped.CanonicalText() {
		t.Fatal("regressor order must change the canonical text")
	}
	invalid := []struct {
		name, text, want string
	}{
		{"corr one argument", outputSpec(`cs_corr(close)`), "需要 2 个参数，写了 1 个"},
		{"corr constant", outputSpec(`cs_corr(close, 1)`), "必须依赖输入列"},
		{"group bool", outputSpec(`gs_rank(close, close > open)`), "需要数值序列，得到布尔序列"},
		{"group constant", outputSpec(`gs_mean(close, 1)`), "函数 gs_mean 的参数 g 必须依赖输入列"},
		{"group param", "params: {k: {type: int, min: 1, max: 9, default: 2}}\noutput: gs_mean(close, k)\n", "必须依赖输入列"},
		{"group missing", outputSpec(`gs_rank(close)`), "需要 2 个参数，写了 1 个"},
		{"group count number", outputSpec(`gs_count(close, industry)`), "需要布尔序列"},
		{"wmean missing group", outputSpec(`gs_wmean(close, mcap)`), "需要 3 个参数，写了 2 个"},
		{"resid no regressor", outputSpec(`cs_resid(close)`), "函数 cs_resid 需要 2～9 个参数，写了 1 个"},
		{"resid too many", outputSpec(`cs_resid(close, a, b, c, d, e, f, g, h, i)`), "函数 cs_resid 需要 2～9 个参数，写了 10 个"},
		{"group resid too many", outputSpec(`gs_resid(close, industry, a, b, c, d, e, f, g, h, i)`), "函数 gs_resid 需要 2～10 个参数，写了 11 个"},
		{"group resid no group", outputSpec(`gs_resid(close)`), "函数 gs_resid 需要 2～10 个参数，写了 1 个"},
		{"resid constant regressor", outputSpec(`cs_resid(close, a, 2)`), "函数 cs_resid 的参数 x2 必须依赖输入列"},
		{"resid bool regressor", outputSpec(`gs_resid(close, industry, a, close > open)`), "参数 x2 需要数值序列，得到布尔序列"},
		{"resid string", outputSpec(`cs_resid(close, "BTC-USDT")`), "只有 cs_ref 的标的参数可以写字符串"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.text)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// cross_section_history 写在顶层：不写为 current；写 observed 不改变规范文本（哈希标记由定义哈希另行追加）。
func TestCrossSectionHistorySpec(t *testing.T) {
	plain := mustCompile(t, "output: ts_mean(cs_rank(close), 5)\n")
	observed := mustCompile(t, "cross_section_history: observed\noutput: ts_mean(cs_rank(close), 5)\n")
	current := mustCompile(t, "cross_section_history: current\noutput: ts_mean(cs_rank(close), 5)\n")
	if plain.ObservedHistory() || current.ObservedHistory() || !observed.ObservedHistory() {
		t.Fatalf("observed: plain %v current %v observed %v", plain.ObservedHistory(), current.ObservedHistory(), observed.ObservedHistory())
	}
	if plain.Spec.CrossSectionHistory != "" || current.Spec.CrossSectionHistory != CrossSectionCurrent {
		t.Fatalf("parsed values %q %q", plain.Spec.CrossSectionHistory, current.Spec.CrossSectionHistory)
	}
	if plain.CanonicalText() != observed.CanonicalText() {
		t.Fatal("cross_section_history must not enter the canonical text")
	}
	for text, want := range map[string]string{
		"cross_section_history: everything\noutput: cs_rank(close)\n":  "只能是 current 或 observed",
		"cross_section_history: {a: 1}\noutput: cs_rank(close)\n":      "必须是 current 或 observed",
		"cross_section_history: observed\noutput: ts_mean(close, 5)\n": "只对截面因子有意义",
		"history: observed\noutput: cs_rank(close)\n":                  "cross_section_history",
	} {
		if _, err := Compile(text); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want containing %q", text, err, want)
		}
	}
}

// 变参的签名：必填时写成 x1[, x2 … x8]，可选时紧跟前一个参数写成 [, x1 … x8]；参数说明只列一次并标出最多次数。
func TestVariadicSignature(t *testing.T) {
	signatures := map[string]string{}
	variadic := map[string]int{}
	for _, doc := range FunctionDocs() {
		signatures[doc.Name] = doc.Signature
		for _, arg := range doc.Args {
			if arg.Variadic > 0 {
				variadic[doc.Name] = arg.Variadic
			}
		}
	}
	if got := signatures["cs_resid"]; got != "cs_resid(y, x1[, x2 … x8])" {
		t.Errorf("cs_resid signature = %s", got)
	}
	if got := signatures["gs_resid"]; got != "gs_resid(y, g[, x1 … x8])" {
		t.Errorf("gs_resid signature = %s", got)
	}
	if got := signatures["gs_wmean"]; got != "gs_wmean(x, w, g)" {
		t.Errorf("gs_wmean signature = %s", got)
	}
	if variadic["cs_resid"] != 8 || variadic["gs_resid"] != 8 || len(variadic) != 2 {
		t.Errorf("variadic = %v", variadic)
	}
}
