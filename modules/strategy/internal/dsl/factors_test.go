package dsl

import (
	"reflect"
	"strings"
	"testing"
)

const factorsDSL = `name: tuned
factor_params:
  bias__var:
    window:
      default: "20"
      per_subject: "10 + rank(vol) * 20"
  ma__var:
    span: 60
rules:
  - id: r
    type: rank
    score: bias__var - ma__var
    filter: params("bias__var", "window") > 0
    select: {top: 1}
    weight: 1
`

func TestParseFactorParamsAndParamsFunction(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte(factorsDSL))
	if err != nil {
		t.Fatalf("Parse() 出错：%v", err)
	}
	outputs := strategy.FactorParamOutputs()
	want := []ParamOutput{
		{Member: "bias__var", Name: "window", Expr: ParamExpr{Default: "20", PerSubject: "10 + rank(vol) * 20"}},
		{Member: "ma__var", Name: "span", Expr: ParamExpr{Default: "60"}},
	}
	if !reflect.DeepEqual(outputs, want) {
		t.Fatalf("factor_params 不符：%+v", outputs)
	}
	columns, err := ReferencedColumns(strategy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(columns, []string{"bias__var", "ma__var", "vol"}) {
		t.Fatalf("引用列应包含参数表达式的列、不含 params() 的参数：%v", columns)
	}
	refs, err := ReferencedParamRefs(strategy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(refs, []ParamRef{{Member: "bias__var", Name: "window"}}) {
		t.Fatalf("params() 引用不符：%+v", refs)
	}
	program, err := Compile(strategy, []string{"bias__var", "ma__var", "vol", "close"})
	if err != nil {
		t.Fatalf("Compile() 出错：%v", err)
	}
	if len(program.Params) != 2 || program.Params[0].PerSubject == nil || program.Params[1].PerSubject != nil {
		t.Fatalf("参数表达式编译不符：%+v", program.Params)
	}
	if !reflect.DeepEqual(program.Columns, []string{"bias__var", "ma__var", "vol"}) {
		t.Fatalf("程序列应包含参数表达式的列：%v", program.Columns)
	}
	if !reflect.DeepEqual(program.ParamRefs, []ParamRef{{Member: "bias__var", Name: "window"}}) {
		t.Fatalf("程序的 params() 引用不符：%+v", program.ParamRefs)
	}
	filter := program.Rules[0].Filter
	if len(filter.ParamRefs) != 1 || filter.ParamRefs[0].Variable == "" {
		t.Fatalf("表达式应带着改写后的变量名：%+v", filter.ParamRefs)
	}
	env := map[string]any{filter.ParamRefs[0].Variable: float64(5), "bias__var": float64(1), "ma__var": float64(1), "vol": float64(1), "close": float64(1),
		"bars": []map[string]float64{}, "score": float64(0), "instrument_id": ""}
	got, err := filter.Run(env)
	if err != nil || got != true {
		t.Fatalf("params() 变量参与求值：%v %v", got, err)
	}
}

func TestValidateFactorParamsRejectsBadDeclarations(t *testing.T) {
	t.Parallel()
	rules := "rules:\n  - id: r\n    type: rank\n    score: close\n    select: {top: 1}\n    weight: 1\n"
	cases := map[string]string{
		"factors 段已删除": "factors:\n  a: {factor: bias}\n",
		"成员键不合法":       "factor_params:\n  Bias__var:\n    window: 3\n",
		"不是可变成员":       "factor_params:\n  bias:\n    window: 3\n",
		"带参数段的固定成员":    "factor_params:\n  bias__20:\n    window: 3\n",
		"缺少 default":   "factor_params:\n  bias__var:\n    window: {per_subject: close}\n",
		"参数名非法":        "factor_params:\n  bias__var:\n    Window: 3\n",
	}
	for name, body := range cases {
		if _, err := Parse([]byte("name: x\n" + body + rules)); err == nil {
			t.Errorf("%s：应被拒绝", name)
		}
	}
	if _, err := Parse([]byte("name: x\nfactors:\n  a: {factor: bias}\n" + rules)); err == nil || !strings.Contains(err.Error(), "factors") {
		t.Errorf("factors 段应给出明确的迁移提示：%v", err)
	}
}

// 与规则表达式一样，结果类型在编译（启用）时检查：参数表达式必须是数值。
func TestCompileRejectsNonNumericParamExpression(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte("name: x\nfactor_params:\n  bias__var:\n    window: \"close > 1\"\nrules:\n  - id: r\n    type: rank\n    score: close\n    select: {top: 1}\n    weight: 1\n"))
	if err != nil {
		t.Fatalf("Parse() 出错：%v", err)
	}
	if _, err := Compile(strategy, []string{"close"}); err == nil || !strings.Contains(err.Error(), "factor_params.bias__var.window") {
		t.Fatalf("应在编译时拒绝非数值参数表达式：%v", err)
	}
}

func TestParamsFunctionRejectsBadCalls(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"参数个数":     `params("bias__var")`,
		"不是字面量":    `params(close, "window")`,
		"固定成员":     `params("bias", "window")`,
		"成员键非法":    `params("Bias__var", "window")`,
		"参数名非法":    `params("bias__var", "Window")`,
		"其他函数":     `foo("bias__var", "window")`,
		"嵌套在 rank": `rank(params("bias__var", "window")) > 0.5`,
	}
	for name, source := range cases {
		_, err := Analyze(source+" > 0", StageFilter)
		if name == "嵌套在 rank" {
			// rank 只能在 score 中使用，这里只确认 params 本身在 score 的 rank 里合法。
			if _, err := Analyze(`rank(params("bias__var", "window"))`, StageScore); err != nil {
				t.Errorf("score 里 rank(params(...)) 应合法：%v", err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s：%s 应被拒绝", name, source)
		}
	}
}
