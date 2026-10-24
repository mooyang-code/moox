package bootstrap

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Retain 返回 true 的检查不清理，即使它长期处于“不再检查”。
func TestPruneStaleChecksKeepsRetainedChecks(t *testing.T) {
	f := newStaleFixture(t)
	for _, id := range []string{"kline_freshness:stockcn:view_off:1m", "kline_freshness:crypto:view_gone:1m"} {
		f.addCheck(id, true, domain.CheckSourceObservability, "business")
		f.addResult(id, domain.CheckReasonRetired, 3*time.Hour)
		f.addResult(id, domain.CheckReasonRetired, time.Minute)
	}
	pruned := f.prune(staleCheckPolicy{Retain: func(check *domain.Check) bool { return check.CheckID == "kline_freshness:stockcn:view_off:1m" }})
	require.Equal(t, []string{"kline_freshness:crypto:view_gone:1m"}, pruned)
	require.True(t, f.exists("kline_freshness:stockcn:view_off:1m"))
}

// K 线检查是否保留取决于评估器：还没评估过时全部保留；评估之后只保留规则存在但关闭监控的；没有评估器、非 K 线检查不保留。
func TestRuntimeRecreatedCheck(t *testing.T) {
	off := &domain.Check{CheckID: "kline_freshness:stockcn:view_off:1m"}
	gone := &domain.Check{CheckID: "kline_freshness:crypto:view_gone:1m"}
	dataset := &domain.Check{CheckID: "dataset:storage_view:view_x:1m"}

	require.False(t, (*Runtime)(nil).recreatedCheck(off))
	require.False(t, (&Runtime{}).recreatedCheck(off), "没有评估器时 K 线检查不会被补建")

	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	rule := monmetrics.KlineFreshnessRule{Enabled: false, SpaceID: "stockcn", DatasetID: "dataset", ViewID: "view_off", Frequency: "1m", MarketID: "stockcn"}
	require.Equal(t, "kline_freshness:stockcn:view_off:1m", monmetrics.KlineFreshnessCheckID(rule))
	evaluator := monmetrics.NewKlineFreshnessEvaluator(query, []monmetrics.KlineFreshnessRule{rule}, 20)
	runtime := &Runtime{klineFreshness: evaluator}
	require.True(t, runtime.recreatedCheck(off), "评估之前保守保留")
	require.True(t, runtime.recreatedCheck(gone), "评估之前保守保留")
	require.False(t, runtime.recreatedCheck(dataset), "非 K 线检查不受影响")

	_, err = evaluator.Evaluate(t.Context(), time.Now().UTC())
	require.NoError(t, err)
	require.True(t, runtime.recreatedCheck(off), "规则存在但关闭监控：会被补建，保留")
	require.False(t, runtime.recreatedCheck(gone), "规则已不存在：不会被补建，可以清理")
}
