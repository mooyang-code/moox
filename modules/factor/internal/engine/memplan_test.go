package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanMemoryFollowsDesignFormula(t *testing.T) {
	plan := PlanMemory(32*gib, 16, 4, 512<<20)
	require.Equal(t, 32*gib*6/10, plan.TotalBudget)
	require.Equal(t, (plan.TotalBudget-512<<20)*4/10, plan.Window)
	headroom := plan.TotalBudget - plan.Window - plan.BlockCache
	require.Equal(t, headroom/2/4, plan.Compute, "并行数取集合数与核数的较小值")
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

func TestTuneConfigWritesOnlyUnsetValuesAndKeepsComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.yaml")
	original := "# 引擎配置\ncompute:\n  workers: 4\n  max_memory: 1GiB   # 单次计算上限\nwindow:\n  max_memory: 0\nmemory:\n  total_budget: 0\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
	plan := PlanMemory(32*gib, 16, 4, 512<<20)

	changes, err := TuneConfig(path, plan, false)
	require.NoError(t, err)
	require.Len(t, changes, 3)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "# 引擎配置")
	require.Contains(t, string(raw), "# 单次计算上限")
	cfg, err := Load(path)
	if err == nil {
		require.Equal(t, ByteSize(plan.TotalBudget>>20<<20), cfg.Memory.TotalBudget)
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// 已经设置过的值再次调优不动；用户改过的值也不覆盖。
	changes, err = TuneConfig(path, plan, false)
	require.NoError(t, err)
	require.Empty(t, changes)
	changes, err = TuneConfig(path, PlanMemory(64*gib, 16, 4, 512<<20), true)
	require.NoError(t, err)
	require.Len(t, changes, 3, "force 覆盖已有值")
}
