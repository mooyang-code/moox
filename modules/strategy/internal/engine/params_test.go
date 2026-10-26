package engine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

func paramProgram(t *testing.T, params string) *dsl.Program {
	t.Helper()
	strategy, err := dsl.Parse([]byte("name: p\nfactor_params:\n" + params +
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
	"bias__var": {{Name: "window", Type: "int", Min: 5, Max: 60, Default: 20}, {Name: "k", Type: "float", Min: 0.5, Max: 4, Default: 2}},
	"ma__var":   {{Name: "window", Type: "int", Min: 5, Max: 60, Default: 20}},
}

// 统一值取各标的有效值的中位数，per_subject 与统一值不同的标的进入覆盖；整型取整、范围外限幅；
// 没写出的参数取定义的默认值；常数表达式不需要任何列。
func TestEvaluateParamsDefaultsOverridesAndFitting(t *testing.T) {
	program := paramProgram(t, "  bias__var:\n    window:\n      default: vol\n      per_subject: vol * 2\n  ma__var:\n    window: \"200\"\n")
	outcome := EvaluateParams(program, paramFrame(map[string]float64{"A": 10, "B": 20, "C": 30}), biasDecls)
	want := ParamSet{
		"bias__var": {
			// A 的 per_subject 是 20，等于统一值，不进覆盖；B 是 40；C 是 60（恰为上限）。
			"window": {Default: 20, Overrides: map[string]float64{"B": 40, "C": 60}},
			"k":      {Default: 2},
		},
		"ma__var": {"window": {Default: 60}},
	}
	if !reflect.DeepEqual(outcome.Params, want) {
		t.Fatalf("参数快照不符：\n got=%+v\nwant=%+v", outcome.Params, want)
	}
	if len(outcome.Notes) == 0 || !strings.Contains(strings.Join(outcome.Notes, "；"), "ma__var") {
		t.Fatalf("限幅应记入说明：%v", outcome.Notes)
	}
}

func TestEvaluateParamsRoundsIntegersAndSkipsMemberOnFailure(t *testing.T) {
	program := paramProgram(t, "  bias__var:\n    window: \"20.6\"\n  ma__var:\n    window: vol\n")
	frame := paramFrame(map[string]float64{"A": 10})
	delete(frame.Rows["A"].Values, "vol") // ma__var 的参数在所有标的上都没有有效值
	outcome := EvaluateParams(program, frame, biasDecls)
	if got := outcome.Params["bias__var"]["window"].Default; got != 21 {
		t.Fatalf("整型参数应四舍五入：%v", got)
	}
	if _, submitted := outcome.Params["ma__var"]; submitted {
		t.Fatalf("求值失败的成员不应输出：%+v", outcome.Params)
	}
	if len(outcome.Notes) != 1 || !strings.Contains(outcome.Notes[0], "ma__var") {
		t.Fatalf("应说明失败的成员：%v", outcome.Notes)
	}
}

// 参数表达式引用的历史列（bars[-1]）缺值的标的不参与统一值与覆盖，不能把缺失读成 0 拉低中位数；上游失败的标的同样不参与。
func TestEvaluateParamsSkipsSubjectsMissingReferencedColumns(t *testing.T) {
	program := paramProgram(t, "  bias__var:\n    window:\n      default: bars[-1].vol\n      per_subject: bars[-1].vol + 1\n")
	frame := Frame{Universe: []string{"A", "B", "C"}, Rows: map[string]Row{
		"A": {Values: map[string]float64{"close": 1, "vol": 30}, Previous: map[string]float64{"vol": 30}},
		"B": {Values: map[string]float64{"close": 1, "vol": 30}},                                          // 新标的，没有上一根
		"C": {Values: map[string]float64{"close": 1, "vol": 50}, Previous: map[string]float64{"vol": 50}}, // 上游失败
	}, FailedColumns: map[string]map[string]string{"vol": {"C": FailedSource}}}
	outcome := EvaluateParams(program, frame, biasDecls)
	got := outcome.Params["bias__var"]["window"]
	if got.Default != 30 || len(got.Overrides) != 1 || got.Overrides["A"] != 31 {
		t.Fatalf("缺数的标的不应参与：%+v", got)
	}
}

// params() 读取这一行的 __params：filter 里按各标的实际用的参数筛选；行里缺参数（新标的、旧行）读成缺失，
// 条件为假，不会被当成 0 或默认值放行。
func TestParamsFunctionReadsRowParams(t *testing.T) {
	strategy, err := dsl.Parse([]byte(`name: p
factor_params:
  bias__var:
    window: 20
rules:
  - id: r
    type: rank
    filter: params("bias__var", "window") > 15
    score: close
    select: {top: 3}
    weight: {total: 1}
`))
	if err != nil {
		t.Fatal(err)
	}
	program, err := dsl.Compile(strategy, []string{"close"})
	if err != nil {
		t.Fatal(err)
	}
	row := func(close float64, params map[string]map[string]float64) Row {
		return Row{Values: map[string]float64{"close": close}, Params: params}
	}
	frame := Frame{
		Universe: []string{"A", "B", "C"}, Expected: map[string][]string{"r": {"A", "B", "C"}}, AgedOut: map[string][]string{},
		Rows: map[string]Row{
			"A": row(3, map[string]map[string]float64{"bias__var": {"window": 30}}),
			"B": row(2, map[string]map[string]float64{"bias__var": {"window": 10}}),
			"C": row(1, nil),
		},
	}
	decision, err := Evaluate(program, frame, State{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != StatusOK || len(decision.Targets) != 1 || decision.Targets[0].InstrumentID != "A" {
		t.Fatalf("只有窗口大于 15 的 A 应入选：%+v", decision)
	}
}
