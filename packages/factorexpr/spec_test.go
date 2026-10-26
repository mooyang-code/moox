package factorexpr

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseSpecKeepsDocumentOrder(t *testing.T) {
	spec, err := ParseSpec(`
params:
  window: {type: int, min: 2, max: 50, default: 20}
  fast: {type: int, min: 2, max: 10, default: 5}
let:
  tp: (high + low + close) / 3
  m: ts_mean(tp, window)
outputs:
  z: tp - m
  a: m
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
	if got := strings.Join(names, ","); got != "window,fast,tp,m,z,a" {
		t.Fatalf("order = %s", got)
	}
	if !spec.Params[0].IsInt() || spec.Params[0].Default != 20 || spec.Params[1].Max != 10 {
		t.Fatalf("params = %+v", spec.Params)
	}
	if spec.Lets[1].Line != 7 {
		t.Fatalf("let m line = %d, want 7", spec.Lets[1].Line)
	}
}

func TestParseSpecAcceptsNullSectionsAndFloats(t *testing.T) {
	spec, err := ParseSpec("params:\nlet:\noutput: close * k\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Params) != 0 || len(spec.Lets) != 0 {
		t.Fatalf("spec = %+v", spec)
	}
	spec, err = ParseSpec("params: {k: {type: float, min: 0, max: 1, default: 0.015}}\noutput: close * k")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Params[0].IsInt() || spec.Params[0].Default != 0.015 {
		t.Fatalf("k = %+v", spec.Params[0])
	}
}

func TestParseSpecRejects(t *testing.T) {
	tooManyParams := "params:\n"
	for i := 0; i <= MaxParams; i++ {
		tooManyParams += fmt.Sprintf("  p%d: {type: int, min: 0, max: 99, default: %d}\n", i, i)
	}
	tooManyParams += "output: close\n"
	cases := []struct {
		name, text, want string
	}{
		{"empty", "  \n", "定义为空"},
		{"too large", "output: close\n#" + strings.Repeat("x", MaxSpecBytes), "超过"},
		{"invalid yaml", "outputs: [a", "不是合法的 YAML"},
		{"multiple documents", "output: close\n---\noutput: close\n", "只能包含一个 YAML 文档"},
		{"not a mapping", "- close\n", "必须是 YAML 映射"},
		{"unknown key", "foo: 1\noutput: close\n", "未知的键"},
		{"duplicate key", "output: close\noutput: close\n", "重复的键"},
		{"anchor", "params: &p {window: {type: int, min: 0, max: 99, default: 1}}\noutput: close\n", "锚点"},
		{"alias", "let:\n  a: close\n  b: *x\noutput: a\n", "YAML"},
		{"tag", "output: !!str close\n", "标签"},
		{"no outputs", "params: {window: {type: int, min: 0, max: 99, default: 1}}\n", "至少需要一个输出"},
		{"outputs not mapping", "outputs: [close]\n", "必须是映射"},
		{"string param", "params: {w: {type: int, min: abc, max: 1, default: 1}}\noutput: close\n", "必须是数值"},
		{"bool param", "params: {w: {type: int, min: 0, max: true, default: 1}}\noutput: close\n", "必须是数值"},
		{"nan param", "params: {w: {type: float, min: 0, max: .nan, default: 1}}\noutput: close\n", "有限数"},
		{"inf param", "params: {w: {type: float, min: 0, max: .inf, default: 1}}\noutput: close\n", "有限数"},
		{"huge int param", "params: {w: {type: int, min: 0, max: 99999999999999999, default: 1}}\noutput: close\n", "超出范围"},
		{"list param", "params: {w: [1, 2]}\noutput: close\n", "参数声明必须是映射"},
		{"nested value", "params: {w: {type: int, min: [1], max: 2, default: 1}}\noutput: close\n", "必须是数值"},
		{"too many params", tooManyParams, "不能超过"},
		{"uppercase param", "params: {Window: {type: int, min: 0, max: 99, default: 1}}\noutput: close\n", "小写字母开头"},
		{"param named like function", "params: {abs: {type: int, min: 0, max: 99, default: 1}}\noutput: close\n", "与函数重名"},
		{"param named like reserved column", "params: {data_time: {type: int, min: 0, max: 99, default: 1}}\noutput: close\n", "系统列"},
		{"let shadows param", "params: {x: {type: int, min: 0, max: 99, default: 1}}\nlet: {x: close}\noutput: x\n", "已在 params.x 中使用"},
		{"duplicate let", "let:\n  a: close\n  a: open\noutput: a\n", "重复的名称"},
		{"empty expression", "output: ''\n", "表达式不能为空"},
		{"null expression", "outputs:\n  o:\n", "表达式不能为空"},
		{"mapping expression", "output: {a: 1}\n", "表达式不能为空"},
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
