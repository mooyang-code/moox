package factorexpr

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// outputSpec 把一条表达式放进只有一个输出的定义；用块标量，表达式里的引号与花括号不需要转义。
func outputSpec(expression string) string {
	return "outputs:\n  o: |\n    " + expression + "\n"
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
			if !errors.As(err, &compileErr) || compileErr.Path != "outputs.o" {
				t.Fatalf("err = %#v, want *Error at outputs.o", err)
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
		{"zero window", `ts_mean(close, 0)`, "1～5000 的整数常量"},
		{"huge window", `ts_mean(close, 6000)`, "1～5000 的整数常量"},
		{"series window", `ts_mean(close, volume)`, "整数常量"},
		{"division gives float window", `ts_mean(close, 10 / 2)`, "整数常量"},
		{"constant series argument", `ts_mean(1, 20)`, "必须依赖输入列"},
		{"std window too small", `ts_std(close, 1)`, "2～5000"},
		{"skew window too small", `ts_skew(close, 2)`, "3～5000"},
		{"quantile out of range", `ts_quantile(close, 20, 1.5)`, "0～1 之间的常量"},
		{"quantile not constant", `ts_quantile(close, 20, volume)`, "0～1 之间的常量"},
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
		{"lookback overflow", `ts_mean(ts_mean(close, 3000), 3000)`, "超过上限 5000"},
		{"recursive lookback overflow", `ema(close, 5000)`, "超过上限 5000"},
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
		{"forward reference", "let:\n  a: b + 1\n  b: close\noutputs: {o: a}\n", "之后（或它自身）定义的 let b"},
		{"self reference", "let:\n  a: a + 1\noutputs: {o: a}\n", "之后（或它自身）定义的 let a"},
		{"let called as function", "let:\n  tp: close\noutputs: {o: tp(1)}\n", "tp 是 let 中间量，不是函数"},
		{"param called as function", "params: {w: 5}\noutputs: {o: w(close)}\n", "w 是参数，不是函数"},
		{"error inside let", "let:\n  m: ts_mean(close)\noutputs: {o: m}\n", "let.m"},
		{"list value annotated", "params: {window: [1, 20]}\noutputs:\n  s_{window}: ts_std(close, window)\n", "参数 window=1"},
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
	_, err := Compile("outputs:\n  o: close + foo(1)\n")
	var compileErr *Error
	if !errors.As(err, &compileErr) {
		t.Fatalf("err = %v", err)
	}
	if compileErr.Path != "outputs.o" || compileErr.DocLine != 2 || compileErr.Line != 1 || compileErr.Column != 9 {
		t.Fatalf("position = %+v", compileErr)
	}
	_, err = Compile("outputs:\n  o: |\n    close\n    + ts_mean(close, 0)\n")
	if !errors.As(err, &compileErr) || compileErr.Line != 2 || compileErr.Column != 18 {
		t.Fatalf("multi-line position = %+v (%v)", compileErr, err)
	}
	_, err = Compile("outputs:\n  o: close +\n")
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
	text += "outputs: {o: a12}\n"
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
	// 整数参数相乘仍是整数，可以当窗口；参数参与运算后的回看按代入后的值计算。
	factor := mustCompile(t, "params: {window: 10}\noutputs:\n  o: ts_mean(close, window * 2)\n")
	if factor.Lookback != 20 {
		t.Fatalf("lookback = %d, want 20", factor.Lookback)
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
	mustCompile(t, "let:\n  unused: ts_mean(open, 3)\noutputs: {o: close}\n")
	if _, err := Compile("let:\n  unused: ts_mean(open)\noutputs: {o: close}\n"); err == nil {
		t.Fatal("an invalid unused let should still be rejected")
	}
}
