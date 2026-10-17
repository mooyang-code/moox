package exec

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/mooyang-code/moox/packages/factorexpr"
	"gopkg.in/yaml.v3"
)

type goldenData struct {
	Length  int        `json:"length"`
	X       []*float64 `json:"x"`
	Y       []*float64 `json:"y"`
	Rolling []struct {
		Expr   string     `json:"expr"`
		Tol    float64    `json:"tol"`
		Expect []*float64 `json:"expect"`
	} `json:"rolling"`
	Cross struct {
		Subjects []string     `json:"subjects"`
		Tags     []string     `json:"tags"`
		Times    int          `json:"times"`
		V        [][]*float64 `json:"v"`
		Cases    []struct {
			Expr   string       `json:"expr"`
			Tol    float64      `json:"tol"`
			Expect [][]*float64 `json:"expect"`
		} `json:"cases"`
	} `json:"cross"`
	Factors struct {
		Bars    map[string][]*float64 `json:"bars"`
		Outputs map[string][]*float64 `json:"outputs"`
	} `json:"factors"`
}

func loadGolden(t *testing.T) *goldenData {
	t.Helper()
	raw, err := os.ReadFile("testdata/golden/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var data goldenData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return &data
}

func value(v *float64) float64 {
	if v == nil {
		return math.NaN()
	}
	return *v
}

// close 判断数值是否在容差内：NaN 只与 NaN 相等，相对误差的分母取 max(1, |want|)。
func closeEnough(got, want, tol float64) bool {
	if math.IsNaN(want) || math.IsNaN(got) {
		return math.IsNaN(want) && math.IsNaN(got)
	}
	return math.Abs(got-want) <= tol*math.Max(1, math.Abs(want))
}

func compileProgram(t *testing.T, expressions map[string]string) *factorexpr.Program {
	t.Helper()
	var inputs []factorexpr.ProgramInput
	for name, expression := range expressions {
		factor, err := factorexpr.Compile("outputs:\n  " + name + ": |\n    " + expression + "\n")
		if err != nil {
			t.Fatalf("compile %s: %v", expression, err)
		}
		inputs = append(inputs, factorexpr.ProgramInput{FactorID: name, Factor: factor})
	}
	program, err := factorexpr.NewProgram(inputs)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func singleSeriesPanel(t *testing.T, length int, columns map[string][]*float64) *Panel {
	t.Helper()
	names := make([]string, 0, len(columns))
	for name := range columns {
		names = append(names, name)
	}
	panel, err := NewPanel(length, names, []SeriesKey{{Subject: "S", Tag: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range columns {
		column, _ := panel.ColumnIndex(name)
		for i, v := range values {
			panel.Set(column, 0, i, value(v))
		}
	}
	for i := 0; i < length; i++ {
		panel.MarkPresent(0, i)
	}
	return panel
}

// 各时序算子与 pandas 严格窗口的结果一致（设计 12.1 的 pandas 锚点）。
func TestRollingMatchesPandas(t *testing.T) {
	data := loadGolden(t)
	panel := singleSeriesPanel(t, data.Length, map[string][]*float64{"x": data.X, "y": data.Y})
	for _, tc := range data.Rolling {
		t.Run(tc.Expr, func(t *testing.T) {
			program := compileProgram(t, map[string]string{"o": tc.Expr})
			result, err := Run(context.Background(), program, panel, Options{Start: program.Lookback - 1, End: data.Length - 1})
			if err != nil {
				t.Fatal(err)
			}
			mismatches := 0
			for i := result.Start; i <= result.End; i++ {
				got, want := result.Value(0, 0, i), value(tc.Expect[i])
				if !closeEnough(got, want, tc.Tol) {
					if mismatches < 5 {
						t.Errorf("t=%d: got %v, want %v", i, got, want)
					}
					mismatches++
				}
			}
			if mismatches > 0 {
				t.Errorf("%d mismatches", mismatches)
			}
		})
	}
}

func TestCrossMatchesPandas(t *testing.T) {
	data := loadGolden(t).Cross
	var keys []SeriesKey
	for i, subject := range data.Subjects {
		keys = append(keys, SeriesKey{Subject: subject, Tag: data.Tags[i]})
	}
	panel, err := NewPanel(data.Times, []string{"v"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	for i, key := range keys {
		s, _ := panel.SeriesIndex(key)
		for tm, v := range data.V[i] {
			panel.Set(0, s, tm, value(v))
			panel.MarkPresent(s, tm)
		}
	}
	for _, tc := range data.Cases {
		t.Run(tc.Expr, func(t *testing.T) {
			program := compileProgram(t, map[string]string{"o": tc.Expr})
			result, err := Run(context.Background(), program, panel, Options{Start: 0, End: data.Times - 1})
			if err != nil {
				t.Fatal(err)
			}
			for i, key := range keys {
				s, _ := panel.SeriesIndex(key)
				for tm := 0; tm < data.Times; tm++ {
					if got, want := result.Value(0, s, tm), value(tc.Expect[i][tm]); !closeEnough(got, want, tc.Tol) {
						t.Errorf("%s t=%d: got %v, want %v", key.Subject, tm, got, want)
					}
				}
			}
		})
	}
}

type libraryEntry struct {
	FactorID string `yaml:"factor_id"`
	Spec     string `yaml:"spec"`
	Lookback int    `yaml:"lookback"`
}

// 现有 11 个 Python 因子在合成 K 线上的输出，与表达式写法在完整窗口上的结果一致。
func TestLibraryMatchesPythonFactors(t *testing.T) {
	data := loadGolden(t).Factors
	raw, err := os.ReadFile("../testdata/xbx_factors.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var entries []libraryEntry
	if err := yaml.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	var inputs []factorexpr.ProgramInput
	roots := map[string]*factorexpr.Node{}
	for _, entry := range entries {
		factor, err := factorexpr.Compile(entry.Spec)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, factorexpr.ProgramInput{FactorID: entry.FactorID, Factor: factor})
		for _, output := range factor.Outputs {
			roots[output.Name] = output.Root
		}
	}
	program, err := factorexpr.NewProgram(inputs)
	if err != nil {
		t.Fatal(err)
	}
	length := len(data.Bars["close"])
	panel := singleSeriesPanel(t, length, data.Bars)
	result, err := Run(context.Background(), program, panel, Options{Start: program.Lookback - 1, End: length - 1, Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Columns) != 11 {
		t.Fatalf("columns = %d", len(result.Columns))
	}
	for c, column := range result.Columns {
		expect, ok := data.Outputs[column.Name]
		if !ok {
			t.Fatalf("no python output for %s", column.Name)
		}
		worst, flips := 0.0, 0
		for i := result.Start; i <= result.End; i++ {
			got, want := result.Value(c, 0, i), value(expect[i])
			if !closeEnough(got, want, 1e-9) {
				// 离散跳变：pandas 的滑动均值在内容相同的窗口上会差 1 个最小精度单位，打破本该存在的并列；精确求和保住了并列。
				// 只有能证明当期值在窗口内确有精确并列时才放行（设计 12.2 的离散跳变口径）。
				if root := roots[column.Name]; root.Op == "ts_rank" && hasExactTie(t, panel, root, i) {
					flips++
					continue
				}
				t.Errorf("%s t=%d: got %v, want %v", column.Name, i, got, want)
				continue
			}
			if !math.IsNaN(want) {
				worst = math.Max(worst, math.Abs(got-want)/math.Max(1, math.Abs(want)))
			}
		}
		t.Logf("%s: max relative error %.3g, %d tie flips", column.Name, worst, flips)
		if flips*100 > result.Len() {
			t.Errorf("%s: %d tie flips exceed 1%%", column.Name, flips)
		}
	}
}

// hasExactTie 计算 ts_rank 的参数在以 t 结尾的窗口上的值，报告当期值是否与窗口内另一个值精确相等。
func hasExactTie(t *testing.T, panel *Panel, rank *factorexpr.Node, at int) bool {
	t.Helper()
	inner := rank.Args[0]
	window := rank.Ints[0]
	program, err := factorexpr.NewProgram([]factorexpr.ProgramInput{{FactorID: "inner", Factor: &factorexpr.Factor{
		Outputs: []factorexpr.Output{{Name: "inner", Root: inner}}, Lookback: inner.Lookback,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), program, panel, Options{Start: at - window + 1, End: at})
	if err != nil {
		t.Fatal(err)
	}
	current := result.Value(0, 0, at)
	for i := at - window + 1; i < at; i++ {
		if result.Value(0, 0, i) == current {
			return true
		}
	}
	return false
}
