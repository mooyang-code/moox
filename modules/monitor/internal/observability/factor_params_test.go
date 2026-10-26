package observability

import (
	"fmt"
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

// 策略的参数投递滞后指标为 1 的实例告警；为 0 的不告警；没有序列时不产生检查项。
func TestBuilderReportsStrategyParamDeliveryStale(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "stale-hot", "strategy", "strategy@control", "moox_strategy_param_delivery_stale", `{"space_id":"crypto","instance_id":"i1"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "stale-ok", "strategy", "strategy@control", "moox_strategy_param_delivery_stale", `{"space_id":"crypto","instance_id":"i2"}`, 0, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	checks := factorParamChecks(got)
	require.Equal(t, "down", checks["delivery"].Status)
	require.Contains(t, checks["delivery"].Reason, "i1")
	require.NotContains(t, checks["delivery"].Reason, "i2")

	empty, _ := openOverviewState(t, func(*gorm.DB) {})
	got, err = (Builder{Metrics: empty, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Empty(t, factorParamChecks(got))
}

func TestBuilderStrategyParamDeliveryHealthyWhenAlertsClear(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "stale-ok", "strategy", "strategy@control", "moox_strategy_param_delivery_stale", `{"space_id":"crypto","instance_id":"i1"}`, 0, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Equal(t, "healthy", factorParamChecks(got)["delivery"].Status)
}

// 目录一次最多返回 500 条序列：实例很多时排序靠后的异常实例也要被检查到。
func TestBuilderStrategyParamDeliveryReadsAllSeriesPages(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, _ := openOverviewState(t, func(db *gorm.DB) {
		for i := 0; i < 500; i++ {
			seedOverviewMetricForInstance(t, db, fmt.Sprintf("stale-%03d", i), "strategy", "strategy@control", "moox_strategy_param_delivery_stale",
				fmt.Sprintf(`{"space_id":"crypto","instance_id":"i%03d"}`, i), 0, now)
		}
		seedOverviewMetricForInstance(t, db, "stale-zzz", "strategy", "strategy@control", "moox_strategy_param_delivery_stale",
			`{"space_id":"crypto","instance_id":"last"}`, 1, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	checks := factorParamChecks(got)
	require.Equal(t, "down", checks["delivery"].Status)
	require.Contains(t, checks["delivery"].Reason, "last")
}
