package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

// 截面回归奇异：本期整截面为 NaN（结果行照常写出 null），成员状态不变；原因与截面数经 Computation、Outcome 传给引擎。
// 只有依赖奇异节点的成员有告警。
func TestComputeReportsCrossSectionWarnings(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	singular := domain.MustDefinition("singular", "singular", "output: cs_resid(close, close, close * 2)\n")
	singular.AllowPartialUniverse = true
	plain := testPipelineFactor("rank", domain.FactorTypeCrossSection, 1, true)
	plan := computePlan(target, []domain.FactorDef{singular, plain}, []string{"BTC", "ETH", "SOL", "XRP"})
	loaded := framesForBars(plan.Available, target, 1)

	computation, err := newComputeRunner(Config{}).Compute(context.Background(), plan, loaded)

	require.NoError(t, err)
	require.Equal(t, map[string]map[string]int{"singular": {"cs_resid_singular": 1}}, computation.Warnings)
	require.Equal(t, "complete", computation.FactorStates["singular"].Status, "告警不改变成员状态")
	item := computation.Results["singular"][""]
	require.Len(t, item.Rows, 4)
	for _, row := range item.Rows {
		require.Nil(t, row[len(row)-1], "奇异截面的结果为 null")
	}
	outcome := outcomeFor(plan, loaded, computation)
	require.Equal(t, computation.Warnings, outcome.Warnings)
	require.Equal(t, "complete", outcome.Status)
}
