package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

func crossValues(item ItemResult) map[string]any {
	values := map[string]any{}
	for _, row := range item.Rows {
		values[row[2].(string)] = row[3]
	}
	return values
}

// cs_ref 的参考标的本期有数时广播到截面每一行；参考标的本期缺数（上游失败、不在可用标的里）时整个截面为 NaN，
// 写成 null，不影响其他标的是否出行。
func TestComputeReferenceSeries(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := domain.MustDefinition("rel_btc", "rel_btc", "output: close - cs_ref(close, \"BTC\")\n")
	factor.AllowPartialUniverse = true
	require.Equal(t, domain.FactorTypeCrossSection, factor.FactorType)

	plan := computePlan(target, []domain.FactorDef{factor}, []string{"BTC", "ETH", "SOL"})
	computation, err := newComputeRunner(Config{}).Compute(context.Background(), plan, framesForBars(plan.Available, target, 1))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"BTC": 0.0, "ETH": 100.0, "SOL": 200.0}, crossValues(computation.Results[factor.FactorID][""]))

	missing := computePlan(target, []domain.FactorDef{factor}, []string{"BTC", "ETH", "SOL"})
	loaded := framesForBars([]string{"ETH", "SOL"}, target, 1)
	loaded.FailedSubjects = []string{"BTC"}
	computation, err = newComputeRunner(Config{}).Compute(context.Background(), missing, loaded)
	require.NoError(t, err)
	require.Equal(t, "degraded", computation.FactorStates[factor.FactorID].Status)
	require.Equal(t, map[string]any{"ETH": nil, "SOL": nil}, crossValues(computation.Results[factor.FactorID][""]),
		"参考标的缺数时整个截面为 null")
}
