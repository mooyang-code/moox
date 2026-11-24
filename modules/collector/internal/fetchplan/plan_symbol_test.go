package fetchplan

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
)

// 标的改了交易所符号（标的集合不变）：对账器给数据集选生效周期 P、不重新登记快照，在计划建立的同一个事务里
// 写下从 P 起的新符号；时间线上 P 之前仍是旧符号，补采按它取。
func TestSymbolChangeWritesATimelineRowFromP(t *testing.T) {
	h := newPlanHarness(t, 3)
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	h.store.shadow = db.FetchPlans()

	renamed := false
	r := h.reconciler()
	r.Groups.ResolveSymbol = func(_, _, _, subjectID string) (string, error) {
		if renamed && subjectID == "S001-USDT" {
			return "S001NEW", nil
		}
		return strings.ReplaceAll(subjectID, "-", ""), nil
	}
	planBaselineSettle(t, h, r)
	registered := h.registrar.calls
	before := h.lastRecord().ID

	renamed = true
	r.lastVerified = h.now
	require.NoError(t, h.reconcile(r))
	latest := h.lastRecord()
	require.NotEqual(t, before, latest.ID, "只改符号也生成新计划")
	change := h.content(latest).Datasets["ds_spot|1m"]
	require.Equal(t, PlanActionSymbols, change.Action)
	require.False(t, change.Register)
	require.NotZero(t, change.P)
	require.Equal(t, "S001NEW", change.Symbols["S001-USDT"])

	// 计划一建立时间线就有这一行（与计划记录同一个事务），不等登记。
	entries, err := db.SeriesTimeline().ListBySpace(context.Background(), "crypto")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	timeline := klinecheck.BuildTimeline(entries)
	p := time.Unix(change.P, 0).UTC()
	symbol, ok := timeline.SymbolAt("ds_spot|1m", "S001-USDT", p.Add(-time.Minute))
	require.True(t, ok)
	require.Equal(t, "S001USDT", symbol, "P 之前是旧符号")
	symbol, ok = timeline.SymbolAt("ds_spot|1m", "S001-USDT", p)
	require.True(t, ok)
	require.Equal(t, "S001NEW", symbol, "P 起是新符号")
	symbol, _ = timeline.SymbolAt("ds_spot|1m", "S001-USDT", p.Add(-24*time.Hour))
	require.Equal(t, "S001USDT", symbol, "早于最早一行沿用最早一行的符号")

	planBaselineSettle(t, h, r)
	require.Equal(t, registered, h.registrar.calls, "符号变化不重新登记快照")
}

// 补采的路由由当前任务推出，与对账器为同一数据集生成的函数计划一致：市场身份（含来源，决定写行的 series_tag）、
// 拉取参数、输出字段都相同。任务删掉、频率不匹配时找不到路由。
func TestBackfillRouteMatchesThePlanOfTheTask(t *testing.T) {
	h := newPlanHarness(t, 3)
	h.tasks.tasks[0].CollectParams = `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"ds_spot","frequency":"1m","output_fields":["close","open","volume"]}`
	r := h.reconciler()
	planBaselineSettle(t, h, r)
	applied, err := h.store.LatestApplied(context.Background(), "crypto")
	require.NoError(t, err)
	content := h.content(applied)
	var planned *marketfetch.Plan
	for _, name := range content.SortedFunctionNames() {
		planned = content.Functions[name].Target
		break
	}
	require.NotNil(t, planned)

	routes := &BackfillRoutes{Tasks: h.tasks, Tags: h.symbols, Options: func(string) (PlanOptions, error) { return testPlanOptions(), nil }}
	route, found, err := routes.BackfillRoute(context.Background(), "crypto", "ds_spot", "1m")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, planned.Market, route.Market)
	require.Equal(t, planned.Fetch, route.Fetch)
	require.Equal(t, findPlanDataset(planned, "ds_spot|1m").OutputFields, route.Datasets[0].OutputFields)
	require.Equal(t, marketfetch.PlanSeriesTag("crypto", planned.Market), marketfetch.PlanSeriesTag("crypto", route.Market), "写行的 series_tag 与计划一致")

	_, found, err = routes.BackfillRoute(context.Background(), "crypto", "ds_spot", "5m")
	require.NoError(t, err)
	require.False(t, found, "频率不匹配")
	h.tasks.tasks = nil
	_, found, err = routes.BackfillRoute(context.Background(), "crypto", "ds_spot", "1m")
	require.NoError(t, err)
	require.False(t, found, "任务已删除")
}

// 用真实的任务仓库：ListKline 要带上任务的标签（标签存在单独的表里），补采路由才推得出来源与市场。
func TestBackfillRouteReadsTagsFromTheTaskRepository(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "spot-1m", TaskName: "spot-1m", DataType: "kline", Ingest: domain.IngestSCF, Enabled: true, TagIDs: []string{"binance_spot"},
		CollectParams: `{"target_dataset_id":"ds_spot","frequency":"1m"}`, ResultDatasetID: "ds_spot", ResultViewID: "view_spot",
	}))

	routes := &BackfillRoutes{Tasks: db.Tasks(), Tags: &planSymbolsStub{}, Options: func(string) (PlanOptions, error) { return testPlanOptions(), nil }}
	route, found, err := routes.BackfillRoute(ctx, "crypto", "ds_spot", "1m")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "binance", route.Market.Provider)
	require.Equal(t, "spot", route.Market.MarketType)

	_, found, err = routes.BackfillRoute(ctx, "crypto", "ds_other", "1m")
	require.NoError(t, err)
	require.False(t, found)
}
