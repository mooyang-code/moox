package command

import (
	"strconv"
	"testing"

	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/stretchr/testify/require"
)

func lookbackPlan(spaceID, freq string, window int) setupFactorPlan {
	// 回看按参数上限推导：上限就是 window。
	spec := "params:\n  window: {type: int, min: 2, max: " + strconv.Itoa(window) + ", default: 2}\noutputs:\n  bias: close / ts_mean(close, window)\n"
	taskID := "ftask_prices_" + freq
	return setupFactorPlan{
		Sets:        []setupFactorTask{{TaskID: taskID, SpaceID: spaceID, SourceDatasetID: "dataset_prices", Freq: freq}},
		Definitions: []setupFactorDefinition{{FactorID: "bias", Name: "Bias", Spec: spec}},
		Members:     []setupFactorMember{{TaskID: taskID, FactorID: "bias", Status: "enabled"}},
	}
}

// 保留期必须大于因子回看根数：crypto（7×24，精确）装不下报错并给出至少要调到多少；装得下无提示；永久保留不检查。
func TestFactorRetentionFindingsCrypto(t *testing.T) {
	policy := storagepolicy.Default() // crypto 1h = 420d = 10080 根

	errs, warnings := factorRetentionFindings(policy, lookbackPlan("crypto", "1h", 200))
	require.Empty(t, errs)
	require.Empty(t, warnings)

	policy.Retention.Spaces = map[string]map[string]string{"crypto": {"1h": "100h"}}
	errs, warnings = factorRetentionFindings(policy, lookbackPlan("crypto", "1h", 200))
	require.Empty(t, warnings)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0], "ftask_prices_1h")
	require.Contains(t, errs[0], "回看 200 根")
	require.Contains(t, errs[0], "约 100 根")
	require.Contains(t, errs[0], "storage_retention.spaces.crypto.1h")
	require.Contains(t, errs[0], "至少调到 201h", "回看 200 根要装下 201 根")

	// 刚好相等也不够：窗口里还要有当前这一根。
	policy.Retention.Spaces = map[string]map[string]string{"crypto": {"1h": "200h"}}
	errs, _ = factorRetentionFindings(policy, lookbackPlan("crypto", "1h", 200))
	require.Len(t, errs, 1)

	policy.Retention.Spaces = map[string]map[string]string{"crypto": {"1h": "forever"}}
	errs, warnings = factorRetentionFindings(policy, lookbackPlan("crypto", "1h", 200))
	require.Empty(t, errs)
	require.Empty(t, warnings)
}

// 交易时段市场按日历估算，装不下只提醒不报错；停用的成员不参与；表达式不合法的由导入时报错，这里跳过。
func TestFactorRetentionFindingsSessionMarketAndDisabledMembers(t *testing.T) {
	policy := storagepolicy.Default()
	policy.Retention.Spaces = map[string]map[string]string{"stockcn": {"1m": "24h"}}
	errs, warnings := factorRetentionFindings(policy, lookbackPlan("stockcn", "1m", 500))
	require.Empty(t, errs)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "仅作提醒")

	plan := lookbackPlan("stockcn", "1m", 500)
	plan.Members[0].Status = "disabled"
	errs, warnings = factorRetentionFindings(policy, plan)
	require.Empty(t, errs)
	require.Empty(t, warnings)

	plan = lookbackPlan("crypto", "1h", 200)
	plan.Definitions[0].Spec = "outputs: {"
	policy.Retention.Spaces = map[string]map[string]string{"crypto": {"1h": "10h"}}
	errs, warnings = factorRetentionFindings(policy, plan)
	require.Empty(t, errs)
	require.Empty(t, warnings)
}
