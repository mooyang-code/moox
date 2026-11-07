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

func TestRenderCollectorRuntimeConfigIncludesRetentionPolicy(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{CollectorRetention: CollectorRetention{
		MaintenanceInterval: "2m", MaintenanceOffset: "10s", MaintenanceTimeout: "30s", MaxRowsPerPass: 25000,
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
}

// 下架自动检测要经出口代理：配了海外机（有 egress-proxy）为 true，没配为 false；模板里原来的值被覆盖。
func TestRenderCollectorDelistAutoDetectFollowsEgressProxy(t *testing.T) {
	template := []byte("delist:\n  auto_detect: true\n")
	for name, tc := range map[string]struct {
		deployments map[string][]string
		want        bool
	}{
		"有海外机":  {map[string][]string{"control": {"admin", "collector"}, "overseas": {"egress-proxy", "trade"}}, true},
		"没配海外机": {map[string][]string{"control": {"admin", "collector"}}, false},
	} {
		rendered, err := RenderCollectorRuntimeConfig(&Snapshot{Manifest: Manifest{Deployments: tc.deployments}}, template)
		require.NoError(t, err, name)
		var got map[string]any
		require.NoError(t, yaml.Unmarshal(rendered, &got), name)
		require.Equal(t, tc.want, got["delist"].(map[string]any)["auto_detect"], name)
	}
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
