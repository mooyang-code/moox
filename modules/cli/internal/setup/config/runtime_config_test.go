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
			SpaceID: "stock_cn", TimerFunctionCount: 200, MeasuredSafeGroupSize: 30,
			StaggerStartSecond: DefaultStockCNStaggerStartSecond, StaggerWindowSeconds: DefaultStockCNStaggerWindowSeconds, StaggerMaxStartsPerSecond: DefaultStockCNStaggerMaxStartsPerSecond,
		}}},
	}}
	rendered, err := RenderCollectorDNSResolverConfig(snapshot, []byte("database:\n  path: ./collector.db\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	stock := got["stock_cn"].(map[string]any)
	require.Equal(t, 200, stock["expected_timer_function_count"])
	require.Equal(t, 30, stock["measured_safe_group_size"])
	require.Equal(t, 5, stock["stagger_start_second"])
	require.Equal(t, 35, stock["stagger_window_seconds"])
	require.Equal(t, 6, stock["stagger_max_starts_per_second"])
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
