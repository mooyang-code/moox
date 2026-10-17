package factorexpr

import (
	"reflect"
	"strings"
	"testing"
)

func TestExpandSingleListParam(t *testing.T) {
	factor := mustCompile(t, `
params: {window: [5, 10, 20]}
outputs:
  bias_q_{window}: ts_rank(close / ts_mean(close, window) - 1, window)
`)
	if got, want := factor.OutputNames(), []string{"bias_q_5", "bias_q_10", "bias_q_20"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outputs = %v, want %v", got, want)
	}
	if got := factor.Outputs[1].Root.Canonical(); got != "ts_rank(sub(div(close,ts_mean(close,10)),1),10)" {
		t.Fatalf("canonical = %s", got)
	}
	if factor.Lookback != 39 {
		t.Fatalf("lookback = %d, want 39", factor.Lookback)
	}
}

// 多个列表参数做笛卡尔积：先按输出的书写顺序，每个输出内按参数声明顺序与列表顺序。
func TestExpandCartesianProduct(t *testing.T) {
	factor := mustCompile(t, `
params:
  fast: [2, 3]
  slow: [10, 20]
  scale: 100
let:
  diff: ts_mean(close, fast) - ts_mean(close, slow)
outputs:
  x_{fast}_{slow}: diff * scale
  y_{fast}_{slow}: diff
`)
	want := []string{"x_2_10", "x_2_20", "x_3_10", "x_3_20", "y_2_10", "y_2_20", "y_3_10", "y_3_20"}
	if got := factor.OutputNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("outputs = %v, want %v", got, want)
	}
	if got := factor.Outputs[2].Root.Canonical(); got != "mul(sub(ts_mean(close,3),ts_mean(close,10)),100)" {
		t.Fatalf("x_3_10 = %s", got)
	}
	if factor.Lookback != 20 {
		t.Fatalf("lookback = %d", factor.Lookback)
	}
}

func TestExpandScalarParamInTemplate(t *testing.T) {
	factor := mustCompile(t, "params: {window: 20}\noutputs:\n  ma_{window}: ts_mean(close, window)\n")
	if got := factor.OutputNames(); !reflect.DeepEqual(got, []string{"ma_20"}) {
		t.Fatalf("outputs = %v", got)
	}
}

func TestExpandRejects(t *testing.T) {
	cases := []struct {
		name, text, want string
	}{
		{"list param missing from template", "params: {w: [5, 10]}\noutputs:\n  ma: ts_mean(close, w)\n", "必须出现在输出列名里"},
		{"duplicate values", "params: {w: [5, 5]}\noutputs:\n  ma_{w}: ts_mean(close, w)\n", "重复"},
		{"duplicate across outputs", "params: {w: 5}\noutputs:\n  ma_{w}: ts_mean(close, w)\n  ma_5: close\n", "重复"},
		{"too many outputs", "params:\n  a: [1, 2, 3, 4, 5, 6, 7, 8, 9]\n  b: [1, 2, 3, 4, 5, 6, 7, 8]\noutputs:\n  o_{a}_{b}: close * a * b\n", "超过 64 个"},
		{"float in template", "params: {w: [0.5, 1]}\noutputs:\n  o_{w}: close * w\n", "必须是整数"},
		{"unknown template param", "outputs:\n  o_{nope}: close\n", "未定义的参数"},
		{"bad characters", "params: {w: 5}\noutputs:\n  o-{w}: close\n", "只能包含"},
		{"unclosed brace", "params: {w: 5}\noutputs:\n  o_{w: close\n", "花括号不匹配"},
		{"stray close brace", "outputs:\n  o_}x: close\n", "花括号不匹配"},
		{"leading digit", "params: {w: 5}\noutputs:\n  '{w}_x': close\n", "不合法"},
		{"negative value", "params: {w: [-5]}\noutputs:\n  o_{w}: close * w\n", "不合法"},
		{"reserved output", "outputs:\n  data_time: close\n", "系统列"},
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

func mustCompile(t *testing.T, text string) *Factor {
	t.Helper()
	factor, err := Compile(text)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return factor
}
