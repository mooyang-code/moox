package bootstrap

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// 沿用超过阈值导出 1，恢复（引擎不再列出该成员）后清零；离线冻结时间按集合取成员里最近的一次。
func TestObserveParamsExportsCarryAlertAndOfflineFreeze(t *testing.T) {
	registry := prometheus.NewRegistry()
	observer := &factorDatasetObserver{}
	observer.carryAlert, observer.offlineFreeze = newParamGauges(registry)
	keys := map[string]report.DatasetKey{"fset_a": {SpaceID: "crypto", DatasetID: "dataset_factor_a", Freq: "1m"}}
	first := time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)
	observer.observeParams(keys, domain.EngineStatus{ParamHealth: []domain.MemberParamHealth{
		{SetID: "fset_a", Member: "fast", CarriedPeriods: 11, AlertAfter: 10},
		{SetID: "fset_a", Member: "slow", CarriedPeriods: 3, AlertAfter: 10, LastOfflineAt: first},
		{SetID: "fset_unknown", Member: "x", CarriedPeriods: 99, AlertAfter: 10},
	}})
	require.Equal(t, 1.0, testutil.ToFloat64(observer.carryAlert.WithLabelValues("crypto", "fset_a", "fast")))
	require.Equal(t, 0.0, testutil.ToFloat64(observer.carryAlert.WithLabelValues("crypto", "fset_a", "slow")))
	require.Equal(t, float64(first.Unix()), testutil.ToFloat64(observer.offlineFreeze.WithLabelValues("crypto", "fset_a")))
	require.Equal(t, 2, testutil.CollectAndCount(observer.carryAlert), "未知集合不导出")

	observer.observeParams(keys, domain.EngineStatus{})
	require.Equal(t, 0.0, testutil.ToFloat64(observer.carryAlert.WithLabelValues("crypto", "fset_a", "fast")), "恢复后清零")
	require.Equal(t, 0.0, testutil.ToFloat64(observer.offlineFreeze.WithLabelValues("crypto", "fset_a")))
}

// 参数状态与成员告警阈值随心跳和目录快照往返。
func TestParamHealthSurvivesWireRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)
	status := domain.EngineStatus{OfflinePending: 3, ParamHealth: []domain.MemberParamHealth{{SetID: "fset_a", Member: "fast", CarriedPeriods: 4, AlertAfter: 10, LastOfflineAt: at}}}
	got, err := factorwire.EngineStatusFromPB(factorwire.EngineStatusToPB(status))
	require.NoError(t, err)
	require.Equal(t, 3, got.OfflinePending)
	require.Equal(t, status.ParamHealth, got.ParamHealth)

	def := domain.MustDefinition("bias", "bias", "outputs:\n  bias: close\n")
	set := domain.EngineSet{Set: domain.FactorSet{SetID: "fset_a"}, Members: []domain.MemberDef{{Alias: "fast", Factor: def, CarryAlertPeriods: 25}}}
	restored, err := factorwire.EngineSetFromPB(factorwire.EngineSetToPB(set))
	require.NoError(t, err)
	require.Equal(t, 25, restored.Members[0].CarryAlertPeriods)
	require.Equal(t, domain.DefaultCarryAlertPeriods, domain.MemberDef{}.CarryAlertAfter())
}
