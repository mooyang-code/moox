package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRenderEgressAndCollectorSeparatePolicyFromRouting(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{EgressProxy: EgressProxy{
		HTTPDomains: []string{"*.binance.com", "data-api.binance.vision"},
		DNS:         EgressDNS{Domains: []string{"FAPI.BINANCE.COM."}, RefreshIntervalSeconds: 300, RequestTimeoutMS: 3000, LookupTimeoutMS: 1500, ProbeTimeoutMS: 500, ProbePort: 443, CacheTTLSeconds: 300, MaxIPsPerDomain: 4},
	}}}
	raw, err := RenderEgressConfig(snapshot, []byte("unrelated: keep\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &got))
	require.Equal(t, "keep", got["unrelated"])
	require.Equal(t, []any{"*.binance.com", "data-api.binance.vision"}, got["domains"])
	require.Equal(t, []any{"fapi.binance.com"}, got["dns"].(map[string]any)["domains"])
	raw, err = RenderCollectorRuntimeConfig(snapshot, []byte("database: {path: keep.db}\ndns_resolver: {target: old}\n"))
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(raw, &got))
	require.Equal(t, "keep.db", got["database"].(map[string]any)["path"])
	require.NotContains(t, got, "dns_resolver")
	dns := got["egress_proxy"].(map[string]any)["dns"].(map[string]any)
	require.Equal(t, "300s", dns["refresh_interval"])
	require.Equal(t, "3000ms", dns["request_timeout"])
	require.NotContains(t, dns, "target")
	require.NotContains(t, dns, "node_id")
}

func TestRenderCollectorRuntimeConfigIncludesStockCapacity(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{
		SCFFetcher: SCFFetcher{Spaces: []SCFFetcherSpace{{
			SpaceID: "stockcn", TimerFunctionCount: 200, MeasuredSafeGroupSize: 30,
			StaggerStartSecond: DefaultStockCNStaggerStartSecond, StaggerWindowSeconds: DefaultStockCNStaggerWindowSeconds, StaggerMaxStartsPerSecond: DefaultStockCNStaggerMaxStartsPerSecond,
		}}},
	}}
	rendered, err := RenderCollectorRuntimeConfig(snapshot, []byte("database:\n  path: ./collector.db\n"))
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

func TestRenderCollectorRuntimeConfigIncludesRetentionPolicy(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{CollectorRetention: CollectorRetention{
		MaintenanceInterval: "2m", MaintenanceOffset: "10s", MaintenanceTimeout: "30s", MaxRowsPerPass: 25000,
		ExecutionDetailRetention: "24h", ScheduledRunSummaryRetention: "720h",
		TerminalRetryRetention: "336h", PeriodSnapshotRetention: "720h",
	}}}
	rendered, err := RenderCollectorRuntimeConfig(snapshot, []byte("database:\n  path: ./collector.db\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	retention := got["collector_retention"].(map[string]any)
	require.Equal(t, "2m", retention["maintenance_interval"])
	require.Equal(t, "10s", retention["maintenance_offset"])
	require.Equal(t, "30s", retention["maintenance_timeout"])
	require.Equal(t, 25000, retention["max_rows_per_pass"])
	require.Equal(t, "24h", retention["execution_detail_retention"])
	require.Equal(t, "720h", retention["scheduled_run_summary_retention"])
	require.Equal(t, "336h", retention["terminal_retry_retention"])
	require.Equal(t, "720h", retention["period_snapshot_retention"])
}

func TestRenderCollectorSCFAccessPreservesAssignedKeyID(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{SCFFetcher: SCFFetcher{Spaces: []SCFFetcherSpace{{SpaceID: "crypto", AccessAddress: "storage.example:11004", AccessID: "access@storage", AccessAddresses: map[string]string{"ap-singapore": "regional.example:11004"}, AccessIDs: map[string]string{"ap-singapore": "access@regional-sg"}}}}}}
	rendered, err := RenderCollectorRuntimeConfig(snapshot, []byte("scf_access:\n  key_id: assigned-scf-key-17\ndatabase:\n  path: keep.db\ncollector_runtime:\n  gateway_target: ip://old.example:11003\nstorage:\n  gateway_target: ip://old-storage.example:11003\n  result_data_node_id: keep-node\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	access := got["scf_access"].(map[string]any)
	require.Equal(t, "assigned-scf-key-17", access["key_id"])
	require.Equal(t, "access@storage", access["access_id"])
	require.Equal(t, "storage.example:11004", access["access_address"])
	require.Equal(t, "scf-collector", access["caller"])
	require.Equal(t, "keep.db", got["database"].(map[string]any)["path"])
	require.NotContains(t, got, "collector_runtime")
	require.Equal(t, map[string]any{"result_data_node_id": "keep-node"}, got["storage"])
}

func TestRenderCollectorSCFAccessRejectsConflictingRegionalRoutes(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{SCFFetcher: SCFFetcher{Spaces: []SCFFetcherSpace{
		{SpaceID: "crypto", AccessAddresses: map[string]string{"ap-singapore": "first.example:11004"}, AccessID: "access@first"},
		{SpaceID: "stockcn", AccessAddresses: map[string]string{"ap-singapore": "second.example:11004"}, AccessID: "access@second"},
	}}}}
	_, err := RenderCollectorRuntimeConfig(snapshot, nil)
	require.ErrorContains(t, err, "must agree across Spaces")
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
