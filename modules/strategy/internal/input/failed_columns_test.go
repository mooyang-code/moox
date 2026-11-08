package input

import (
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
)

// 因子失败的标的记在该因子产出的列上（factor_failed）；K 线 View 的失败记在全部引用列上（source_failed）。
func TestFailedColumnsMapsFactorAndSourceFailures(t *testing.T) {
	resolved := Resolved{Columns: map[string]ColumnBinding{
		"ma":    {Source: "factor", Member: "f_ma", FactorID: "f_ma"},
		"close": {Source: "view"},
	}}
	program := &dsl.Program{Columns: []string{"ma", "close"}}
	result := readiness.Result{
		FailedByFactor: map[string][]string{"f_ma": {"ETH-USDT", "SOL-USDT"}},
		FailedSubjects: []string{"BTC-USDT"},
	}
	got := failedColumns(resolved, program, result)
	want := map[string]map[string]string{
		"ma":    {"ETH-USDT": engine.FailedFactor, "SOL-USDT": engine.FailedFactor, "BTC-USDT": engine.FailedSource},
		"close": {"BTC-USDT": engine.FailedSource},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("失败列映射不符：\n got=%v\nwant=%v", got, want)
	}
}
