package engine

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanMemoryFollowsDesignFormula(t *testing.T) {
	plan := PlanMemory(32*gib, 16, 4, 512<<20)
	require.Equal(t, 32*gib*6/10, plan.TotalBudget)
	require.Equal(t, (plan.TotalBudget-512<<20)*4/10, plan.Window)
	headroom := plan.TotalBudget - plan.Window - plan.BlockCache
	require.Equal(t, headroom/2/4, plan.Compute, "并行数取任务数与核数的较小值")
	require.Empty(t, plan.Warnings)
}

func TestPlanMemoryWarnsOnSmallMachine(t *testing.T) {
	plan := PlanMemory(2*gib, 2, 4, 512<<20)
	require.Equal(t, gib, plan.Compute, "单次计算上限不低于 1GiB")
	require.NotEmpty(t, plan.Warnings, "预算扣除常驻后不足 1GiB 要警告")
}

func TestConfigValueRoundsToMiB(t *testing.T) {
	require.Equal(t, "4GiB", ConfigValue(4*gib))
	require.Equal(t, "1536MiB", ConfigValue(gib+gib/2+12345))
}

// 建议值按 moox.toml 的覆盖表打印，键路径是引擎配置里已有的三项。
func TestOverridesPrintMemoryPlanAsTopologyTable(t *testing.T) {
	plan := PlanMemory(32*gib, 16, 4, 512<<20)
	require.Equal(t, "[overrides.\"factor-engine\".\"engine.yaml\"]\n"+
		"\"memory.total_budget\" = \""+ConfigValue(plan.TotalBudget)+"\"\n"+
		"\"window.max_memory\" = \""+ConfigValue(plan.Window)+"\"\n"+
		"\"compute.max_memory\" = \""+ConfigValue(plan.Compute)+"\"\n", plan.Overrides())
}

func TestWindowEstimateMatchesDocExamples(t *testing.T) {
	// 设计 4.2 / 8.1：2000 个序列、N = 1 万、7 个数值字段约 1.4GB；N = 5 万约 7.1GB。
	gb := func(n int64) float64 { return float64(n) / 1e9 }
	require.InDelta(t, 1.425, gb(WindowEstimate(2000, 10000, 7, 0)), 0.001)
	require.InDelta(t, 7.125, gb(WindowEstimate(2000, 50000, 7, 0)), 0.001)
	require.Equal(t, int64(10)*int64(40+16)*int64(2*8+1), WindowEstimate(10, 40, 2, 0), "余量至少 16 根")

	plan := PlanMemory(16<<30, 8, 4, 512<<20)
	require.Empty(t, plan.CheckWindowEstimate(plan.Window))
	require.Contains(t, plan.CheckWindowEstimate(plan.Window+1), "超过建议的窗口上限")
}
