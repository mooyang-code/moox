package exec

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/mooyang-code/moox/packages/factorexpr"
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
