package marketfetch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func symbolTestPlan(exceptions map[string]string) *Plan {
	return &Plan{
		SymbolExceptions: exceptions,
		Datasets:         []PlanDataset{{DatasetID: "ds", Frequency: "1m", Copies: []PlanCopy{{From: 0, Subjects: []PlanSubject{{ID: "AAA-USDT"}}}}}},
	}
}

func TestSymbolAtUsesOldMappingBeforePAndNewMappingFromP(t *testing.T) {
	p := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	content := &FetchPlanContent{Datasets: map[string]*PlanDatasetChange{"ds|1m": {Changed: true, P: p.Unix()}}}
	function := &PlanFunctionChange{Old: symbolTestPlan(map[string]string{"AAA-USDT": "OLDSYM"}), New: symbolTestPlan(map[string]string{"AAA-USDT": "NEWSYM"})}

	symbol, ok := content.SymbolAt(function, "ds|1m", "AAA-USDT", p.Add(-time.Minute))
	require.True(t, ok)
	require.Equal(t, "OLDSYM", symbol)
	// at == P 属于新的一份。
	symbol, ok = content.SymbolAt(function, "ds|1m", "AAA-USDT", p)
	require.True(t, ok)
	require.Equal(t, "NEWSYM", symbol)

	// 数据集没有变更、或新旧计划缺一方：不覆盖，沿用计划自带的映射。
	_, ok = content.SymbolAt(function, "other|1m", "AAA-USDT", p)
	require.False(t, ok)
	_, ok = content.SymbolAt(&PlanFunctionChange{New: function.New}, "ds|1m", "AAA-USDT", p)
	require.False(t, ok)
}
