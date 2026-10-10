package config

import (
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// threeHostManifest 是三台主机的部署表：control、storage（含外部接入）和 compute-1（出口代理、交易、外部接入）。
const threeHostManifest = `
[hosts.storage]
address = "192.0.2.20"
private_address = "10.206.0.5"
region = "ap-nanjing"
provider = "Tencent"
ssh = { username = "ubuntu", password = "storage-password" }

[hosts.compute-1]
address = "192.0.2.30"
region = "ap-hongkong"
root = "/home/ubuntu/moox"
ssh = { port = 2222, username = "ubuntu", password = "compute-password" }

[egress_proxy]
http_domains = ["FAPI.Binance.com", "*.binance.com"]

[egress_proxy.dns]
domains = ["FAPI.BINANCE.COM.", "api.binance.com"]
refresh_interval_seconds = 120
`

func loadManifest(t *testing.T, body string) (*Snapshot, error) {
	t.Helper()
	root := t.TempDir()
	return Load(writeManifest(t, root, body, 0o600), root)
}

// withPlacements 把 [placements] 换成给定的内容。
func withPlacements(body, placements string) string {
	start := strings.Index(body, "[placements]")
	end := strings.Index(body[start:], "\n\n")
	if end < 0 {
		return body[:start] + placements
	}
	return body[:start] + placements + body[start+end:]
}

func TestLoadValidManifest(t *testing.T) {
	snapshot, err := loadManifest(t, validManifest)
	require.NoError(t, err)
	manifest := snapshot.Manifest
	assert.Equal(t, "admin", manifest.Admin.Username)
	assert.Equal(t, 4222, manifest.EventBus.Port)
	assert.True(t, manifest.EventBus.TLSEnabled)
	assert.Equal(t, "ap-guangzhou", manifest.TencentCloud.Region)
	assert.Equal(t, DefaultDeployRoot, manifest.Paths.DeployRoot)
	control := manifest.ControlHost()
	assert.Equal(t, "control", control.ID)
	assert.Equal(t, "/data/moox/control", control.Root, "root 默认为 <deploy_root>/<主机 ID>")
	assert.Equal(t, 22, control.SSH.Port)
	assert.Equal(t, []string{"control"}, manifest.HostIDs())
	assert.Equal(t, "control", manifest.EventBusHost().ID)
	assert.Equal(t, "tls://192.0.2.10:4222", manifest.EventBusURL())
	assert.Equal(t, "1m", manifest.StorageView.MaintenanceCheckInterval)
	assert.Equal(t, "1h", manifest.StorageView.CapacityCheckInterval)
	assert.Equal(t, "1h0m0s", manifest.StorageView.CapacityCheckJitter)
	assert.Equal(t, int64(1<<30), manifest.StorageView.MaxViewFileBytes)
	policy := manifest.StoragePolicy()
	assert.Equal(t, storagepolicy.Default().Retention, policy.Retention, "省略 [storage_retention] 时使用推荐的保留期")
	assert.Equal(t, uint64(5000), policy.View.Bars)
	assert.Equal(t, uint64(6000), policy.View.TrimBars)
	require.NoError(t, policy.Validate())
	assert.Equal(t, 50, manifest.LocalLogs.MaxSizeMB)
	assert.Equal(t, 5, manifest.LocalLogs.BackupCount)
	assert.Empty(t, manifest.Notification.WebhookURL)
	assert.False(t, manifest.CompileHost.Configured())
	require.NoError(t, snapshot.VerifyUnchanged())
}

func TestLoadHostsAndPlacements(t *testing.T) {
	body := withPlacements(validManifest, `[placements]
control = ["console-proxy", "web-host", "admin", "eventbus", "monitor", "collector"]
storage = ["storage-primary", "storage-node", "storage-view", "access"]
compute-1 = ["trade", "access", "egress-proxy"]`) + threeHostManifest
	snapshot, err := loadManifest(t, body)
	require.NoError(t, err)
	manifest := snapshot.Manifest
	assert.Equal(t, []string{"control", "compute-1", "storage"}, manifest.HostIDs(), "control 在前，其余按 ID 排序")

	storage, ok := manifest.Host("storage")
	require.True(t, ok)
	assert.Equal(t, "10.206.0.5", storage.PrivateAddress)
	assert.Equal(t, "ap-nanjing", storage.Region)
	assert.Equal(t, "tencent", storage.Provider)
	assert.Equal(t, "/data/moox/storage", storage.Root)
	compute, _ := manifest.Host("compute-1")
	assert.Equal(t, "/home/ubuntu/moox", compute.Root)
	assert.Equal(t, 2222, compute.SSH.Port)

	assert.Equal(t, []string{"storage-primary", "storage-node", "storage-view", "access"}, manifest.Components("storage"))
	assert.Equal(t, []string{"compute-1", "storage"}, manifest.HostsOf("access"))
	assert.Equal(t, []string{"control", "compute-1", "storage"}, manifest.HostsOf("host-gateway"), "主机组件部署在每台主机上")
	assert.True(t, manifest.HasComponent("storage", "host-agent"))
	assert.False(t, manifest.HasComponent("control", "access"))
	deployment := manifest.Deployment()
	assert.Len(t, deployment.Hosts, 3)
	assert.Len(t, deployment.Placements, 13)

	assert.Equal(t, []string{"fapi.binance.com", "*.binance.com"}, manifest.EgressProxy.HTTPDomains)
	assert.Equal(t, []string{"fapi.binance.com", "api.binance.com"}, manifest.EgressProxy.DNS.Domains)
	assert.Equal(t, 120, manifest.EgressProxy.DNS.RefreshIntervalSeconds)
	assert.Equal(t, 3000, manifest.EgressProxy.DNS.RequestTimeoutMS, "省略的 DNS 参数使用默认值")
	assert.Equal(t, 4, manifest.EgressProxy.DNS.MaxIPsPerDomain)
}

func TestLoadPathsDeployRootAppliesToHostsWithoutRoot(t *testing.T) {
	snapshot, err := loadManifest(t, validManifest+"\n[paths]\ndeploy_root = \"/srv/moox\"\n")
	require.NoError(t, err)
	assert.Equal(t, "/srv/moox/control", snapshot.Manifest.ControlHost().Root)
}

func TestLoadOptionalCompileHost(t *testing.T) {
	snapshot, err := loadManifest(t, validManifest+`
[compile_host]
address = "192.0.2.99"
provider = "tencent"
ssh = { username = "builder", password = "compile-password" }
`)
	require.NoError(t, err)
	compile := snapshot.Manifest.CompileHost
	assert.True(t, compile.Configured())
	assert.Equal(t, "192.0.2.99", compile.Address)
	assert.Equal(t, 22, compile.SSH.Port)
	assert.Equal(t, []string{"control"}, snapshot.Manifest.HostIDs(), "编译主机不是 MooX 主机")
}

func TestLoadRejectsLegacyKeysWithMigrationHint(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"control_host":  {validManifest + "\n[control_host]\nname = \"control\"\nhost = \"192.0.2.10\"\n", "[hosts.<主机 ID>]"},
		"other_hosts":   {validManifest + "\n[[other_hosts]]\nname = \"compute\"\nhost = \"192.0.2.11\"\n", "[placements]"},
		"dns_resolver":  {validManifest + "\n[dns_resolver]\nenabled = true\n", "[egress_proxy.dns]"},
		"eventbus.host": {strings.Replace(validManifest, "port = 4222\n", "host = \"192.0.2.10\"\nport = 4222\n", 1), "eventbus.host 已删除"},
		"control_root":  {validManifest + "\n[paths]\ncontrol_root = \"/data/moox/prod\"\n", "root = \"/data/moox/prod\""},
		"hosts by address": {
			strings.Replace(validManifest, "[hosts.control]", "[hosts.\"192.0.2.11\"]\nport = 22\n\n[hosts.control]", 1), "以主机 ID 为键",
		},
		"compile_host.host":  {validManifest + "\n[compile_host]\nhost = \"192.0.2.99\"\n", "[compile_host] 改为 address"},
		"scf gateway target": {validManifest + "\n[scf_fetcher]\n[[scf_fetcher.spaces]]\nstorage_gateway_host = \"x\"\n", "自动选择外部接入"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadManifest(t, tc.body)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestLoadRejectsInvalidManifest(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing admin password", body: strings.Replace(validManifest, `password = "admin-password"`, `password = ""`, 1), want: "admin.password"},
		{name: "bcrypt password too long", body: strings.Replace(validManifest, "admin-password", strings.Repeat("x", 73), 1), want: "72 bytes"},
		{name: "missing secret id", body: strings.Replace(validManifest, `secret_id = "secret-id"`, `secret_id = ""`, 1), want: "tencent_cloud.secret_id"},
		{name: "missing secret key", body: strings.Replace(validManifest, `secret_key = "secret-key"`, `secret_key = ""`, 1), want: "tencent_cloud.secret_key"},
		{name: "empty region", body: strings.Replace(validManifest, `secret_key = "secret-key"`, "secret_key = \"secret-key\"\nregion = \" \"", 1), want: "tencent_cloud.region"},
		{name: "eventbus explicit zero port", body: strings.Replace(validManifest, "port = 4222", "port = 0", 1), want: "eventbus.port"},
		{name: "eventbus invalid port", body: strings.Replace(validManifest, "port = 4222", "port = 70000", 1), want: "eventbus.port"},
		{name: "eventbus tls disabled", body: strings.Replace(validManifest, "tls_enabled = true", "tls_enabled = false", 1), want: "eventbus.tls_enabled"},
		{name: "notification webhook must use HTTPS", body: strings.Replace(validManifest, `webhook_url = ""`, `webhook_url = "http://example.test/hook"`, 1), want: "notification.webhook_url"},
		{name: "notification webhook must match channel host", body: strings.Replace(validManifest, `webhook_url = ""`, `webhook_url = "https://open.feishu.cn/hook"`, 1), want: "notification.webhook_url"},
		{name: "unknown field", body: "unexpected = true\n" + validManifest, want: "unknown field"},
		{name: "missing control host", body: strings.Replace(withPlacements(validManifest, "[placements]\nstorage = [\"admin\"]"), "[hosts.control]", "[hosts.storage]", 1), want: "[hosts.control]"},
		{name: "invalid host id", body: strings.Replace(validManifest, "[placements]", "[hosts.Storage_A]\naddress = \"192.0.2.20\"\nssh = { username = \"u\", password = \"p\" }\n\n[placements]", 1), want: "主机 ID"},
		{name: "address with scheme", body: strings.Replace(validManifest, `address = "192.0.2.10"`, `address = "tls://192.0.2.10"`, 1), want: "hosts.control.address"},
		{name: "address with port", body: strings.Replace(validManifest, `address = "192.0.2.10"`, `address = "192.0.2.10:22"`, 1), want: "hosts.control.address"},
		{name: "invalid ssh port", body: strings.Replace(validManifest, `ssh = { username`, `ssh = { port = 70000, username`, 1), want: "hosts.control.ssh.port"},
		{name: "missing ssh password", body: strings.Replace(validManifest, `password = "control-password"`, `password = ""`, 1), want: "hosts.control.ssh.password"},
		{name: "relative root", body: strings.Replace(validManifest, "[hosts.control]", "[hosts.control]\nroot = \"data/moox\"", 1), want: "hosts.control.root"},
		{name: "unknown component", body: withPlacements(validManifest, "[placements]\ncontrol = [\"console-proxy\", \"web-host\", \"admin\", \"eventbus\", \"monitor\", \"gateway\"]"), want: "不在组件目录中"},
		{name: "host component in placements", body: withPlacements(validManifest, "[placements]\ncontrol = [\"console-proxy\", \"web-host\", \"admin\", \"eventbus\", \"monitor\", \"host-gateway\"]"), want: "自动部署"},
		{name: "placement on unknown host", body: withPlacements(validManifest, "[placements]\ncontrol = [\"console-proxy\", \"web-host\", \"admin\", \"eventbus\", \"monitor\"]\nmissing = [\"access\"]"), want: "不在 [hosts] 中"},
		{name: "no eventbus", body: withPlacements(validManifest, "[placements]\ncontrol = [\"console-proxy\", \"web-host\", \"admin\", \"monitor\"]"), want: "eventbus"},
		{name: "tls mode without console proxy", body: withPlacements(validManifest, "[placements]\ncontrol = [\"console-proxy\", \"web-host\", \"admin\", \"eventbus\", \"monitor\"]\nstorage = [\"storage-primary\", \"storage-node\", \"storage-view\"]") + "\n[hosts.storage]\naddress = \"192.0.2.20\"\ntls_mode = \"public\"\nssh = { username = \"u\", password = \"p\" }\n", want: "tls_mode"},
		{name: "access without region", body: withPlacements(validManifest, "[placements]\ncontrol = [\"console-proxy\", \"web-host\", \"admin\", \"eventbus\", \"monitor\", \"access\"]"), want: "region"},
		{name: "invalid egress domain", body: validManifest + "\n[egress_proxy]\nhttp_domains = [\"https://binance.com\"]\n", want: "egress_proxy.http_domains"},
		{name: "duplicate dns domain", body: validManifest + "\n[egress_proxy.dns]\ndomains = [\"api.binance.com\", \"API.BINANCE.COM.\"]\n", want: "重复"},
		{name: "compile host without username", body: validManifest + "\n[compile_host]\naddress = \"192.0.2.99\"\n", want: "compile_host.ssh.username"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadManifest(t, tt.body)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "admin-password")
			assert.NotContains(t, err.Error(), "secret-key")
			assert.NotContains(t, err.Error(), "control-password")
		})
	}
}

func TestRenderCollectorRuntimeConfigRendersEgressWhenProxyIsPlaced(t *testing.T) {
	body := withPlacements(validManifest, `[placements]
control = ["console-proxy", "web-host", "admin", "eventbus", "monitor", "collector"]
compute-1 = ["egress-proxy"]`) + threeHostManifest
	snapshot, err := loadManifest(t, body)
	require.NoError(t, err)
	rendered, err := RenderCollectorRuntimeConfig(snapshot, []byte("database:\n  path: ./collector.db\negress_proxy:\n  domains: [stale.example.com]\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	require.Equal(t, "./collector.db", got["database"].(map[string]any)["path"])
	egress := got["egress_proxy"].(map[string]any)
	require.Equal(t, []any{"fapi.binance.com", "*.binance.com"}, egress["domains"])
	dns := egress["dns"].(map[string]any)
	require.Equal(t, []any{"fapi.binance.com", "api.binance.com"}, dns["domains"])
	require.Equal(t, "120s", dns["refresh_interval"])
	require.Equal(t, "3000ms", dns["request_timeout"])
	require.Equal(t, "300s", dns["cache_ttl"])
	require.NotContains(t, string(rendered), "compute-password")
	require.NotContains(t, string(rendered), "192.0.2.30", "出口代理经服务目录寻址，Collector 配置里不写主机地址")
}

func TestRenderCollectorRuntimeConfigWithoutEgressProxyConnectsDirectly(t *testing.T) {
	snapshot, err := loadManifest(t, validManifest+"\n[egress_proxy]\nhttp_domains = [\"fapi.binance.com\"]\n\n[egress_proxy.dns]\ndomains = [\"fapi.binance.com\"]\n")
	require.NoError(t, err)
	rendered, err := RenderCollectorRuntimeConfig(snapshot, []byte("egress_proxy:\n  dns:\n    domains: [fapi.binance.com]\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	egress := got["egress_proxy"].(map[string]any)
	require.Equal(t, []any{}, egress["domains"])
	require.Equal(t, []any{}, egress["dns"].(map[string]any)["domains"])
}
