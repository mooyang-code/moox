package engine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

func paramProgram(t *testing.T, params string) *dsl.Program {
	t.Helper()
	strategy, err := dsl.Parse([]byte("name: p\nfactors:\n  fast: {factor: Bias}\n  slow: {factor: Bias}\nfactor_params:\n" + params +
		"rules:\n  - id: r\n    type: rank\n    score: close\n    select: {top: 1}\n    weight: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := dsl.Compile(strategy, []string{"close", "vol"})
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func paramFrame(values map[string]float64) Frame {
	frame := Frame{Rows: map[string]Row{}}
	for id, vol := range values {
		frame.Rows[id] = Row{Values: map[string]float64{"close": 1, "vol": vol}}
		frame.Universe = append(frame.Universe, id)
	}
	return frame
}

var biasDecls = map[string][]dsl.ParamDecl{
	"fast": {{Name: "window", Type: "int", Min: 5, Max: 60, Default: 20}, {Name: "k", Type: "float", Min: 0.5, Max: 4, Default: 2}},
	"slow": {{Name: "window", Type: "int", Min: 5, Max: 60, Default: 20}},
}

// 统一值取各标的有效值的中位数，per_subject 与统一值不同的标的进入覆盖；整型取整、范围外限幅；
// 没写出的参数取定义的默认值；常数表达式不需要任何列。
func TestEvaluateParamsDefaultsOverridesAndFitting(t *testing.T) {
	program := paramProgram(t, "  fast:\n    window:\n      default: vol\n      per_subject: vol * 2\n  slow:\n    window: \"200\"\n")
	outcome := EvaluateParams(program, paramFrame(map[string]float64{"A": 10, "B": 20, "C": 30}), biasDecls)
	want := ParamSet{
		"fast": {
			// A 的 per_subject 是 20，等于统一值，不进覆盖；B 是 40；C 是 60（恰为上限）。
			"window": {Default: 20, Overrides: map[string]float64{"B": 40, "C": 60}},
			"k":      {Default: 2},
		},
		"slow": {"window": {Default: 60}},
	}
	if !reflect.DeepEqual(outcome.Params, want) {
		t.Fatalf("参数快照不符：\n got=%+v\nwant=%+v", outcome.Params, want)
	}
	if len(outcome.Notes) == 0 || !strings.Contains(strings.Join(outcome.Notes, "；"), "slow") {
		t.Fatalf("限幅应记入说明：%v", outcome.Notes)
	}
}

func TestEvaluateParamsRoundsIntegersAndSkipsMemberOnFailure(t *testing.T) {
	program := paramProgram(t, "  fast:\n    window: \"20.6\"\n  slow:\n    window: vol\n")
	frame := paramFrame(map[string]float64{"A": 10})
	delete(frame.Rows["A"].Values, "vol") // slow 的参数在所有标的上都没有有效值
	outcome := EvaluateParams(program, frame, biasDecls)
	if got := outcome.Params["fast"]["window"].Default; got != 21 {
		t.Fatalf("整型参数应四舍五入：%v", got)
	}
	if _, submitted := outcome.Params["slow"]; submitted {
		t.Fatalf("求值失败的成员不应输出：%+v", outcome.Params)
	}
	if len(outcome.Notes) != 1 || !strings.Contains(outcome.Notes[0], "slow") {
		t.Fatalf("应说明失败的成员：%v", outcome.Notes)
	}
}
