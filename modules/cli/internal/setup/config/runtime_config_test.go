package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRenderCollectorRuntimeConfigRendersEgressDNS(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{
		DNSResolver: DNSResolver{
			Enabled:                true,
			TradeNode:              "compute-1",
			RefreshIntervalSeconds: 300,
			RequestTimeoutMS:       3000,
			CacheTTLSeconds:        300,
			Domains:                []string{"FAPI.binance.com.", "api.binance.com"},
		},
		OtherHosts: []Host{{Name: "compute-1", Address: "43.132.204.177", Password: "do-not-copy"}},
	}}
	rendered, err := RenderCollectorRuntimeConfig(snapshot, []byte(`database:
  path: ./collector.db
egress_proxy:
  domains: [stale.example.com]
`))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	require.Equal(t, "./collector.db", got["database"].(map[string]any)["path"])
	egress := got["egress_proxy"].(map[string]any)
	require.Equal(t, []any{}, egress["domains"], "moox.toml 没有 HTTP 白名单时全部直连")
	dns := egress["dns"].(map[string]any)
	require.Equal(t, []any{"fapi.binance.com", "api.binance.com"}, dns["domains"])
	require.Equal(t, "300s", dns["refresh_interval"])
	require.Equal(t, "3000ms", dns["request_timeout"])
	require.Equal(t, "300s", dns["cache_ttl"])
	require.NotContains(t, string(rendered), "do-not-copy")
	require.NotContains(t, string(rendered), "43.132.204.177", "出口代理经服务目录寻址，Collector 配置里不写主机地址")
}

func TestRenderCollectorRuntimeConfigDisabledResolverUsesLocalDNS(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{DNSResolver: DNSResolver{Enabled: false, Domains: []string{"fapi.binance.com"}}}}
	rendered, err := RenderCollectorRuntimeConfig(snapshot, []byte("egress_proxy:\n  dns:\n    domains: [fapi.binance.com]\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	dns := got["egress_proxy"].(map[string]any)["dns"].(map[string]any)
	require.Equal(t, []any{}, dns["domains"])
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

func TestWriteRenderedRuntimeConfigPreservesModeAndReplacesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector", "app.yaml")
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
