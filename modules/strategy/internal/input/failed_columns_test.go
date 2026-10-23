package input

import (
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
)

// 因子失败的标的里，因回看窗口 K 线空洞失败的记 factor_gap，其余记 factor_failed；K 线 View 的失败仍是 source_failed。
func TestFailedColumnsDistinguishesGapFromFactorFailure(t *testing.T) {
	resolved := Resolved{Columns: map[string]ColumnBinding{
		"ma":    {Source: "factor", Member: "f_ma", FactorID: "f_ma"},
		"close": {Source: "view"},
	}}
	program := &dsl.Program{Columns: []string{"ma", "close"}}
	result := readiness.Result{
		FailedByFactor: map[string][]string{"f_ma": {"ETH-USDT", "SOL-USDT"}},
		GapByFactor:    map[string][]string{"f_ma": {"SOL-USDT"}},
		FailedSubjects: []string{"BTC-USDT"},
	}
	got := failedColumns(resolved, program, result)
	want := map[string]map[string]string{
		"ma":    {"ETH-USDT": engine.FailedFactor, "SOL-USDT": engine.FailedGap, "BTC-USDT": engine.FailedSource},
		"close": {"BTC-USDT": engine.FailedSource},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("失败列映射不符：\n got=%v\nwant=%v", got, want)
	}
}
