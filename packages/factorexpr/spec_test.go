package factorexpr

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseSpecKeepsDocumentOrder(t *testing.T) {
	spec, err := ParseSpec(`
params:
  window: 20
  fast: [5, 10]
let:
  tp: (high + low + close) / 3
  m: ts_mean(tp, window)
outputs:
  z_{fast}: tp - m
  a_{fast}: m
`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range spec.Params {
		names = append(names, p.Name)
	}
	for _, b := range spec.Lets {
		names = append(names, b.Name)
	}
	for _, b := range spec.Outputs {
		names = append(names, b.Name)
	}
	if got := strings.Join(names, ","); got != "window,fast,tp,m,z_{fast},a_{fast}" {
		t.Fatalf("order = %s", got)
	}
	if spec.Params[0].List || !spec.Params[1].List || len(spec.Params[1].Values) != 2 {
		t.Fatalf("params = %+v", spec.Params)
	}
	if !spec.Params[0].Values[0].IsInt || spec.Params[0].Values[0].Value != 20 {
		t.Fatalf("window = %+v", spec.Params[0].Values[0])
	}
	if spec.Lets[1].Line != 7 {
		t.Fatalf("let m line = %d, want 7", spec.Lets[1].Line)
	}
}

func TestParseSpecAcceptsNullSectionsAndFloats(t *testing.T) {
	spec, err := ParseSpec("params:\nlet:\noutputs:\n  o: close * k\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Params) != 0 || len(spec.Lets) != 0 {
		t.Fatalf("spec = %+v", spec)
	}
	spec, err = ParseSpec("params: {k: 0.015}\noutputs: {o: close * k}")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Params[0].Values[0].IsInt || spec.Params[0].Values[0].Value != 0.015 {
		t.Fatalf("k = %+v", spec.Params[0].Values[0])
	}
}

func TestParseSpecRejects(t *testing.T) {
	tooManyParams := "params:\n"
	for i := 0; i <= MaxParams; i++ {
		tooManyParams += fmt.Sprintf("  p%d: %d\n", i, i)
	}
	tooManyParams += "outputs: {o: close}\n"
	cases := []struct {
		name, text, want string
	}{
		{"empty", "  \n", "定义为空"},
		{"too large", "outputs: {o: close}\n#" + strings.Repeat("x", MaxSpecBytes), "超过"},
		{"invalid yaml", "outputs: [a", "不是合法的 YAML"},
		{"multiple documents", "outputs: {a: close}\n---\noutputs: {b: close}\n", "只能包含一个 YAML 文档"},
		{"not a mapping", "- close\n", "必须是 YAML 映射"},
		{"unknown key", "foo: 1\noutputs: {o: close}\n", "未知的键"},
		{"duplicate key", "outputs: {a: close}\noutputs: {b: close}\n", "重复的键"},
		{"anchor", "params: &p {window: 20}\noutputs: {o: close}\n", "锚点"},
		{"alias", "let:\n  a: close\n  b: *x\noutputs: {o: a}\n", "YAML"},
		{"tag", "outputs: {o: !!str close}\n", "标签"},
		{"no outputs", "params: {window: 20}\n", "至少需要一个输出"},
		{"outputs not mapping", "outputs: [close]\n", "必须是映射"},
		{"string param", "params: {w: abc}\noutputs: {o: close}\n", "必须是数值"},
		{"bool param", "params: {w: true}\noutputs: {o: close}\n", "必须是数值"},
		{"nan param", "params: {w: .nan}\noutputs: {o: close}\n", "有限数"},
		{"inf param", "params: {w: .inf}\noutputs: {o: close}\n", "有限数"},
		{"huge int param", "params: {w: 99999999999999999}\noutputs: {o: close}\n", "超出范围"},
		{"empty list", "params: {w: []}\noutputs: {o: close}\n", "列表不能为空"},
		{"nested list", "params: {w: [[1]]}\noutputs: {o: close}\n", "只能是数值"},
		{"too many params", tooManyParams, "不能超过"},
		{"uppercase param", "params: {Window: 20}\noutputs: {o: close}\n", "小写字母开头"},
		{"param named like function", "params: {abs: 1}\noutputs: {o: close}\n", "与函数重名"},
		{"param named like reserved column", "params: {data_time: 1}\noutputs: {o: close}\n", "系统列"},
		{"let shadows param", "params: {x: 1}\nlet: {x: close}\noutputs: {o: x}\n", "已在 params.x 中使用"},
		{"duplicate let", "let:\n  a: close\n  a: open\noutputs: {o: a}\n", "重复的名称"},
		{"empty expression", "outputs: {o: ''}\n", "表达式不能为空"},
		{"null expression", "outputs:\n  o:\n", "表达式不能为空"},
		{"mapping expression", "outputs:\n  o: {a: 1}\n", "表达式不能为空"},
		{"long expression", "outputs:\n  o: " + strings.Repeat("c", MaxExpressionRunes+1) + "\n", "超过"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSpec(tc.text)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestErrorFormatting(t *testing.T) {
	err := &Error{Path: "outputs.o", DocLine: 3, Line: 1, Column: 9, Message: "未知的函数 foo"}
	if got := err.Error(); got != "outputs.o（定义第 3 行，表达式第 1 行第 9 列）：未知的函数 foo" {
		t.Fatalf("Error() = %s", got)
	}
	if got := (&Error{DocLine: 2, Message: "x"}).Error(); got != "定义第 2 行：x" {
		t.Fatalf("Error() = %s", got)
	}
}
