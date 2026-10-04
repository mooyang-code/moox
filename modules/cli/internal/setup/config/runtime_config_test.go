package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRenderTradeDNSResolverConfigKeepsUnrelatedSettings(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{
		DNSResolver: DNSResolver{
			Enabled:         true,
			Domains:         []string{"FAPI.BINANCE.COM."},
			LookupTimeoutMS: 1500,
			ProbeTimeoutMS:  500,
			ProbePort:       443,
			CacheTTLSeconds: 300,
			MaxIPsPerDomain: 4,
		},
	}}
	rendered, err := RenderTradeDNSResolverConfig(snapshot, []byte(`database:
  path: ./trade.db
admin:
  service_auth:
    secret_key: do-not-copy
eventbus:
  enabled: true
dns_resolver:
  enabled: true
  domains: [old.example]
`))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	require.Equal(t, "./trade.db", got["database"].(map[string]any)["path"])
	require.Equal(t, "do-not-copy", got["admin"].(map[string]any)["service_auth"].(map[string]any)["secret_key"])
	resolver := got["dns_resolver"].(map[string]any)
	require.Equal(t, true, resolver["enabled"])
	require.Equal(t, []any{"fapi.binance.com"}, resolver["domains"])
	require.NotContains(t, string(rendered), "trade_node")
}

func TestRenderTradeDNSResolverConfigDisablesNonResolverNode(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{DNSResolver: DNSResolver{
		Enabled:   true,
		TradeNode: "compute-1",
		Domains:   []string{"fapi.binance.com"},
	}}}
	rendered, err := RenderTradeDNSResolverConfigForNode(snapshot, "control", nil)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	resolver := got["dns_resolver"].(map[string]any)
	require.Equal(t, false, resolver["enabled"])
	require.Equal(t, []any{}, resolver["domains"])
}

func TestRenderCollectorDNSResolverConfigDerivesTradeTarget(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{
		DNSResolver: DNSResolver{
			Enabled:                true,
			TradeNode:              "compute-1",
			RefreshIntervalSeconds: 300,
			RequestTimeoutMS:       3000,
			CacheTTLSeconds:        300,
			Domains:                []string{"fapi.binance.com"},
		},
		OtherHosts: []Host{{Name: "compute-1", Address: "43.132.204.177"}},
	}}
	rendered, err := RenderCollectorDNSResolverConfig(snapshot, []byte("database:\n  path: ./collector.db\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	resolver := got["dns_resolver"].(map[string]any)
	require.Equal(t, "ip://43.132.204.177:11003", resolver["target"])
	require.Equal(t, "compute-1", resolver["node_id"])
	require.Equal(t, "300s", resolver["refresh_interval"])
	require.Equal(t, "3000ms", resolver["request_timeout"])
	require.NotContains(t, string(rendered), "secret")
}

func TestRenderCollectorDNSResolverConfigIncludesStockCapacity(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{
		SCFFetcher: SCFFetcher{Spaces: []SCFFetcherSpace{{
			SpaceID: "stockcn", TimerFunctionCount: 200, MeasuredSafeGroupSize: 30,
			StaggerStartSecond: DefaultStockCNStaggerStartSecond, StaggerWindowSeconds: DefaultStockCNStaggerWindowSeconds, StaggerMaxStartsPerSecond: DefaultStockCNStaggerMaxStartsPerSecond,
		}}},
	}}
	rendered, err := RenderCollectorDNSResolverConfig(snapshot, []byte("database:\n  path: ./collector.db\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	stock := got["stockcn"].(map[string]any)
	require.Equal(t, 200, stock["expected_timer_function_count"])
	require.Equal(t, 30, stock["measured_safe_group_size"])
	require.Equal(t, 5, stock["stagger_start_second"])
	require.Equal(t, 35, stock["stagger_window_seconds"])
	require.Equal(t, 6, stock["stagger_max_starts_per_second"])
}

func TestRenderCollectorDNSResolverConfigIncludesRetentionPolicy(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{CollectorRetention: CollectorRetention{
		MaintenanceInterval: "2m", MaintenanceTimeout: "30s", MaxRowsPerPass: 25000,
		ExecutionDetailRetention: "24h", ScheduledRunSummaryRetention: "720h",
		TerminalRetryRetention: "336h", PeriodSnapshotRetention: "720h",
	}}}
	rendered, err := RenderCollectorDNSResolverConfig(snapshot, []byte("database:\n  path: ./collector.db\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	retention := got["collector_retention"].(map[string]any)
	require.Equal(t, "2m", retention["maintenance_interval"])
	require.Equal(t, "30s", retention["maintenance_timeout"])
	require.Equal(t, 25000, retention["max_rows_per_pass"])
	require.Equal(t, "24h", retention["execution_detail_retention"])
	require.Equal(t, "720h", retention["scheduled_run_summary_retention"])
	require.Equal(t, "336h", retention["terminal_retry_retention"])
	require.Equal(t, "720h", retention["period_snapshot_retention"])
}

func TestRenderCollectorRuntimeClaimTargetDistinctFromStorage(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{SCFFetcher: SCFFetcher{Spaces: []SCFFetcherSpace{{
		SpaceID: "stockcn", CollectorRPCGatewayTarget: "ip://collector.example:11003", CollectorGatewayTargetNode: "collector-node",
		StorageRPCGatewayTarget: "ip://storage.example:11003", StorageGatewayNodeID: "storage-node",
	}}}}}
	rendered, err := RenderCollectorDNSResolverConfig(snapshot, []byte("storage:\n  gateway_target: ip://storage.example:11003\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	runtime, ok := got["collector_runtime"].(map[string]any)
	require.True(t, ok, "Collector Claim endpoint must be rendered separately")
	require.Equal(t, "ip://collector.example:11003", runtime["gateway_target"])
	require.Equal(t, "collector-node", runtime["node_id"])
}

func TestRenderCollectorRuntimeRejectsConflictsAndClearsStaleRoute(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{SCFFetcher: SCFFetcher{Spaces: []SCFFetcherSpace{
		{SpaceID: "crypto", CollectorRPCGatewayTarget: "ip://first.example:11003", CollectorGatewayTargetNode: "first"},
		{SpaceID: "stockcn", CollectorRPCGatewayTarget: "ip://second.example:11003", CollectorGatewayTargetNode: "second"},
	}}}}
	_, err := RenderCollectorDNSResolverConfig(snapshot, nil)
	require.ErrorContains(t, err, "Collector gateway")
	snapshot.Manifest.SCFFetcher.Spaces[1].CollectorRPCGatewayTarget = ""
	snapshot.Manifest.SCFFetcher.Spaces[1].CollectorGatewayTargetNode = "second"
	_, err = RenderCollectorDNSResolverConfig(snapshot, nil)
	require.ErrorContains(t, err, "configured together")
	snapshot.Manifest.SCFFetcher.Spaces = nil
	rendered, err := RenderCollectorDNSResolverConfig(snapshot, []byte("collector_runtime:\n  gateway_target: ip://stale.example:11003\n  node_id: stale\ndatabase:\n  path: keep.db\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	runtime := got["collector_runtime"].(map[string]any)
	require.Empty(t, runtime["gateway_target"])
	require.Empty(t, runtime["node_id"])
	require.Equal(t, "keep.db", got["database"].(map[string]any)["path"])
}

func TestRenderCollectorRuntimeGlobalPairIsOrderIndependent(t *testing.T) {
	configured := SCFFetcherSpace{SpaceID: "crypto", CollectorRPCGatewayTarget: "ip://collector.example:11003", CollectorGatewayTargetNode: "collector"}
	inherited := SCFFetcherSpace{SpaceID: "stockcn"}
	matching := configured
	matching.SpaceID = "stockcn"
	for _, spaces := range [][]SCFFetcherSpace{{configured, inherited}, {inherited, configured}, {configured, matching}} {
		snapshot := &Snapshot{Manifest: Manifest{SCFFetcher: SCFFetcher{Spaces: spaces}}}
		rendered, err := RenderCollectorDNSResolverConfig(snapshot, nil)
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, yaml.Unmarshal(rendered, &got))
		runtime := got["collector_runtime"].(map[string]any)
		require.Equal(t, configured.CollectorRPCGatewayTarget, runtime["gateway_target"])
		require.Equal(t, configured.CollectorGatewayTargetNode, runtime["node_id"])
	}
}

func TestRenderDisabledResolverReplacesStaleSettings(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{DNSResolver: DNSResolver{Enabled: false}}}
	rendered, err := RenderTradeDNSResolverConfig(snapshot, []byte("dns_resolver:\n  enabled: true\n  domains: [fapi.binance.com]\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	resolver := got["dns_resolver"].(map[string]any)
	require.Equal(t, false, resolver["enabled"])
	require.Equal(t, []any{}, resolver["domains"])
}

func TestWriteRenderedRuntimeConfigPreservesModeAndReplacesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trade", "app.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o600))
	require.NoError(t, WriteRenderedRuntimeConfig(path, []byte("new\n")))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "new\n", string(raw))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
