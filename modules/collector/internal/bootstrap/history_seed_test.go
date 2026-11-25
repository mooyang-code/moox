package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/stretchr/testify/require"
)

type fakeDatasets map[string]storagesource.DatasetInfo

func (f fakeDatasets) GetDataset(_ context.Context, _, datasetID string) (storagesource.DatasetInfo, error) {
	return f[datasetID], nil
}

// 深度取 min(保留期 − 1 天, 行情源最长历史, max_depth)。
func TestHistorySeedDepth(t *testing.T) {
	cfg := Default()
	datasets := fakeDatasets{
		"short":   {Retention: "840h"},    // 35 天
		"forever": {Retention: "forever"}, // 不限
		"medium":  {Retention: "7200h"},   // 300 天
		"unknown": {},                     // Storage 没解析出保留期
	}
	options := historySeedOptions(cfg, datasets, &fetchplan.Reconcilers{})
	day := 24 * time.Hour
	ctx := context.Background()
	depth := func(space, dataset string) time.Duration {
		got, err := options.Depth(ctx, space, dataset, "5m")
		require.NoError(t, err)
		return got
	}
	require.Equal(t, 34*day, depth("crypto", "short"), "35 天保留期留 1 天余量")
	require.Equal(t, 299*day, depth("stockus", "medium"))
	require.Equal(t, 365*day, depth("crypto", "forever"), "不限保留期用 max_depth")
	require.Equal(t, 365*day, depth("crypto", "unknown"))
	require.Equal(t, 28*day, depth("stockhk", "medium"), "港股行情源只有约 28 天")
	require.True(t, options.Enabled)
	require.True(t, options.MergeSessions)
	require.Equal(t, float64(seedDefaultRowsPerSecond), options.RowsPerSecond)
	require.Equal(t, float64(seedDefaultLocalRowsPerSecond), options.LocalRowsPerSecond)
}

func TestHistorySeedConfigOverridesAndValidation(t *testing.T) {
	disabled := false
	cfg := Default()
	cfg.HistorySeed = HistorySeedConfig{Enabled: &disabled, MaxDepth: "90d", DepthCaps: map[string]string{"stockus": "30d"}, MaxRowsPerSecond: 500, MergeSessions: &disabled}
	require.NoError(t, cfg.validateHistorySeed())
	options := historySeedOptions(cfg, fakeDatasets{"d": {Retention: "forever"}}, &fetchplan.Reconcilers{})
	require.False(t, options.Enabled)
	require.False(t, options.MergeSessions)
	require.Equal(t, float64(500), options.RowsPerSecond)
	day := 24 * time.Hour
	got, err := options.Depth(context.Background(), "stockus", "d", "5m")
	require.NoError(t, err)
	require.Equal(t, 30*day, got, "配置覆盖空间的行情源深度上限")
	got, _ = options.Depth(context.Background(), "crypto", "d", "5m")
	require.Equal(t, 90*day, got)

	for _, bad := range []HistorySeedConfig{{MaxDepth: "abc"}, {MaxDepth: "forever"}, {DepthCaps: map[string]string{"x": "0d"}}, {SubjectsPerJob: -1}, {MaxRowsPerSecond: -1}} {
		cfg := Default()
		cfg.HistorySeed = bad
		require.Error(t, cfg.validateHistorySeed(), "%+v", bad)
	}
}

// 补齐只用写入直达存储机的云函数地域：入口主机是存储机的算，经海外机转发的（crypto 常配的香港）不算。
func TestHistorySeedRegionsOnlyUseDirectEntries(t *testing.T) {
	cfg := Default()
	cfg.SCFEntry.Host = "storage"
	cfg.SCFPlan.EntryRoutes = map[string]SCFEntryEndpoint{
		"ap-hongkong":  {Address: "172.19.32.13:11004", Host: "overseas"},
		"ap-singapore": {Address: "146.56.196.204:11004", Host: "storage"},
		"ap-tokyo":     {Address: "146.56.196.204:11004", Host: "storage"},
	}
	reconcilers := &fetchplan.Reconcilers{}
	reconcilers.Store("crypto", &fetchplan.PlanReconciler{SpaceID: "crypto", Options: fetchplan.PlanOptions{
		Markets: map[string][]string{"spot": {"ap-hongkong", "ap-singapore", "ap-tokyo"}, "swap": {"ap-hongkong"}},
	}})
	cfg.SCFMarkets = map[string]map[string][]string{"crypto": {"spot": {"ap-hongkong", "ap-singapore", "ap-tokyo"}, "swap": {"ap-hongkong"}}}
	options := historySeedOptions(cfg, fakeDatasets{}, reconcilers)

	require.Equal(t, []string{"ap-singapore", "ap-tokyo"}, options.Regions("crypto", "spot"), "香港的入口是海外机，不算")
	require.Empty(t, options.Regions("crypto", "swap"), "只配了香港：没有直达地域，补齐走本地执行端")
	require.Empty(t, options.Regions("stockus", "equity"), "没有对账器的空间没有地域")
	require.Equal(t, 2*fetchplan.DefaultBackfillReservedConcurrency, options.Slots(context.Background(), "crypto"), "名额取各市场最大的“直达地域数 × 预留并发”")
}

// 没有云函数地域的空间（没有对账器、或市场没配地域）不返回补齐并发，走空间的补采并发上限。
func TestHistorySeedSlotsNeedRegions(t *testing.T) {
	cfg := Default()
	options := historySeedOptions(cfg, fakeDatasets{}, &fetchplan.Reconcilers{})
	require.Zero(t, options.Slots(context.Background(), "crypto"))
}
