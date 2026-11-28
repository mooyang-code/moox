package fetchplan

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
)

// 补采的路由由当前任务推出，与对账器为同一数据集生成的函数计划一致：市场身份（含来源，决定写行的 series_tag）、
// 拉取参数、输出字段都相同。任务删掉、频率不匹配时找不到路由。
func TestBackfillRouteMatchesThePlanOfTheTask(t *testing.T) {
	h := newPlanHarness(t, 3)
	h.tasks.tasks[0].CollectParams = `{"frequency":"1m","output_fields":["close","open","volume"]}`
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
		CollectParams: `{"frequency":"1m"}`, ResultDatasetID: "ds_spot",
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
