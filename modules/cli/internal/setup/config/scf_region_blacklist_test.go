package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSCFRegionBlacklistAllocation(t *testing.T) {
	cfg := SCFFetcherSpace{SpaceID: "crypto", TimerFunctionCount: 44, RegionBlacklist: []string{" AP-GUANGZHOU ", "ap-guangzhou"}, Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true}, {Region: "ap-singapore", Enabled: true}}}
	require.NoError(t, resolveSCFTimerFunctionCounts(&cfg, "space"))
	require.Equal(t, []string{"ap-guangzhou"}, cfg.RegionBlacklist)
	require.Zero(t, cfg.Regions[0].FunctionCount)
	require.Equal(t, 44, cfg.Regions[1].FunctionCount)
	require.Equal(t, 44, cfg.TimerFunctionCount)
}

func TestSCFRegionBlacklistRejectsConflictingCapacity(t *testing.T) {
	for _, count := range []int{1, 60} {
		cfg := SCFFetcherSpace{SpaceID: "crypto", TimerFunctionCount: count, RegionBlacklist: []string{"ap-guangzhou"}, Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true}, {Region: "ap-singapore", Enabled: true}}}
		if count == 1 {
			cfg.Regions[0].FunctionCount = 1
		}
		require.Error(t, resolveSCFTimerFunctionCounts(&cfg, "space"))
	}
	for _, region := range []string{"", "not-a-region"} {
		cfg := SCFFetcherSpace{SpaceID: "crypto", RegionBlacklist: []string{region}}
		require.ErrorContains(t, resolveSCFTimerFunctionCounts(&cfg, "space"), "region_blacklist")
	}
}

func TestSCFRegionBlacklistPreservesExplicitCapacityAndSpaceIsolation(t *testing.T) {
	cfg := SCFFetcherSpace{SpaceID: "crypto", TimerFunctionCount: 58, RegionBlacklist: []string{"ap-guangzhou"}, Regions: []SCFFetcherRegion{{Region: "ap-guangzhou"}, {Region: "ap-singapore", Enabled: true, FunctionCount: 50}, {Region: "ap-tokyo", Enabled: true, FunctionCount: 8}}}
	require.NoError(t, resolveSCFTimerFunctionCounts(&cfg, "crypto"))
	require.Equal(t, 58, cfg.TimerFunctionCount)
	stock := SCFFetcherSpace{SpaceID: "stockcn", TimerFunctionCount: 10, Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 10}}}
	require.NoError(t, resolveSCFTimerFunctionCounts(&stock, "stockcn"))
	require.False(t, stock.IsRegionBlacklisted("ap-guangzhou"))
	cfg.InstrumentSnapshotRegion = "ap-guangzhou"
	require.ErrorContains(t, resolveSCFTimerFunctionCounts(&cfg, "crypto"), "instrument_snapshot_region")
	cfg.InstrumentSnapshotRegion = ""
	cfg.Regions[0].FunctionCount = 1
	require.ErrorContains(t, resolveSCFTimerFunctionCounts(&cfg, "crypto"), "blacklisted")
}

func TestRenderCollectorRegionBlacklists(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{SCFFetcher: SCFFetcher{Spaces: []SCFFetcherSpace{{SpaceID: "crypto", RegionBlacklist: []string{"ap-guangzhou"}}, {SpaceID: "stockcn"}}}}}
	raw, err := RenderCollectorDNSResolverConfig(snapshot, []byte("database:\n  path: ../data/collector/moox_collector.db\nscf_region_blacklists:\n  old: [ap-beijing]\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &got))
	require.Equal(t, map[string]any{"crypto": []any{"ap-guangzhou"}, "stockcn": []any{}}, got["scf_region_blacklists"])
	require.Equal(t, "../data/collector/moox_collector.db", got["database"].(map[string]any)["path"])
}
