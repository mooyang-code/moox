package factorexpr

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// outputSpec 把一条表达式放进只有一个输出的定义；用块标量，表达式里的引号与花括号不需要转义。
func outputSpec(expression string) string {
	return "output: |\n    " + expression + "\n"
}

func TestSyntaxWhitelistRejects(t *testing.T) {
	cases := []struct {
		name, expression, want string
	}{
		{"string", `close + "abc"`, "不允许使用字符串"},
		{"nil", `close + nil`, "不允许使用 nil"},
		{"predicate builtin", `sum(close)`, "不支持 sum"},
		{"map builtin", `map(close, # > 1)`, "不支持 map"},
		{"member access", `close.x`, "成员访问或下标"},
		{"index", `close[0]`, "成员访问或下标"},
		{"slice", `close[0:2]`, "切片"},
		{"optional chain", `close?.x`, "可选链"},
		{"array", `[1, 2]`, "数组或字典"},
		{"map literal", `{a: 1}`, "数组或字典"},
		{"in", `close in [1]`, "不允许使用 in"},
		{"range", `1..3`, "不允许使用范围"},
		{"coalesce", `close ?? 1`, "不允许使用 ??"},
		{"matches", `close matches "a"`, "matches"},
		{"let expression", `let x = close; x`, "let"},
		{"if expression", `if close > 1 { close } else { open }`, "语法错误"},
		{"unterminated", `ts_mean(close, 20`, "语法错误"},
		{"dangling operator", `close +`, "语法错误"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(outputSpec(tc.expression))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			var compileErr *Error
			if !errors.As(err, &compileErr) || compileErr.Path != "output" {
				t.Fatalf("err = %#v, want *Error at output", err)
			}
		})
	}
}

func TestTypeAndNameChecks(t *testing.T) {
	cases := []struct {
		name, expression, want string
	}{
		{"bool in arithmetic", `close + (open > 1)`, "需要数值，得到布尔序列"},
		{"number as condition", `where(close, 1, 0)`, "需要布尔值"},
		{"number in logic", `(close > 1) && open`, "需要布尔值"},
		{"not on number", `!close`, "需要布尔值"},
		{"unary plus on bool", `+(close > 1)`, "运算 + 需要数值"},
		{"function as identifier", `ts_mean + 1`, "是函数，需要写成 ts_mean(...)"},
		{"unknown function", `foo(close)`, "未知的函数 foo"},
		{"operator by name", `add(close, 1)`, "未知的函数 add"},
		{"too few arguments", `ts_mean(close)`, "需要 2 个参数，写了 1 个"},
		{"too many arguments", `pct_change(close, 1, 2)`, "需要 1～2 个参数"},
		{"float window", `ts_mean(close, 2.5)`, "整数常量"},
		{"zero window", `ts_mean(close, 0)`, "1～100000 的整数常量"},
		{"huge window", `ts_mean(close, 100001)`, "1～100000 的整数常量"},
		{"series window", `ts_mean(close, volume)`, "整数常量"},
		{"division gives float window", `ts_mean(close, 10 / 2)`, "整数常量"},
		{"constant series argument", `ts_mean(1, 20)`, "必须依赖输入列"},
		{"std window too small", `ts_std(close, 1)`, "2～100000"},
		{"skew window too small", `ts_skew(close, 2)`, "3～100000"},
		{"quantile out of range", `ts_quantile(close, 20, 1.5)`, "0～1 之间的常量"},
		{"quantile not constant", `ts_quantile(close, 20, volume)`, "0～1 之间的常量"},
		{"chip window too small", `chip_profit(open, high, low, close, volume, 19)`, "20～500 的整数常量"},
		{"chip window too large", `chip_profit(open, high, low, close, volume, 501)`, "20～500 的整数常量"},
		{"chip quantile out of range", `chip_cost(open, high, low, close, volume, 120, 1)`, "0.01～0.99 之间的常量"},
		{"chip constant series", `chip_cost(open, high, low, close, 1, 120, 0.5)`, "必须依赖输入列"},
		{"chip missing quantile", `chip_cost(open, high, low, close, volume, 120)`, "需要 7 个参数，写了 6 个"},
		{"sma m not less than n", `sma(close, 5, 5)`, "m 必须小于 n"},
		{"count needs bool series", `ts_count(close, 5)`, "需要布尔序列"},
		{"count constant bool", `ts_count(true, 5)`, "必须依赖输入列"},
		{"cross section on constant", `cs_rank(2) + close`, "必须依赖输入列"},
		{"reserved column", `data_time + 1`, "系统列"},
		{"division by zero constant", `close + 1 / 0`, "常量运算的结果不是有限数"},
		{"log of negative constant", `close + log(-1)`, "常量运算的结果不是有限数"},
		{"constant output", `1 + 2`, "不能是常量"},
		{"bool output", `close > 1`, "输出必须是数值"},
		{"bool constant output", `true`, "输出必须是数值"},
		{"huge integer literal", `close * 99999999999999999`, "超出范围"},
		{"lookback overflow", `ts_mean(ts_mean(close, 60000), 60000)`, "超过上限 100000"},
		{"recursive lookback overflow", `ema(close, 100000)`, "超过上限 100000"},
		{"ema explicit k out of range", `ema(close, 20, 100001)`, "0～100000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(outputSpec(tc.expression))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestLetAndParamResolution(t *testing.T) {
	cases := []struct {
		name, text, want string
	}{
		{"forward reference", "let:\n  a: b + 1\n  b: close\noutput: a\n", "之后（或它自身）定义的 let b"},
		{"self reference", "let:\n  a: a + 1\noutput: a\n", "之后（或它自身）定义的 let a"},
		{"let called as function", "let:\n  tp: close\noutput: tp(1)\n", "tp 是 let 中间量，不是函数"},
		{"param called as function", "params: {w: {type: int, min: 1, max: 9, default: 5}}\noutput: w(close)\n", "w 是参数，不是函数"},
		{"error inside let", "let:\n  m: ts_mean(close)\noutput: m\n", "let.m"},
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

func TestErrorPositions(t *testing.T) {
	_, err := Compile("output: close + foo(1)\n")
	var compileErr *Error
	if !errors.As(err, &compileErr) {
		t.Fatalf("err = %v", err)
	}
	if compileErr.Path != "output" || compileErr.DocLine != 1 || compileErr.Line != 1 || compileErr.Column != 9 {
		t.Fatalf("position = %+v", compileErr)
	}
	_, err = Compile("output: |\n    close\n    + ts_mean(close, 0)\n")
	if !errors.As(err, &compileErr) || compileErr.Line != 2 || compileErr.Column != 18 {
		t.Fatalf("multi-line position = %+v (%v)", compileErr, err)
	}
	_, err = Compile("output: close +\n")
	if !errors.As(err, &compileErr) || compileErr.Line != 1 || compileErr.Column == 0 {
		t.Fatalf("syntax error position = %+v (%v)", compileErr, err)
	}
	if compileErr.Unwrap() == nil {
		t.Fatalf("syntax error should keep the parser error")
	}
}

func TestExpressionNodeLimits(t *testing.T) {
	terms := make([]string, 300)
	for i := range terms {
		terms[i] = "close"
	}
	_, err := Compile(outputSpec(strings.Join(terms, " + ")))
	if err == nil || !strings.Contains(err.Error(), "超过 512 个语法节点") {
		t.Fatalf("err = %v", err)
	}
	// let 互相引用会让展开后的树指数增长：按展开后的节点数限制。
	text := "let:\n  a0: close + open\n"
	for i := 1; i <= 12; i++ {
		text += "  a" + strconv.Itoa(i) + ": a" + strconv.Itoa(i-1) + " + a" + strconv.Itoa(i-1) + "\n"
	}
	text += "output: a12\n"
	_, err = Compile(text)
	if err == nil || !strings.Contains(err.Error(), "超过 4096 个") {
		t.Fatalf("err = %v", err)
	}
}

func TestConstantFolding(t *testing.T) {
	cases := []struct {
		expression, canonical string
	}{
		{`ts_mean(close, 5 * 4)`, "ts_mean(close,20)"},
		{`where(true, close, open)`, "close"},
		{`where(1 > 2, close, open)`, "open"},
		{`close * -1`, "mul(close,-1)"},
		{`close * (2 + 0.5)`, "mul(close,2.5)"},
		{`close / 1.0`, "div(close,1)"},
		{`pct_change(close)`, "pct_change(close,1)"},
		{`max(3, 4) + close`, "add(4,close)"},
		{`where(close > open, 1, 0)`, "where(gt(close,open),1,0)"},
		{`close > 1 ? close : 0`, "where(gt(close,1),close,0)"},
		{`where((close > 1 and open < 2) || not (high > 3), 1, 0)`, "where(or(and(gt(close,1),lt(open,2)),not(gt(high,3))),1,0)"},
		{`close ** 2 + close ^ 2`, "add(pow(close,2),pow(close,2))"},
		{`ts_quantile(close, 20, 0.9)`, "ts_quantile(close,20,0.9)"},
		{`sma(close, 20, 1)`, "sma(close,20,1)"},
	}
	for _, tc := range cases {
		factor, err := Compile(outputSpec(tc.expression))
		if err != nil {
			t.Fatalf("%s: %v", tc.expression, err)
		}
		if got := factor.Outputs[0].Root.Canonical(); got != tc.canonical {
			t.Errorf("%s: canonical = %s, want %s", tc.expression, got, tc.canonical)
		}
	}
	// 参数参与的逐元素运算不折叠（取值每期才知道），常量部分照常折叠。
	factor := mustCompile(t, "params: {k: {type: float, min: 0, max: 5, default: 2}}\noutput: close * (k + 1 * 2)\n")
	if got := factor.Outputs[0].Root.Canonical(); got != "mul(close,add($k,2))" {
		t.Fatalf("canonical = %s", got)
	}
}

func TestFactorTypeDerivation(t *testing.T) {
	if factor := mustCompile(t, outputSpec(`ts_mean(close, 5)`)); factor.Type != FactorTypeTimeSeries {
		t.Fatalf("type = %s", factor.Type)
	}
	factor := mustCompile(t, "outputs:\n  a: ts_mean(close, 5)\n  b: cs_rank(ts_mean(volume, 20))\n")
	if factor.Type != FactorTypeCrossSection {
		t.Fatalf("type = %s", factor.Type)
	}
	if !factor.Outputs[1].Root.CrossSection() || factor.Outputs[0].Root.CrossSection() {
		t.Fatalf("cross flags wrong")
	}
	if factor.Lookback != 20 {
		t.Fatalf("lookback = %d", factor.Lookback)
	}
}

func TestUnusedLetIsAllowedButChecked(t *testing.T) {
	mustCompile(t, "let:\n  unused: ts_mean(open, 3)\noutput: close\n")
	if _, err := Compile("let:\n  unused: ts_mean(open)\noutput: close\n"); err == nil {
		t.Fatal("an invalid unused let should still be rejected")
	}
}

// nan 是空值常量：可以写在数值位置（where 的分支、fill_nan 的取值、运算），不能写在窗口、位移、分位数等编译期位置，
// 也不能用作参数名或 let 名；不含 nan 字面量的常量运算算出 NaN 仍然报错。
func TestNaNLiteral(t *testing.T) {
	valid := []struct {
		expression, canonical string
	}{
		{`where(close > open, close, nan)`, "where(gt(close,open),close,nan)"},
		{`fill_nan(close, nan)`, "fill_nan(close,nan)"},
		{`close + nan`, "add(close,nan)"},
		{`close + (nan + 1)`, "add(close,nan)"},
		{`cs_rank(where(close > 0, close, nan))`, "cs_rank(where(gt(close,0),close,nan))"},
		{`where(nan > 0, close, open)`, "where(gt(nan,0),close,open)"},
		{`where(is_nan(nan), close, open)`, "close"},
		{`ts_mean(close, ceil(7.5))`, "ts_mean(close,8)"},
		{`ts_mean(close, round(2.5))`, "ts_mean(close,3)"},
	}
	for _, tc := range valid {
		factor, err := Compile(outputSpec(tc.expression))
		if err != nil {
			t.Fatalf("%s: %v", tc.expression, err)
		}
		if got := factor.Outputs[0].Root.Canonical(); got != tc.canonical {
			t.Errorf("%s: canonical = %s, want %s", tc.expression, got, tc.canonical)
		}
	}
	invalid := []struct {
		name, text, want string
	}{
		{"window", outputSpec(`ts_mean(close, nan)`), "参数 n 不能是 nan"},
		{"folded window", outputSpec(`ts_mean(close, nan + 1)`), "参数 n 不能是 nan"},
		{"shift", outputSpec(`delay(close, nan)`), "参数 k 不能是 nan"},
		{"quantile", outputSpec(`ts_quantile(close, 20, nan)`), "参数 q 不能是 nan"},
		{"constant output", outputSpec(`nan`), "不能是常量"},
		{"zero over zero", outputSpec(`close + 0 / 0`), "常量运算的结果不是有限数"},
		{"constant condition output", outputSpec(`where(nan > 0, 1, 0)`), "不能只由常量决定"},
		{"param name", "params: {nan: {type: int, min: 1, max: 9, default: 5}}\noutput: close\n", "保留字"},
		{"let name", "let:\n  nan: close\noutput: close\n", "保留字"},
		{"called as function", outputSpec(`nan(close)`), "未知的函数 nan"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.text)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
	// 不同写法得到同一个 NaN 常量：规范文本相同（定义哈希相同）。
	a := mustCompile(t, outputSpec(`close + nan`)).CanonicalText()
	b := mustCompile(t, outputSpec(`close + (nan * 2)`)).CanonicalText()
	if a != b {
		t.Fatalf("canonical differs: %q vs %q", a, b)
	}
}

// 聚合广播类截面函数的编译期检查：cs_quantile 的 q 只收 0～1 的字面量，cs_count 只收布尔序列，cs_ref 的标的只收字符串字面量。
func TestAggregateChecks(t *testing.T) {
	valid := []struct {
		expression, canonical string
	}{
		{`cs_quantile(close, 0.9)`, "cs_quantile(close,0.9)"},
		{`cs_count(close > open)`, "cs_count(gt(close,open))"},
		{`close - cs_median(close)`, "sub(close,cs_median(close))"},
		{`cs_ref(close, "BTC-USDT")`, `cs_ref(close,"BTC-USDT")`},
		{`cs_ref(close, "600519.XSHG")`, `cs_ref(close,"600519.XSHG")`},
		{`c and cs_count(c) <= ceil(cs_count(close > 0) * 0.04)`, ""},
	}
	for _, tc := range valid {
		text := outputSpec(tc.expression)
		if strings.HasPrefix(tc.expression, "c and") {
			text = "let:\n  c: close > open\noutput: where(" + tc.expression + ", 1, 0)\n"
		}
		factor, err := Compile(text)
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
	invalid := []struct {
		name, text, want string
	}{
		{"quantile param", "params: {q: {type: float, min: 0, max: 1, default: 0.5}}\noutput: cs_quantile(close, q)\n", "只能写字面量，不能使用参数 q"},
		{"quantile out of range", outputSpec(`cs_quantile(close, 1.5)`), "0～1 之间的字面量"},
		{"quantile nan", outputSpec(`cs_quantile(close, nan)`), "不能是 nan"},
		{"quantile series", outputSpec(`cs_quantile(close, volume)`), "0～1 之间的字面量"},
		{"count number", outputSpec(`cs_count(close)`), "需要布尔序列"},
		{"count constant", outputSpec(`cs_count(true) + close`), "必须依赖输入列"},
		{"mean constant", outputSpec(`cs_mean(1) + close`), "必须依赖输入列"},
		{"mean bool", outputSpec(`cs_mean(close > 1)`), "需要数值序列"},
		{"ref identifier", outputSpec(`cs_ref(close, btc)`), "必须是标的 ID 的字符串字面量"},
		{"ref number", outputSpec(`cs_ref(close, 1)`), "必须是标的 ID 的字符串字面量"},
		{"ref empty", outputSpec(`cs_ref(close, "")`), "标的 ID 不能为空"},
		{"ref bad character", outputSpec(`cs_ref(close, "BTC USDT")`), "不允许的字符"},
		{"ref too long", outputSpec(`cs_ref(close, "` + strings.Repeat("A", MaxSubjectLiteralRunes+1) + `")`), "超过 64 个字符"},
		{"ref missing subject", outputSpec(`cs_ref(close)`), "需要 2 个参数"},
		{"string elsewhere", outputSpec(`cs_mean("BTC-USDT")`), "只有 cs_ref 的标的参数可以写字符串"},
		{"string in let", "let:\n  s: '\"BTC-USDT\"'\noutput: cs_ref(close, s)\n", "不允许使用字符串"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.text)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
	// 参考标的进入规范文本（定义哈希）与 RefSubjects；不同参考标的的两个 cs_ref 是不同的节点。
	factor := mustCompile(t, "outputs:\n  a: cs_ref(close, \"ETH-USDT\")\n  b: cs_ref(close, \"BTC-USDT\") + cs_ref(close, \"ETH-USDT\")\n")
	if got := strings.Join(factor.RefSubjects, ","); got != "BTC-USDT,ETH-USDT" {
		t.Fatalf("RefSubjects = %s", got)
	}
	if !strings.Contains(factor.CanonicalText(), `add(cs_ref(close,"BTC-USDT"),cs_ref(close,"ETH-USDT"))`) {
		t.Fatalf("canonical = %s", factor.CanonicalText())
	}
	program, err := NewProgram([]ProgramInput{{FactorID: "f", Factor: factor}})
	if err != nil {
		t.Fatal(err)
	}
	refs := 0
	for _, node := range program.Nodes {
		if node.Op == "cs_ref" {
			refs++
		}
	}
	if refs != 2 {
		t.Fatalf("program has %d cs_ref nodes, want 2 (same subject shared, different subjects kept apart)", refs)
	}
	if mustCompile(t, outputSpec(`ts_mean(close, 5)`)).RefSubjects != nil {
		t.Fatal("RefSubjects should be empty without cs_ref")
	}
}

// 事件记忆的回看是 L(x)+n−1（多个序列参数取最大的 L）；c 只收布尔序列，n 只收窗口常量。
func TestEventMemoryChecks(t *testing.T) {
	for expression, lookback := range map[string]int{
		`ts_last_valid(close, 10)`:                                 10,
		`ts_bars_since(close > open, 10)`:                          10,
		`ts_value_when(close > open, delay(close, 3), 10)`:         13,
		`ts_bars_since(ts_mean(close, 5) > open, 1)`:               5,
		`fill_nan(ts_last_valid(where(close > 1, 1, nan), 20), 0)`: 20,
	} {
		if got := mustCompile(t, outputSpec(expression)).Lookback; got != lookback {
			t.Errorf("%s: lookback = %d, want %d", expression, got, lookback)
		}
	}
	for expression, want := range map[string]string{
		`ts_bars_since(close, 10)`:            "需要布尔序列",
		`ts_value_when(close, close, 10)`:     "需要布尔序列",
		`ts_value_when(close > open, 1, 10)`:  "必须依赖输入列",
		`ts_last_valid(close, 0)`:             "1～100000 的整数常量",
		`ts_last_valid(close, nan)`:           "不能是 nan",
		`ts_bars_since(close > open, 100001)`: "1～100000 的整数常量",
	} {
		if _, err := Compile(outputSpec(expression)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want containing %q", expression, err, want)
		}
	}
}

// 第 1 期新函数的非法写法：宽松窗口的 m（大于 n、低于严格版的下限、用参数时的联合约束）、类型、q 的范围；取整与聚合广播函数的参数类型。
func TestNewFunctionsRejectInvalidArguments(t *testing.T) {
	cases := []struct {
		text, want string
	}{
		{outputSpec(`ts_sum_relaxed(close, 5, 6)`), "m 不能大于 n"},
		{outputSpec(`ts_median_relaxed(close, 5, 6)`), "m 不能大于 n"},
		{outputSpec(`ts_quantile_relaxed(close, 5, 0.5, 6)`), "m 不能大于 n"},
		{outputSpec(`ts_corr_relaxed(close, volume, 5, 6)`), "m 不能大于 n"},
		{outputSpec(`ts_var_relaxed(close, 20, 1)`), "2～100000"},
		{outputSpec(`ts_zscore_relaxed(close, 20, 1)`), "2～100000"},
		{outputSpec(`ts_skew_relaxed(close, 20, 2)`), "3～100000"},
		{outputSpec(`ts_kurt_relaxed(close, 20, 3)`), "4～100000"},
		{outputSpec(`ts_corr_relaxed(close, volume, 20, 1)`), "2～100000"},
		{outputSpec(`ts_cov_relaxed(close, volume, 20, 1)`), "2～100000"},
		{outputSpec(`ts_rank_relaxed(close, 20, 0)`), "1～100000"},
		{outputSpec(`ts_count_relaxed(close, 5, 1)`), "需要布尔序列"},
		{outputSpec(`ts_count_relaxed(true, 5, 1) + close`), "必须依赖输入列"},
		{outputSpec(`ts_cov_relaxed(close, 1, 20, 3)`), "必须依赖输入列"},
		{outputSpec(`ts_argmax_relaxed(close, 20)`), "需要 3 个参数"},
		{outputSpec(`ts_quantile_relaxed(close, 20, 1.5, 3)`), "0～1 之间的常量"},
		{outputSpec(`ts_quantile_relaxed(close, 20, nan, 3)`), "不能是 nan"},
		{outputSpec(`ts_product_relaxed(close, 20, nan)`), "不能是 nan"},
		{"params: {m: {type: int, min: 1, max: 30, default: 25}}\noutput: ts_sum_relaxed(close, 20, m)\n", "m 不能大于 n"},
		{outputSpec(`floor(close > 0)`), "需要数值"},
		{outputSpec(`ceil(true) + close`), "需要数值"},
		{outputSpec(`round()`), "需要 1 个参数"},
	}
	for _, aggregate := range []string{"cs_sum", "cs_std", "cs_median", "cs_min", "cs_max", "cs_mean"} {
		cases = append(cases,
			struct{ text, want string }{outputSpec(aggregate + `(2) + close`), "必须依赖输入列"},
			struct{ text, want string }{outputSpec(aggregate + `(close > open)`), "需要数值序列"},
			struct{ text, want string }{outputSpec(aggregate + `(close, 2)`), "需要 1 个参数"})
	}
	for _, tc := range cases {
		_, err := Compile(tc.text)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want containing %q", tc.text, err, tc.want)
		}
	}
	// m 用参数且声明范围的上限超过 n：编译时按默认值检查；默认值合法时，提交的取值仍按联合约束检查。
	factor := mustCompile(t, "params: {m: {type: int, min: 1, max: 30, default: 5}}\noutput: ts_sum_relaxed(close, 20, m)\n")
	if err := factor.CheckParams(map[string]float64{"m": 25}); err == nil || !strings.Contains(err.Error(), "m 不能大于 n") {
		t.Fatalf("CheckParams(m=25) = %v", err)
	}
	if err := factor.CheckParams(map[string]float64{"m": 20}); err != nil {
		t.Fatalf("CheckParams(m=20) = %v", err)
	}
}
