package factorexpr

import (
	"math"
	"testing"
)

func TestRegistryIsComplete(t *testing.T) {
	list := Functions()
	if len(list) != len(functionList) {
		t.Fatalf("Functions() = %d, want %d", len(list), len(functionList))
	}
	for i, fn := range list {
		if i > 0 && (list[i-1].Category > fn.Category || (list[i-1].Category == fn.Category && list[i-1].Name >= fn.Name)) {
			t.Fatalf("functions not sorted at %s", fn.Name)
		}
		if fn.Doc == "" || fn.Example == "" || fn.Lookback == "" {
			t.Errorf("%s lacks documentation", fn.Name)
		}
		if fn.Operator() {
			t.Errorf("%s should not be an operator", fn.Name)
		}
		// 示例本身必须能编译（示例都写成数值表达式）。
		if _, err := Compile(outputSpec(fn.Example + " + close * 0")); err != nil {
			t.Errorf("%s example %q does not compile: %v", fn.Name, fn.Example, err)
		}
		if fn.Category == CategoryElementwise && Scalar(fn.Name) == nil {
			t.Errorf("elementwise %s has no scalar implementation", fn.Name)
		}
	}
	for _, fn := range operatorList {
		if !fn.Operator() || Scalar(fn.Name) == nil {
			t.Errorf("operator %s misconfigured", fn.Name)
		}
		if _, ok := LookupFunction(fn.Name); ok {
			t.Errorf("operator %s must not be callable by name", fn.Name)
		}
	}
	if _, ok := LookupOperator("add"); !ok {
		t.Fatal("add operator missing")
	}
}

func TestScalarSemantics(t *testing.T) {
	isNaN := math.IsNaN
	checks := []struct {
		op   string
		args []float64
		want float64
	}{
		{"div", []float64{1, 0}, math.NaN()},
		{"div", []float64{6, 3}, 2},
		{"mod", []float64{5, 0}, math.NaN()},
		{"log", []float64{0}, math.NaN()},
		{"sqrt", []float64{-1}, math.NaN()},
		{"exp", []float64{1000}, math.NaN()}, // 溢出为 Inf，记为 NaN
		{"pow", []float64{-8, 1.0 / 3}, math.NaN()},
		{"signed_power", []float64{-8, 1.0 / 3}, -2},
		{"max", []float64{1, math.NaN()}, math.NaN()},
		{"clip", []float64{5, 3, 1}, math.NaN()},
		{"clip", []float64{5, 1, 3}, 3},
		{"gt", []float64{math.NaN(), 0}, math.NaN()},
		{"gt", []float64{1, 0}, 1},
		{"and", []float64{0, math.NaN()}, math.NaN()},
		{"or", []float64{1, math.NaN()}, math.NaN()},
		{"and", []float64{1, 0}, 0},
		{"not", []float64{math.NaN()}, math.NaN()},
		{"where", []float64{math.NaN(), 1, 2}, math.NaN()},
		{"where", []float64{0, 1, 2}, 2},
		{"fill_nan", []float64{math.NaN(), 7}, 7},
		{"is_nan", []float64{math.NaN()}, 1},
		{"sign", []float64{-3}, -1},
	}
	for _, c := range checks {
		got := Scalar(c.op)(c.args)
		if isNaN(c.want) != isNaN(got) || (!isNaN(got) && math.Abs(got-c.want) > 1e-12) {
			t.Errorf("%s%v = %v, want %v", c.op, c.args, got, c.want)
		}
	}
	if Scalar("ts_mean") != nil {
		t.Fatal("ts_mean is not elementwise")
	}
}
