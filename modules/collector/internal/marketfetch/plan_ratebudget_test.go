package marketfetch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func rateTestFunction(marketType string, subjects ...int) *PlanFunction {
	plan := &Plan{Market: PlanMarket{Provider: "binance", MarketType: marketType}}
	for _, count := range subjects {
		copies := PlanCopy{Subjects: make([]PlanSubject, count)}
		plan.Datasets = append(plan.Datasets, PlanDataset{Copies: []PlanCopy{copies}})
	}
	return &PlanFunction{Plan: plan}
}

func TestEstimateRateBudgetSpotAndSwap(t *testing.T) {
	functions := map[string]*PlanFunction{}
	for i := 0; i < 11; i++ { // 现货：506 个标的，1m 与 1h 两个数据集
		functions["spot-"+string(rune('a'+i))] = rateTestFunction("spot", 46, 46)
	}
	functions["swap-a"] = rateTestFunction("swap", 525, 525)
	got := EstimateRateBudget(functions, 5)
	require.Len(t, got, 2)
	spot, swap := got[0], got[1]
	require.Equal(t, "spot", spot.MarketType)
	require.Equal(t, 11*92, spot.Requests)
	require.Equal(t, 11*92*2/5+1, spot.PerIPTypical) // 向上取整
	require.False(t, spot.Exceeds())
	require.Equal(t, 1050, swap.Requests)
	require.False(t, swap.Exceeds())

	// 只有一个出口 IP 时现货最坏情况超过限额的 70%。
	require.True(t, EstimateRateBudget(functions, 1)[0].Exceeds())
}
