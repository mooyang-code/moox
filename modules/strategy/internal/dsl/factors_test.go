package dsl

import (
	"reflect"
	"strings"
	"testing"
)

const factorsDSL = `name: tuned
factors:
  fast: {factor: Bias}
  slow: {factor: Bias}
factor_params:
  fast:
    window:
      default: "20"
      per_subject: "10 + rank(vol) * 20"
  slow:
    window: 60
rules:
  - id: r
    type: rank
    score: fast_bias - slow_bias
    filter: fast__window > 0
    select: {top: 1}
    weight: 1
`

func TestParseFactorsAndParams(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte(factorsDSL))
	if err != nil {
		t.Fatalf("Parse() 出错：%v", err)
	}
	if !reflect.DeepEqual(strategy.Factors, []FactorDecl{{Alias: "fast", FactorID: "Bias"}, {Alias: "slow", FactorID: "Bias"}}) {
		t.Fatalf("factors 不符：%+v", strategy.Factors)
	}
	outputs := strategy.FactorParamOutputs()
	want := []ParamOutput{
		{Alias: "fast", Name: "window", Expr: ParamExpr{Default: "20", PerSubject: "10 + rank(vol) * 20"}},
		{Alias: "slow", Name: "window", Expr: ParamExpr{Default: "60"}},
	}
	if !reflect.DeepEqual(outputs, want) {
		t.Fatalf("factor_params 不符：%+v", outputs)
	}
	if got := strategy.FactorIDs(); !reflect.DeepEqual(got, []string{"Bias"}) {
		t.Fatalf("FactorIDs 不符：%v", got)
	}
	columns, err := ReferencedColumns(strategy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(columns, []string{"fast__window", "fast_bias", "slow_bias", "vol"}) {
		t.Fatalf("引用列应包含参数表达式的列：%v", columns)
	}
	program, err := Compile(strategy, []string{"fast_bias", "slow_bias", "fast__window", "vol", "close"})
	if err != nil {
		t.Fatalf("Compile() 出错：%v", err)
	}
	if len(program.Params) != 2 || program.Params[0].PerSubject == nil || program.Params[1].PerSubject != nil {
		t.Fatalf("参数表达式编译不符：%+v", program.Params)
	}
	if !reflect.DeepEqual(program.Columns, []string{"fast__window", "fast_bias", "slow_bias", "vol"}) {
		t.Fatalf("程序列应包含参数表达式的列：%v", program.Columns)
	}
}

func TestValidateFactorsRejectsBadDeclarations(t *testing.T) {
	t.Parallel()
	rules := "rules:\n  - id: r\n    type: rank\n    score: close\n    select: {top: 1}\n    weight: 1\n"
	cases := map[string]string{
		"别名含连续下划线":   "factors:\n  a__b: {factor: Bias}\n",
		"定义 ID 非法":   "factors:\n  a: {factor: \"1x\"}\n",
		"未知字段":       "factors:\n  a: {factor: Bias, window: 3}\n",
		"参数针对未声明的成员": "factors:\n  a: {factor: Bias}\nfactor_params:\n  b:\n    window: 3\n",
		"缺少 default": "factors:\n  a: {factor: Bias}\nfactor_params:\n  a:\n    window: {per_subject: close}\n",
		"参数名非法":      "factors:\n  a: {factor: Bias}\nfactor_params:\n  a:\n    Window: 3\n",
	}
	for name, body := range cases {
		if _, err := Parse([]byte("name: x\n" + body + rules)); err == nil {
			t.Errorf("%s：应被拒绝", name)
		} else if !strings.Contains(err.Error(), "factor") && !strings.Contains(err.Error(), "参数") && !strings.Contains(err.Error(), "别名") {
			t.Logf("%s：%v", name, err)
		}
	}
}

// 与规则表达式一样，结果类型在编译（启用）时检查：参数表达式必须是数值。
func TestCompileRejectsNonNumericParamExpression(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte("name: x\nfactors:\n  a: {factor: Bias}\nfactor_params:\n  a:\n    window: \"close > 1\"\nrules:\n  - id: r\n    type: rank\n    score: close\n    select: {top: 1}\n    weight: 1\n"))
	if err != nil {
		t.Fatalf("Parse() 出错：%v", err)
	}
	if _, err := Compile(strategy, []string{"close"}); err == nil || !strings.Contains(err.Error(), "factor_params.a.window") {
		t.Fatalf("应在编译时拒绝非数值参数表达式：%v", err)
	}
}
