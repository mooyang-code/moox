package topology

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRenderEgressAndCollectorSeparatePolicyFromRouting(t *testing.T) {
	snapshot := &Config{EgressProxy: EgressProxy{
		HTTPDomains: []string{"*.binance.com", "data-api.binance.vision"},
	}}
	raw, err := RenderEgressConfig(snapshot, []byte("unrelated: keep\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &got))
	require.Equal(t, "keep", got["unrelated"])
	require.Equal(t, []any{"*.binance.com", "data-api.binance.vision"}, got["domains"])
	require.NotContains(t, got, "dns")
	raw, err = RenderCollectorRuntimeConfig(snapshot, []byte("database: {path: keep.db}\n"))
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(raw, &got))
	require.Equal(t, "keep.db", got["database"].(map[string]any)["path"])
	require.Equal(t, map[string]any{"domains": []any{"*.binance.com", "data-api.binance.vision"}}, got["egress_proxy"])
}

func TestRenderCollectorRuntimeConfigIncludesRetentionPolicy(t *testing.T) {
	snapshot := &Config{CollectorRetention: CollectorRetention{
		MaintenanceInterval: "2m", MaintenanceOffset: "10s", MaintenanceTimeout: "30s", MaxRowsPerPass: 25000,
	}}
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
		rendered, err := RenderCollectorRuntimeConfig(&Config{Deployments: tc.deployments}, template)
		require.NoError(t, err, name)
		var got map[string]any
		require.NoError(t, yaml.Unmarshal(rendered, &got), name)
		require.Equal(t, tc.want, got["delist"].(map[string]any)["auto_detect"], name)
	}
}

// 全新安装以仓库里新版 Collector 的模板为底：模板自带 delist 段，渲染后按拓扑写入正确的值。
func TestRenderCollectorDelistFromShippedTemplate(t *testing.T) {
	template, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "collector", "config", "app.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(template), "\ndelist:\n", "新版 Collector 模板应自带 delist 段")
	for name, tc := range map[string]struct {
		deployments map[string][]string
		want        bool
	}{
		"有海外机":  {map[string][]string{"control": {"admin", "collector"}, "overseas": {"egress-proxy", "trade"}}, true},
		"没配海外机": {map[string][]string{"control": {"admin", "collector"}}, false},
	} {
		rendered, err := RenderCollectorRuntimeConfig(&Config{Deployments: tc.deployments}, template)
		require.NoError(t, err, name)
		var got map[string]any
		require.NoError(t, yaml.Unmarshal(rendered, &got), name)
		require.Equal(t, tc.want, got["delist"].(map[string]any)["auto_detect"], name)
	}
}

// 旧版 Collector 的配置没有 delist 段：渲染不新增，免得还没换二进制的 Collector 读到不认识的键。
func TestRenderCollectorDoesNotAddDelistToOldConfig(t *testing.T) {
	deployments := map[string][]string{"control": {"admin", "collector"}, "overseas": {"egress-proxy", "trade"}}
	rendered, err := RenderCollectorRuntimeConfig(&Config{Deployments: deployments}, []byte("database:\n  path: ./collector.db\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	require.NotContains(t, got, "delist")
	require.Equal(t, "./collector.db", got["database"].(map[string]any)["path"])
}

// 出口与 Collector 的契约：用仓库里的示例配置和随发布的模板渲染，
// 白名单两边一致，不渲染 DNS 快照与旧的路由字段，输出里没有任何密钥字段。
func TestRenderedRuntimeConfigFromShippedTemplates(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "moox.toml.example"))
	require.NoError(t, err)
	cfg, err := Parse(example)
	require.NoError(t, err)
	egressTemplate, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "egressproxy", "config", "app.yaml"))
	require.NoError(t, err)
	collectorTemplate, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "collector", "config", "app.yaml"))
	require.NoError(t, err)
	egress, err := RenderEgressConfig(&cfg, egressTemplate)
	require.NoError(t, err)
	collector, err := RenderCollectorRuntimeConfig(&cfg, collectorTemplate)
	require.NoError(t, err)
	require.Contains(t, string(egress), "data-api.binance.vision")
	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal(collector, &parsed))
	require.Equal(t, map[string]any{"domains": []any{"*.binance.com", "data-api.binance.vision", "openapi.okx.com"}}, parsed["egress_proxy"])
	forbidden := regexp.MustCompile(`(?m)(^|[^A-Za-z0-9_])(dns|target|node_id|dns_resolver|secret_id|secret_key):`)
	for _, rendered := range []string{string(egress), string(collector)} {
		require.Empty(t, forbidden.FindAllString(rendered, -1))
	}
}
