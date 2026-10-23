package observability

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func factorParamChecks(got Overview) map[string]BusinessStatus {
	out := map[string]BusinessStatus{}
	for _, check := range got.BusinessChecks {
		if check.Kind == "factor_params" {
			out[check.Module] = check
		}
	}
	return out
}

// 成员连续沿用参数超过阈值告警（指标为 1），离线冻结只在最近 30 分钟内告警；没有序列时不产生检查项。
func TestBuilderReportsFactorParamCarryAndOfflineFreeze(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "carry-hot", "factor", "factor-mgr@control", "moox_factor_param_carry_alert", `{"space_id":"crypto","set_id":"fset_a","member":"fast"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "carry-ok", "factor", "factor-mgr@control", "moox_factor_param_carry_alert", `{"space_id":"crypto","set_id":"fset_a","member":"slow"}`, 0, now)
		seedOverviewMetricForInstance(t, db, "off-recent", "factor", "factor-mgr@control", "moox_factor_param_offline_freeze_timestamp_seconds", `{"space_id":"crypto","set_id":"fset_a"}`, float64(now.Add(-5*time.Minute).Unix()), now)
		seedOverviewMetricForInstance(t, db, "off-old", "factor", "factor-mgr@control", "moox_factor_param_offline_freeze_timestamp_seconds", `{"space_id":"crypto","set_id":"fset_b"}`, float64(now.Add(-2*time.Hour).Unix()), now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	checks := factorParamChecks(got)
	require.Equal(t, "down", checks["carry"].Status)
	require.Contains(t, checks["carry"].Reason, "fset_a/fast")
	require.NotContains(t, checks["carry"].Reason, "fset_a/slow")
	require.Equal(t, "down", checks["offline_freeze"].Status)
	require.Contains(t, checks["offline_freeze"].Reason, "fset_a")
	require.NotContains(t, checks["offline_freeze"].Reason, "fset_b")

	empty, _ := openOverviewState(t, func(*gorm.DB) {})
	got, err = (Builder{Metrics: empty, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Empty(t, factorParamChecks(got))
}

func TestBuilderFactorParamsHealthyWhenAlertsClear(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "carry-ok", "factor", "factor-mgr@control", "moox_factor_param_carry_alert", `{"space_id":"crypto","set_id":"fset_a","member":"fast"}`, 0, now)
		seedOverviewMetricForInstance(t, db, "off-none", "factor", "factor-mgr@control", "moox_factor_param_offline_freeze_timestamp_seconds", `{"space_id":"crypto","set_id":"fset_a"}`, 0, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	checks := factorParamChecks(got)
	require.Equal(t, "healthy", checks["carry"].Status)
	require.Equal(t, "healthy", checks["offline_freeze"].Status)
}
