package bootstrap

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	"github.com/stretchr/testify/require"
)

// 各空间的采集计划默认值挪进了 market.yaml：取值必须与原来写在代码里的逐项相同。
func TestSCFPlanSpaceDefaultsComeFromMarketFiles(t *testing.T) {
	require.Equal(t, map[string]SCFPlanSpaceConfig{
		"stockcn": {
			EgressBalance:   &SCFPlanEgressBalance{MaxPerIP: 1, Rounds: 5, IdleStart: "15:10", IdleEnd: "07:50", Timezone: "Asia/Shanghai"},
			AutoSize:        &SCFPlanAutoSize{TargetFetchMS: 2000, RequestMS: 500, MinFunctions: 8},
			DefaultIngest:   "pull",
			DefaultExecutor: "scf",
			Fetch:           &SCFPlanFetchConfig{RequestTimeoutMS: 3000, StorageTimeoutMS: 2000, NotBeforeMS: 800},
		},
		// 港股：固定每函数 25 只，不估算函数个数、不做出口 IP 去重，收盘后等 5 秒。
		"stockhk": {
			SymbolsPerFunction: 25,
			DefaultIngest:      "pull",
			DefaultExecutor:    "scf",
			Fetch:              &SCFPlanFetchConfig{RequestTimeoutMS: 3000, StorageTimeoutMS: 2000, NotBeforeMS: 5000},
		},
		"crypto": {
			DefaultIngest: "stream", DefaultExecutor: "local",
			Markets: map[string]SCFPlanMarketConfig{
				"spot": {Fetch: &SCFPlanFetchConfig{NotBeforeMS: 3000}},
				"swap": {Fetch: &SCFPlanFetchConfig{NotBeforeMS: 8000}, LatenessS: 20},
			},
		},
	}, defaultSCFPlan().Spaces)
}

// 允许迟到时间：crypto 15 秒、stockcn 90 秒；未登记的空间报错，不再给 60 秒的默认值。
func TestDefaultLatenessSecondsComesFromMarketFiles(t *testing.T) {
	for space, want := range map[string]int{"crypto": 15, "stockcn": 90, "stockhk": 90} {
		got, err := defaultLatenessSeconds(space)
		require.NoError(t, err)
		require.Equal(t, want, got, space)
	}
	_, err := defaultLatenessSeconds("no_such_space")
	require.ErrorIs(t, err, markets.ErrUnknownMarket)
}

// 标的属性同步的默认任务取各市场 market.yaml 的 subject_sync，内容与原来写在代码里的相同。
func TestSubjectSyncDefaultsComeFromMarketFiles(t *testing.T) {
	require.ElementsMatch(t, []subjectsync.AttributeJob{
		{SpaceID: "stockcn", Sources: []string{"eastmoney"}, InstrumentType: "equity", Cron: "10 8 * * *", Timezone: "Asia/Shanghai"},
		{SpaceID: "crypto", Sources: []string{"binance"}, InstrumentType: "spot", Cron: "10 8 * * *", Timezone: "Asia/Shanghai"},
	}, subjectsync.DefaultConfig().Attributes)
}
