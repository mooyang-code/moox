package engine

import (
	"testing"

	"github.com/expr-lang/expr"
	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// 运行错误只写中文原因，表达式库的英文原文不进入结果。保存与编译已经拒绝了会在求值时出错的写法，这里直接用表达式库
// 构造运行时必然出错的程序（?? 的左边是不会为空的列值，结果类型对不上要求的布尔或数值）来验证兜底。
func TestRuntimeErrorsAreChinese(t *testing.T) {
	env := expr.Env(map[string]any{"close": float64(0), "instrument_id": ""})
	boolProgram, err := expr.Compile("close ?? true", env, expr.AsBool())
	if err != nil {
		t.Fatal(err)
	}
	numberProgram, err := expr.Compile("instrument_id ?? close", env, expr.AsFloat64())
	if err != nil {
		t.Fatal(err)
	}
	row := Row{Values: map[string]float64{"close": 1}}
	if _, err := runBool(&dsl.Expression{Program: boolProgram}, "A", row, 0); err == nil || err.Error() != "表达式求值出错" {
		t.Fatalf("布尔阶段的运行错误应只写中文原因：%v", err)
	}
	result := evaluateNumeric(&dsl.Expression{Program: numberProgram, Numeric: true}, []string{"A"}, map[string]Row{"A": row})
	if got := result.failed["A"]; got != "score_error:求值出错" {
		t.Fatalf("分数阶段的运行错误应写成 score_error:求值出错：%q", got)
	}
}
