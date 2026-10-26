package factorexpr

import (
	"reflect"
	"strings"
	"testing"
)

// 单输出写 output（没有输出名，输出名就是因子 ID），多输出写 outputs（至少两项）；二者只能写一个（设计 5.2）。
func TestSingleOutputSpec(t *testing.T) {
	factor := mustCompile(t, "params: {k: {type: int, min: 2, max: 9, default: 5}}\noutput: ts_mean(close, k)\n")
	if !factor.SingleOutput() || len(factor.Outputs) != 1 || factor.Outputs[0].Name != "" {
		t.Fatalf("single output = %+v", factor.Outputs)
	}
	if got := factor.OutputNamesFor("ma"); !reflect.DeepEqual(got, []string{"ma"}) {
		t.Fatalf("OutputNamesFor = %v", got)
	}
	if got := factor.CanonicalText(); got != "$k int [2, 9] = 5\noutput = ts_mean(close,$k)\n" {
		t.Fatalf("canonical = %q", got)
	}
}

func TestMultiOutputSpec(t *testing.T) {
	factor := mustCompile(t, "outputs:\n  mid: ts_mean(close, 5)\n  up: ts_max(high, 5)\n")
	if factor.SingleOutput() {
		t.Fatal("outputs with two entries is not a single-output factor")
	}
	if got := factor.OutputNamesFor("boll"); !reflect.DeepEqual(got, []string{"mid", "up"}) {
		t.Fatalf("OutputNamesFor = %v", got)
	}
}

func TestOutputSpecRejects(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"both keys", "output: close\noutputs:\n  a: close\n  b: open\n", "只能写一个"},
		{"both keys reversed", "outputs:\n  a: close\n  b: open\noutput: close\n", "只能写一个"},
		{"single entry in outputs", "outputs:\n  a: close\n", "outputs 至少要有两项"},
		{"no output at all", "params: {w: {type: int, min: 1, max: 5, default: 2}}\n", "至少需要一个输出"},
		{"output not a scalar", "output:\n  a: close\n", "表达式不能为空"},
		{"output empty", "output: ''\n", "表达式不能为空"},
		{"output named var", "outputs:\n  var: close\n  b: open\n", "参数段"},
		{"output looks like a value", "outputs:\n  n5: close\n  b: open\n", "参数段"},
		{"output uppercase", "outputs:\n  Mid: close\n  b: open\n", "不合法"},
		{"output double underscore", "outputs:\n  a__b: close\n  c: open\n", "不合法"},
		{"output too long", "outputs:\n  " + strings.Repeat("x", 17) + ": close\n  b: open\n", "超过 16 个字符"},
		{"output reserved column", "outputs:\n  freq: close\n  b: open\n", "系统列"},
		{"param name uppercase", "params: {Window: {type: int, min: 1, max: 5, default: 2}}\noutput: close\n", "参数名"},
		{"param name double underscore", "params: {a__b: {type: int, min: 1, max: 5, default: 2}}\noutput: close\n", "参数名"},
		{"param name too long", "params: {" + strings.Repeat("w", 17) + ": {type: int, min: 1, max: 5, default: 2}}\noutput: close\n", "超过 16 个字符"},
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

// 程序里单输出因子的输出名取因子 ID，多输出因子用 outputs 里的名字；不同因子可以有同名输出。
func TestProgramOutputNames(t *testing.T) {
	program, err := NewProgram([]ProgramInput{
		{Key: "a", FactorID: "ema", Factor: mustCompile(t, "output: ema(close, 20, 0)\n")},
		{Key: "b", FactorID: "boll", Factor: mustCompile(t, "outputs:\n  mid: ts_mean(close, 20)\n  up: ts_max(high, 20)\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, output := range program.Outputs {
		names = append(names, output.FactorID+"."+output.Name)
	}
	if want := []string{"ema.ema", "boll.mid", "boll.up"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("program outputs = %v, want %v", names, want)
	}
}
